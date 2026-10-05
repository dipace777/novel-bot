package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"sync"
)

type memorySample struct {
	RSS, MaxRSS, ServiceRSS uint64
	Processes               int
	OK                      bool
	Stamp, Interval         float64
}
type memoryCollector struct {
	mu          sync.RWMutex
	sample      memorySample
	descriptors []*prometheus.Desc
}

func newMemoryCollector() *memoryCollector {
	m := &memoryCollector{}
	for _, metric := range []struct{ name, help string }{
		{"novelbot_browser_rss_bytes", "Sum of RSS for all owned Chromium process groups; shared pages may be counted more than once."},
		{"novelbot_browser_rss_max_bytes", "Largest sampled RSS sum for one owned Chromium process group."},
		{"novelbot_browser_processes", "Number of OS processes observed in owned browser process groups."},
		{"novelbot_worker_service_rss_bytes", "RSS of this Go API/worker process from the same OS sample."},
		{"novelbot_browser_memory_sample_success", "1 if the last OS memory sample succeeded and all known browser groups were observed; 0 otherwise."},
		{"novelbot_browser_memory_sample_timestamp_seconds", "Unix timestamp of the last successful OS memory sample."},
		{"novelbot_browser_memory_sample_interval_seconds", "Configured interval between worker memory samples."},
	} {
		m.descriptors = append(m.descriptors, prometheus.NewDesc(metric.name, metric.help, nil, nil))
	}
	return m
}
func (m *memoryCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, descriptor := range m.descriptors {
		ch <- descriptor
	}
}
func (m *memoryCollector) Collect(ch chan<- prometheus.Metric) {
	// Copy once so RSS, timestamp, and success always refer to the same sample.
	m.mu.RLock()
	sample := m.sample
	m.mu.RUnlock()
	ok := float64(0)
	if sample.OK {
		ok = 1
	}
	values := []float64{float64(sample.RSS), float64(sample.MaxRSS), float64(sample.Processes), float64(sample.ServiceRSS), ok, sample.Stamp, sample.Interval}
	for i, value := range values {
		ch <- prometheus.MustNewConstMetric(m.descriptors[i], prometheus.GaugeValue, value)
	}
}
func (m *memoryCollector) failed() { m.mu.Lock(); m.sample.OK = false; m.mu.Unlock() }
