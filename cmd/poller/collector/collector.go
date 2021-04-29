/*
 * Copyright NetApp Inc, 2021 All rights reserved
 */
package collector

import (
	"path"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"goharvest2/pkg/dload"
	"goharvest2/pkg/errors"
	"goharvest2/pkg/logger"
	"goharvest2/pkg/matrix"
	"goharvest2/pkg/tree/node"

	"goharvest2/cmd/poller/exporter"
	"goharvest2/cmd/poller/options"
	"goharvest2/cmd/poller/plugin"
	"goharvest2/cmd/poller/schedule"

	// built-in plugins

	"goharvest2/cmd/poller/plugin/aggregator"
	//"goharvest2/cmd/poller/plugin/calculator"
	"goharvest2/cmd/poller/plugin/label_agent"
)

type Collector interface {
	Init() error
	Start(*sync.WaitGroup)
	GetName() string
	GetObject() string
	GetParams() *node.Node
	GetOptions() *options.Options
	GetCollectCount() uint64
	AddCollectCount(uint64)
	GetStatus() (uint8, string, string)
	SetStatus(uint8, string)
	SetSchedule(*schedule.Schedule)
	SetMatrix(*matrix.Matrix)
	SetMetadata(*matrix.Matrix)
	WantedExporters() []string
	LinkExporter(exporter.Exporter)
	LoadPlugins(*node.Node) error
}

var CollectorStatus = [3]string{
	"up",
	"standby",
	"failed",
}

type AbstractCollector struct {
	Name         string
	Prefix       string
	Object       string
	Status       uint8
	Message      string
	Options      *options.Options
	Params       *node.Node
	Schedule     *schedule.Schedule
	Matrix       *matrix.Matrix
	Metadata     *matrix.Matrix
	Exporters    []exporter.Exporter
	Plugins      []plugin.Plugin
	collectCount uint64
	countMux     *sync.Mutex
}

func New(name, object string, options *options.Options, params *node.Node) *AbstractCollector {
	c := AbstractCollector{
		Name:     name,
		Object:   object,
		Options:  options,
		Params:   params,
		countMux: &sync.Mutex{},
	}
	c.Prefix = "(collector) (" + name + ":" + object + ")"

	return &c
}

// This is a func not method to enforce "inheritance"
// A collector can to choose to call this function
// inside its Init method, or leave it to be called
// by the poller during dynamic load
func Init(c Collector) error {

	params := c.GetParams()
	options := c.GetOptions()
	name := c.GetName()
	object := c.GetObject()

	/* Initialize schedule and tasks (polls) */
	tasks := params.GetChildS("schedule")
	if tasks == nil || len(tasks.GetChildren()) == 0 {
		return errors.New(errors.MISSING_PARAM, "schedule")
	}

	s := schedule.New()

	// Each task will be mapped to a collector method
	// Example: "data" will be alligned to method PollData()
	for _, task := range tasks.GetChildren() {

		method_name := "Poll" + strings.Title(task.GetNameS())

		if m := reflect.ValueOf(c).MethodByName(method_name); m.IsValid() {
			if foo, ok := m.Interface().(func() (*matrix.Matrix, error)); ok {
				if err := s.AddTaskString(task.GetNameS(), task.GetContentS(), foo); err == nil {
					//logger.Debug(c.Prefix, "scheduled task [%s] with %s interval", task.Name, task.GetInterval().String())

				} else {
					return errors.New(errors.INVALID_PARAM, "schedule ("+task.GetNameS()+"): "+err.Error())
				}
			} else {
				return errors.New(errors.ERR_IMPLEMENT, method_name+" has not signature 'func() (*matrix.Matrix, error)'")
			}
		} else {
			return errors.New(errors.ERR_IMPLEMENT, method_name)
		}
	}
	c.SetSchedule(s)

	/* Initialize Matrix, the container of collected data */
	mx := matrix.New(name, object)
	if export_options := params.GetChildS("export_options"); export_options != nil {
		mx.SetExportOptions(export_options)
	} else {
		mx.SetExportOptions(matrix.DefaultExportOptions())
		// @TODO log warning for user
	}
	mx.SetGlobalLabel("datacenter", params.GetChildContentS("datacenter"))

	if gl := params.GetChildS("global_labels"); gl != nil {
		for _, c := range gl.GetChildren() {
			mx.SetGlobalLabel(c.GetNameS(), c.GetContentS())
		}
	}

	if params.GetChildContentS("export_data") == "false" {
		mx.SetExportable(false)
	}

	c.SetMatrix(mx)

	/* Initialize Plugins */
	if plugins := params.GetChildS("plugins"); plugins != nil {
		if err := c.LoadPlugins(plugins); err != nil {
			return err
		}
	}

	/* Initialize metadata */
	md := matrix.New(name, "metadata_collector")

	md.SetGlobalLabel("hostname", options.Hostname)
	md.SetGlobalLabel("version", options.Version)
	md.SetGlobalLabel("poller", options.Poller)
	md.SetGlobalLabel("collector", name)
	md.SetGlobalLabel("object", object)

	md.NewMetricInt64("poll_time")
	md.NewMetricInt64("task_time")
	md.NewMetricInt64("api_time")
	md.NewMetricInt64("parse_time")
	md.NewMetricInt64("calc_time")
	md.NewMetricInt64("plugin_time")
	md.NewMetricInt64("content_length")
	md.NewMetricFloat64("api_time_percent")
	md.NewMetricUint64("count")
	//md.AddLabel("task", "")
	//md.AddLabel("interval", "")

	/* each task we run is an "instance" */
	for _, task := range s.GetTasks() {
		instance, _ := md.NewInstance(task.Name)
		instance.SetLabel("task", task.Name)
		t := task.GetInterval().Seconds()
		instance.SetLabel("interval", strconv.FormatFloat(t, 'f', 4, 32))
	}

	md.SetExportOptions(matrix.DefaultExportOptions())

	c.SetMetadata(md)
	c.SetStatus(0, "initialized")

	return nil
}

