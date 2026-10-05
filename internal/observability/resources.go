package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"sync"
)

type resourceSample struct {
	OK                                                     bool
	Stamp                                                  float64
	Memory, PeakMemory, MemoryLimit, Swap, OOM, MemoryHits uint64
	CPUSeconds, ThrottledSeconds, CPULimit                 float64
	Periods, ThrottledPeriods, PIDs, PIDLimit, PIDHits     uint64
}
type resourceCollector struct {
	mu          sync.RWMutex
	sample      resourceSample
	descriptors []*prometheus.Desc
}

func newResourceCollector() *resourceCollector {
	c := &resourceCollector{}
	for _, metric := range []struct{ name, help string }{
		{"sample_success", "1 if the cgroup v2 resource sample is complete; 0 if unavailable."},
		{"sample_timestamp_seconds", "Timestamp of the last complete cgroup resource sample."},
		{"memory_bytes", "Current cgroup memory, including browser children and page cache."},
		{"memory_peak_bytes", "Kernel-recorded cgroup memory peak since container creation."},
		{"memory_limit_bytes", "Enforced cgroup memory limit; 0 means unlimited."},
		{"swap_bytes", "Current cgroup swap usage."},
		{"oom_events", "Cumulative cgroup OOM events."},
		{"memory_limit_events", "Cumulative cgroup memory max events."},
		{"cpu_seconds", "Cumulative cgroup CPU usage in seconds."},
		{"cpu_throttled_seconds", "Cumulative CPU throttled time in seconds."},
		{"cpu_limit_cores", "Cgroup CPU quota in cores; 0 means unlimited."},
		{"cpu_periods", "Cumulative CPU enforcement periods."},
		{"cpu_throttled_periods", "Cumulative periods in which CPU was throttled."},
		{"pids", "Current cgroup processes and threads."},
		{"pids_limit", "Enforced cgroup task limit; 0 means unlimited."},
		{"pids_limit_events", "Cumulative cgroup task-limit rejections."},
	} {
		c.descriptors = append(c.descriptors, prometheus.NewDesc("novelbot_container_"+metric.name, metric.help, nil, nil))
	}
	return c
}
func (c *resourceCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range c.descriptors {
		ch <- d
	}
}
func (c *resourceCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	s := c.sample
	c.mu.RUnlock()
	ok := 0.
	if s.OK {
		ok = 1
	}
	values := []float64{ok, s.Stamp, float64(s.Memory), float64(s.PeakMemory), float64(s.MemoryLimit), float64(s.Swap), float64(s.OOM), float64(s.MemoryHits), s.CPUSeconds, s.ThrottledSeconds, s.CPULimit, float64(s.Periods), float64(s.ThrottledPeriods), float64(s.PIDs), float64(s.PIDLimit), float64(s.PIDHits)}
	// All are snapshot gauges, including cumulative fields; deltas are computed by the load runner.
	for i, v := range values {
		ch <- prometheus.MustNewConstMetric(c.descriptors[i], prometheus.GaugeValue, v)
	}
}
func (c *resourceCollector) update() {
	s, err := containerResources()
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.sample.OK = false
		return
	}
	c.sample = s
}
