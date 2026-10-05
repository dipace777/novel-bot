package loadtest

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

type workerSample struct {
	ID                                    string
	Resource                              resourceSnapshot
	Active, Reserved, Starting, Processes int
	BrowserRSS, ServiceRSS                uint64
	Ready, MemoryOK                       bool
	Stamp, Interval                       float64
}

func scrape(ctx context.Context, client *http.Client, origin, token string) (workerSample, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(origin, "/")+"/metrics", nil)
	if err != nil {
		return workerSample{}, fmt.Errorf("invalid metrics endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/plain; version=0.0.4")
	response, err := client.Do(req)
	if err != nil {
		return workerSample{}, fmt.Errorf("worker metrics unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return workerSample{}, fmt.Errorf("worker metrics rejected")
	}
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(io.LimitReader(response.Body, 4*1024*1024))
	if err != nil {
		return workerSample{}, fmt.Errorf("invalid worker metrics")
	}
	values := map[string]float64{}
	var id string
	for _, name := range []string{"novelbot_worker_active_sessions", "novelbot_worker_reserved_sessions", "novelbot_worker_starting_sessions", "novelbot_worker_ready", "novelbot_browser_rss_bytes", "novelbot_worker_service_rss_bytes", "novelbot_browser_processes", "novelbot_browser_memory_sample_success", "novelbot_browser_memory_sample_timestamp_seconds", "novelbot_browser_memory_sample_interval_seconds"} {
		family := families[name]
		if family == nil || len(family.Metric) != 1 || family.Metric[0].Gauge == nil {
			return workerSample{}, fmt.Errorf("required worker gauge missing")
		}
		metric := family.Metric[0]
		workerID := ""
		for _, label := range metric.Label {
			if label.GetName() == "worker_id" {
				workerID = label.GetValue()
			}
		}
		if workerID == "" || (id != "" && workerID != id) {
			return workerSample{}, fmt.Errorf("worker metric identity mismatch")
		}
		id = workerID
		value := metric.Gauge.GetValue()
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1e18 {
			return workerSample{}, fmt.Errorf("invalid worker gauge value")
		}
		values[name] = value
	}
	resource, err := scrapeResource(families, id)
	if err != nil {
		return workerSample{}, err
	}
	return workerSample{Resource: resource, ID: id, Active: int(values["novelbot_worker_active_sessions"]), Reserved: int(values["novelbot_worker_reserved_sessions"]), Starting: int(values["novelbot_worker_starting_sessions"]), Ready: values["novelbot_worker_ready"] == 1, MemoryOK: values["novelbot_browser_memory_sample_success"] == 1, BrowserRSS: uint64(values["novelbot_browser_rss_bytes"]), ServiceRSS: uint64(values["novelbot_worker_service_rss_bytes"]), Processes: int(values["novelbot_browser_processes"]), Stamp: values["novelbot_browser_memory_sample_timestamp_seconds"], Interval: values["novelbot_browser_memory_sample_interval_seconds"]}, nil
}
func scrapeAll(ctx context.Context, client *http.Client, c Config) ([]workerSample, error) {
	type result struct {
		sample workerSample
		err    error
	}
	ch := make(chan result, len(c.MetricsURLs))
	for _, origin := range c.MetricsURLs {
		go func(origin string) {
			bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			s, err := scrape(bounded, client, origin, c.WorkerToken)
			ch <- result{s, err}
		}(origin)
	}
	samples := make([]workerSample, 0, len(c.MetricsURLs))
	ids := map[string]bool{}
	for range c.MetricsURLs {
		r := <-ch
		if r.err != nil {
			return nil, r.err
		}
		if ids[r.sample.ID] {
			return nil, fmt.Errorf("metrics origins must identify distinct workers")
		}
		ids[r.sample.ID] = true
		samples = append(samples, r.sample)
	}
	return samples, nil
}
func observeSamples(s *Stage, samples []workerSample, holding bool) {
	total := 0
	allReady := true
	for _, sample := range samples {
		total += sample.Active
		allReady = allReady && sample.Ready && sample.Starting == 0
	}
	atTarget := holding && allReady && total == s.Concurrency
	for _, sample := range samples {
		evidence := s.Workers[sample.ID]
		if evidence == nil {
			evidence = &WorkerEvidence{WorkerID: sample.ID}
			s.Workers[sample.ID] = evidence
		}
		evidence.observeResource(sample.Resource, atTarget && sample.Active > 0, sample.Interval)
		evidence.PeakActive = max(evidence.PeakActive, sample.Active)
		evidence.PeakReserved = max(evidence.PeakReserved, sample.Reserved)
		if !sample.Ready || total > s.Concurrency {
			evidence.Unhealthy = true
		}
		fresh := sample.MemoryOK && sample.Stamp > evidence.lastStamp && sample.Interval > 0 && time.Since(time.UnixMilli(int64(sample.Stamp*1000))) < time.Duration(sample.Interval*2*float64(time.Second))
		if atTarget && !sample.MemoryOK {
			evidence.Unhealthy = true
		}
		if !fresh {
			continue
		}
		evidence.lastStamp = sample.Stamp
		evidence.Samples++
		evidence.PeakBrowserRSS = max(evidence.PeakBrowserRSS, sample.BrowserRSS)
		evidence.PeakServiceRSS = max(evidence.PeakServiceRSS, sample.ServiceRSS)
		evidence.PeakTotalRSS = max(evidence.PeakTotalRSS, sample.BrowserRSS+sample.ServiceRSS)
		evidence.PeakBrowserProcesses = max(evidence.PeakBrowserProcesses, sample.Processes)
		if atTarget && sample.Active > 0 && sample.BrowserRSS > 0 {
			evidence.SteadySamples++
		}
	}
}

func scrapeResource(families map[string]*dto.MetricFamily, id string) (resourceSnapshot, error) {
	var s resourceSnapshot
	family := families["novelbot_container_sample_success"]
	if family == nil {
		return s, nil
	} // Older/non-container workers remain usable for RSS-only runs.
	values := map[string]float64{}
	for _, name := range []string{"sample_success", "sample_timestamp_seconds", "memory_bytes", "memory_peak_bytes", "memory_limit_bytes", "swap_bytes", "oom_events", "memory_limit_events", "cpu_seconds", "cpu_throttled_seconds", "cpu_limit_cores", "cpu_periods", "cpu_throttled_periods", "pids", "pids_limit", "pids_limit_events"} {
		family := families["novelbot_container_"+name]
		if family == nil || len(family.Metric) != 1 || family.Metric[0].Gauge == nil {
			return s, fmt.Errorf("container resource metric missing")
		}
		metric := family.Metric[0]
		same := false
		for _, label := range metric.Label {
			if label.GetName() == "worker_id" && label.GetValue() == id {
				same = true
			}
		}
		value := metric.Gauge.GetValue()
		if !same || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1e18 {
			return s, fmt.Errorf("invalid container resource metric")
		}
		values[name] = value
	}
	s = resourceSnapshot{OK: values["sample_success"] == 1, Stamp: values["sample_timestamp_seconds"], Memory: uint64(values["memory_bytes"]), PeakMemory: uint64(values["memory_peak_bytes"]), Limit: uint64(values["memory_limit_bytes"]), Swap: uint64(values["swap_bytes"]), OOM: uint64(values["oom_events"]), MemoryHits: uint64(values["memory_limit_events"]), CPU: values["cpu_seconds"], Throttled: values["cpu_throttled_seconds"], CPULimit: values["cpu_limit_cores"], Periods: uint64(values["cpu_periods"]), ThrottledPeriods: uint64(values["cpu_throttled_periods"]), PIDs: uint64(values["pids"]), PIDLimit: uint64(values["pids_limit"]), PIDHits: uint64(values["pids_limit_events"])}
	return s, nil
}