func (me *AbstractCollector) Start(wg *sync.WaitGroup) {

	defer wg.Done()

	// keep track of connection errors
	// to increment time before retry
	retry_delay := 1
	me.SetStatus(0, "running")

	for {

		me.Metadata.Reset()

		results := make([]*matrix.Matrix, 0)

		for _, task := range me.Schedule.GetTasks() {
			if !task.IsDue() {
				continue
			}

			var (
				start, plugin_start    time.Time
				task_time, plugin_time time.Duration
			)

			start = time.Now()
			data, err := task.Run()
			task_time = time.Since(start)

			if err != nil {

				if !me.Schedule.IsStandBy() {
					logger.Debug(me.Prefix, "handling error during [%s] poll...", task.Name)
				}
				switch {
				case errors.IsErr(err, errors.ERR_CONNECTION):
					if retry_delay < 1024 {
						retry_delay *= 4
					}
					if !me.Schedule.IsStandBy() {
						//logger.Error(me.Prefix, err.Error())
						logger.Warn(me.Prefix, "target system unreachable, entering standby mode (retry to connect in %d s)", retry_delay)
					}
					me.Schedule.SetStandByMode(task.Name, time.Duration(retry_delay)*time.Second)
					me.SetStatus(1, errors.ERR_CONNECTION)
				case errors.IsErr(err, errors.ERR_NO_INSTANCE):
					me.Schedule.SetStandByMode(task.Name, 5*time.Minute)
					me.SetStatus(1, errors.ERR_NO_INSTANCE)
					logger.Info(me.Prefix, "no [%s] instances on system, entering standby mode", me.Object)
				case errors.IsErr(err, errors.ERR_NO_METRIC):
					me.SetStatus(1, errors.ERR_NO_METRIC)
					me.Schedule.SetStandByMode(task.Name, 1*time.Hour)
					logger.Warn(me.Prefix, "no [%s] metrics on system, entering standby mode", me.Object)
				default:
					// enter failed state
					logger.Error(me.Prefix, err.Error())
					if errmsg := errors.GetClass(err); errmsg != "" {
						me.SetStatus(2, errmsg)
					} else {
						me.SetStatus(2, err.Error())
					}
					return
				}
				// don't continue on errors
				break
			} else if me.Schedule.IsStandBy() {
				// recover from standby mode
				me.Schedule.Recover()
				me.SetStatus(0, "running")
				logger.Info(me.Prefix, "recovered from standby mode, back to normal schedule")
			}

			if data != nil {
				results = append(results, data)

				if task.Name == "data" {

					plugin_start = time.Now()

					for _, plg := range me.Plugins {
						if plg_data_slice, err := plg.Run(data); err != nil {
							logger.Error(me.Prefix, "plugin [%s]: %s", plg.GetName(), err.Error())
						} else if plg_data_slice != nil {
							results = append(results, plg_data_slice...)
							logger.Debug(me.Prefix, "plugin [%s] added (%d) data", plg.GetName(), len(plg_data_slice))
						} else {
							logger.Debug(me.Prefix, "plugin [%s]: completed", plg.GetName())
						}
					}

					plugin_time = time.Since(plugin_start)
					me.Metadata.LazySetValueInt64("plugin_time", task.Name, plugin_time.Microseconds())
				}

				me.Metadata.LazySetValueInt64("poll_time", task.Name, task.Runtime().Microseconds())
				me.Metadata.LazySetValueInt64("task_time", task.Name, task_time.Microseconds())

				if api_time, ok := me.Metadata.LazyGetValueInt64("api_time", task.Name); ok && api_time != 0 {
					me.Metadata.LazySetValueFloat64("api_time_percent", task.Name, float64(api_time)/float64(task_time.Microseconds())*100)
				}

			}
		}

		logger.Debug(me.Prefix, "exporting collected (%d) data", len(results))

		// @TODO better handling when exporter is standby/failed state
		for _, e := range me.Exporters {
			if code, status, reason := e.GetStatus(); code != 0 {
				logger.Warn(me.Prefix, "exporter [%s] down (%d - %s) (%s), skip export", e.GetName(), code, status, reason)
				continue
			}

			if err := e.Export(me.Metadata); err != nil {
				logger.Warn(me.Prefix, "export metadata to [%s]: %s", e.GetName(), err.Error())
			}

			// continue if metadata failed, since it might be specific to metadata
			for _, data := range results {
				if data.IsExportable() {
					if err := e.Export(data); err != nil {
						logger.Error(me.Prefix, "export data to [%s]: %s", e.GetName(), err.Error())
						break
					}
				} else {
					logger.Debug(me.Prefix, "skipped data (%s) (%s) - set non-exportable", data.UUID, data.Object)
				}
			}
		}

		logger.Debug(me.Prefix, "sleeping %s until next poll", me.Schedule.NextDue().String())
		me.Schedule.Sleep()
	}
}

