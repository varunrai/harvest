package matrix

import (
	"fmt"
	"errors"
    "math"
    "strconv"
)

type Counter struct {
	Name string
	Index int
	DisplayName string
	Scalar bool
	Enabled bool
}

type Instance struct {
	Name string
	Index int
	DisplayName string
	Labels map[string]string
}

type Matrix struct {
	Object string
	GlobalLabels map[string]string
	LabelNames map[string]string
    InstanceKeys [][]string
	ExportOptions map[string]string
	Instances map[string]Instance
	Counters map[string]Counter
	CounterIndex int
	Data [][]float64
}

func NewMatrix(object string) *Matrix {
    m := Matrix{Object: object, CounterIndex: 0 }
    m.GlobalLabels = map[string]string{}
    m.LabelNames = map[string]string{}
    m.InstanceKeys = [][]string{}
    m.Instances = map[string]Instance{}
    m.Counters = map[string]Counter{}
    return &m
}

func (m *Matrix) InitData() error {
	var x, y, i, j int
	x = len(m.Counters)
	y = len(m.Instances)
	if x == 0 || y == 0 {
		return errors.New("Counter or Instance cache empty")
	}
	m.Data = make([][]float64, x)
	for i=0; i<x; i+=1 {
		m.Data[i] = make([]float64, y)
		for j=0; j<y; j+=1 {
			m.Data[i][j] = math.NaN()
		}
	}
	return nil
}

func (m *Matrix) AddCounter(key, name string, enabled bool) (Counter, error) {
	var counter Counter
	var exists bool
	var err error
	if _, exists = m.Counters[key]; exists {
		err = errors.New(fmt.Sprintf("Counter [%s] already in cache", key))
	} else {
		counter = Counter{Name: name, Index: m.CounterIndex, Scalar: true, Enabled: enabled}
		m.Counters[key] = counter
		m.CounterIndex += 1
	}
	return counter, err
}

func (m *Matrix) GetCounter(name string) (Counter, bool) {
    var c Counter
    var found bool
    c, found = m.Counters[name]
	return c, found
}

func (m *Matrix) GetCounters() []Counter {
	var c Counter
	var counters []Counter
	counters = make([]Counter, len(m.Counters))
	for _, c = range m.Counters {
		counters = append(counters, c)
	}
	return counters
}

func (m *Matrix) AddInstance(key, name string) (Instance, error) {
	var instance Instance
	var exists bool
	var err error
	if _, exists = m.Instances[key]; exists {
		err = errors.New(fmt.Sprintf("Instance [%s] already in cache", key))
	} else {
		instance = Instance{Name: name, Index: len(m.Instances)}
		m.Instances[key] = instance
	}
	return instance, err
}

func (m *Matrix) GetInstance(name string) (Instance, bool) {
    var i Instance
    var found bool
    i, found = m.Instances[name]
    return i, found
}

func (m *Matrix) GetInstances() []Instance {
	var i Instance
	var instances []Instance
	instances = make([]Instance, len(m.Instances))
	for _, i = range m.Instances {
		instances = append(instances, i)
	}
	return instances
}

func (m *Matrix) SetValue(c Counter, i Instance, value []byte) error {
	var numeric float64
	var err error

    numeric, err = strconv.ParseFloat(string(value), 64)

    if err == nil {
		m.Data[c.Index][i.Index] = numeric
    }
	return err
}

func (m *Matrix) GetValue(c Counter, i Instance) (float64, bool) {
	var value float64
	value = m.Data[c.Index][i.Index]
	return value, value==value

}

func (m *Matrix) AddLabel(key, name string) {
	m.LabelNames[key] = name
}

func (m *Matrix) AddInstanceKey(key []string) {
    m.InstanceKeys = append(m.InstanceKeys, key)
}

func (m *Matrix) SetInstanceLabel(i Instance, label, value string) {
	var display string
	var exists bool

	if display, exists = m.LabelNames[label]; exists {
		i.Labels[display] = value
	} else {
		i.Labels[label] = value
	}
}

func (m *Matrix) GetInstanceLabel(i Instance, display string) (string, bool) {
    var label string
    var found bool
    label, found = i.Labels[display]
    return label, found
}

func (m *Matrix) GetInstanceLabels(i Instance) map[string]string {
	return i.Labels
}

func (m *Matrix) SetGlobalLabel(label, value string) {
	m.GlobalLabels[label] = value
}

func (m *Matrix) GetGlobalLabels() map[string]string {
	return m.GlobalLabels
}
