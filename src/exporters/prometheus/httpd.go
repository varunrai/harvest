package main

import (
	"fmt"
	"net/http"
	"bytes"
	"sort"
	"strings"
	"time"
	"goharvest2/poller/struct/matrix"
	"goharvest2/share/logger"
)


func (e *Prometheus) StartHttpd(url, port string) {

	mux := http.NewServeMux()
	mux.HandleFunc("/", e.ServeInfo)
	mux.HandleFunc("/metrics", e.ServeMetrics)

	PORT := ":"+port
	logger.Info(e.Prefix, "Starting server at [%s]", PORT)
	server := &http.Server{ Addr: PORT, Handler: mux}
	go server.ListenAndServe()

}

func (e *Prometheus) ServeInfo(w http.ResponseWriter, r *http.Request) {
	
	body := make([]string, 0)
	//matrix_by_collector := make(map[string][]*matrix.Matrix)
	num_collectors := 0
	num_objects := 0
	num_metrics := 0
	unique_data := map[string]map[string]*matrix.Matrix{}
	unique_metadata := map[string]*matrix.Matrix{}
	//collector_names = map[string]string
	//object_names map[string][]string

	for _, m := range e.cache {

		if m.IsMetadata {
			logger.Debug(e.Prefix, "Cache Metadata= [%-20s] [%-20s] (%d) (%d)", m.Collector, m.Object, len(m.Metrics), len(m.Instances))
			//if _, exists := unique_metadata[m.Collector]; !exists {
			//	unique_metadata[m.Collector] = make(map[string]*matrix.Matrix)
			//}
			unique_metadata[m.Collector] = m
		} else {
			logger.Debug(e.Prefix, "Cache Data=     [%-20s] [%-20s]", m.Collector, m.Object)
			if _, exists := unique_data[m.Collector]; !exists {
				unique_data[m.Collector] = make(map[string]*matrix.Matrix)
			}
			unique_data[m.Collector][m.Object] = m
		}
	}

	for col, per_object := range unique_data {

		objects := make([]string, 0)

		for obj, data := range per_object {

			metrics := make([]string, 0)

			for _, metric := range data.Metrics {
				
				if !metric.Enabled {
					continue
				}

				num_metrics += 1

				if metric.Scalar {
					metrics = append(metrics, fmt.Sprintf(metric_template, obj + "_" + metric.Display))
				} else {
					array_metric := fmt.Sprintf(metric_template, obj + "_" + metric.Display)
					array_metric += "\n<ul>"
					for _, label := range metric.Labels {
						array_metric += "\n" + fmt.Sprintf(metric_template, label)
					}
					array_metric += "\n</ul>"
					metrics = append(metrics, array_metric)
				}
			}

			sort.Strings(metrics)

			objects = append(objects, fmt.Sprintf(object_template, obj, strings.Join(metrics, "\n")))

			//num_metrics += len(metrics)
			num_objects += 1
		}

		if md, exists := unique_metadata[col]; exists {
			metrics := make([]string, 0)
			for _, metric := range md.Metrics {
				metrics = append(metrics, fmt.Sprintf(metric_template, "metadata_" + md.MetadataType + "_" + metric.Display))
			}
			objects = append(objects, fmt.Sprintf(object_template, "metadata", strings.Join(metrics, "\n")))
		}

		body = append(body, fmt.Sprintf(collector_template, col, strings.Join(objects, "\n")))
		num_collectors += 1
	}

	poller := e.options.Poller
	body_flat := fmt.Sprintf(html_template, poller, poller, poller, num_collectors, num_objects, num_metrics, strings.Join(body, "\n\n"))
	
	w.WriteHeader(200)
	w.Header().Set("content-type", "text/html")
	w.Write([]byte(body_flat))
}

func (e *Prometheus) ServeMetrics(w http.ResponseWriter, r *http.Request) {

	logger.Info(e.Prefix, "Serving metrics from %d cached items", len(e.cache))
	sep := []byte("\n")
	var data [][]byte

	start := time.Now()
	count := 0

	for _, m := range e.cache {
		logger.Info(e.Prefix, "Rendering metrics [%s:%s]", m.Collector, m.Object)
		rendered := e.Render(m)

		data = append(data, rendered...)
		count += len(rendered)
	}

	duration := time.Since(start)
	e.Metadata.SetValueSS("time", "render", duration.Seconds())
	e.Metadata.SetValueSS("count", "render", float64(count))

	md := e.Render(e.Metadata)
	data = append(data, md...)
	//data = append(data, sep)

	w.WriteHeader(200)
	w.Header().Set("content-type", "text/plain")
	w.Write(bytes.Join(data, sep))
}