func (me *AbstractCollector) GetName() string {
	return me.Name
}

func (me *AbstractCollector) GetObject() string {
	return me.Object
}

// get and reset of collected data counter
// this and next method are only to report the poller
// how much data we have collected (independent of poll interval)
func (me *AbstractCollector) GetCollectCount() uint64 {
	me.countMux.Lock()
	count := me.collectCount
	me.collectCount = 0
	me.countMux.Unlock()
	return count
}

// add count to the collected data counter
func (me *AbstractCollector) AddCollectCount(n uint64) {
	me.countMux.Lock()
	me.collectCount += n
	me.countMux.Unlock()
}

func (me *AbstractCollector) GetStatus() (uint8, string, string) {
	return me.Status, CollectorStatus[me.Status], me.Message
}

func (me *AbstractCollector) SetStatus(status uint8, msg string) {
	if status < 0 || status >= uint8(len(CollectorStatus)) {
		panic("invalid status code " + strconv.Itoa(int(status)))
	}
	me.Status = status
	me.Message = msg
}

func (me *AbstractCollector) GetParams() *node.Node {
	return me.Params
}

func (me *AbstractCollector) GetOptions() *options.Options {
	return me.Options
}

func (me *AbstractCollector) SetSchedule(s *schedule.Schedule) {
	me.Schedule = s
}

func (me *AbstractCollector) SetMatrix(m *matrix.Matrix) {
	me.Matrix = m
}

func (me *AbstractCollector) SetMetadata(m *matrix.Matrix) {
	me.Metadata = m
}

func (me *AbstractCollector) WantedExporters() []string {
	var names []string
	if e := me.Params.GetChildS("exporters"); e != nil {
		names = e.GetAllChildContentS()
	}
	return names
}

func (me *AbstractCollector) LinkExporter(e exporter.Exporter) {
	// @TODO: add lock if we want to add exporters while collector is running
	//logger.Info(c.LongName, "Adding exporter [%s:%s]", e.GetClass(), e.GetName())
	me.Exporters = append(me.Exporters, e)
}

func (me *AbstractCollector) LoadPlugins(params *node.Node) error {

	var p plugin.Plugin
	var abc *plugin.AbstractPlugin

	for _, x := range params.GetChildren() {

		name := x.GetNameS()
		if name == "" {
			name = x.GetContentS() // some plugins are defined as list elements others as dicts
			x.SetNameS(name)
		}

		abc = plugin.New(me.Name, me.Options, x, me.Params)

		// case 1: available as built-in plugin
		if p = getBuiltinPlugin(name, abc); p != nil {
			logger.Debug(me.Prefix, "loaded built-in plugin [%s]", name)
			// case 2: available as dynamic plugin
		} else {
			binpath := path.Join(me.Options.HomePath, "bin", "plugins", strings.ToLower(me.Name))
			module, err := dload.LoadFuncFromModule(binpath, strings.ToLower(name), "New")
			if err != nil {
				//logger.Error(c.LongName, "load plugin [%s]: %v", name, err)
				return errors.New(errors.ERR_DLOAD, "plugin "+name+": "+err.Error())
			}

			NewFunc, ok := module.(func(*plugin.AbstractPlugin) plugin.Plugin)
			if !ok {
				//logger.Error(c.LongName, "load plugin [%s]: New() has not expected signature", name)
				return errors.New(errors.ERR_DLOAD, name+": New()")
			}
			p = NewFunc(abc)
			logger.Debug(me.Prefix, "loaded dynamic plugin [%s]", name)
		}

		if err := p.Init(); err != nil {
			logger.Error(me.Prefix, "init plugin [%s]: %v", name, err)
			return err
		}
		me.Plugins = append(me.Plugins, p)
	}
	logger.Debug(me.Prefix, "initialized %d plugins", len(me.Plugins))
	return nil
}

func getBuiltinPlugin(name string, abc *plugin.AbstractPlugin) plugin.Plugin {

	if name == "Aggregator" {
		return aggregator.New(abc)
	}

	/*
		if name == "Calculator" {
			return calculator.New(abc)
		}*/

	if name == "LabelAgent" {
		return label_agent.New(abc)
	}

	return nil
}