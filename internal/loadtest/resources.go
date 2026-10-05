package loadtest

import (
	"math"
	"time"
)

type resourceEvidence struct {
	ResourceSamples             int     `json:"container_samples"`
	ResourceSteadySamples       int     `json:"container_steady_samples"`
	PeakContainerMemory         uint64  `json:"peak_container_memory_bytes"`
	ContainerMemoryLimit        uint64  `json:"container_memory_limit_bytes"`
	PeakSwap                    uint64  `json:"peak_swap_bytes"`
	CPULimit                    float64 `json:"container_cpu_limit_cores"`
	PeakCPUUtilization          float64 `json:"peak_cpu_utilization_fraction"`
	SteadyCPUP95                float64 `json:"steady_cpu_utilization_p95_fraction"`
	SteadyThrottleP95           float64 `json:"steady_throttled_periods_p95_fraction"`
	PeakPIDs                    uint64  `json:"peak_container_tasks"`
	PIDLimit                    uint64  `json:"container_task_limit"`
	OOMEvents                   uint64  `json:"oom_events_during_test"`
	MemoryLimitEvents           uint64  `json:"memory_limit_events_during_test"`
	PIDLimitEvents              uint64  `json:"task_limit_events_during_test"`
	ResourceUnhealthy           bool    `json:"container_samples_unhealthy"`
	previous                    resourceSnapshot
	previousSteady              bool
	cpuSamples, throttleSamples []float64
}
type resourceSnapshot struct {
	OK                                                                        bool
	Stamp                                                                     float64
	Memory, PeakMemory, Limit, Swap, OOM, MemoryHits, PIDs, PIDLimit, PIDHits uint64
	CPU, Throttled, CPULimit                                                  float64
	Periods, ThrottledPeriods                                                 uint64
}

func (w *WorkerEvidence) observeResource(s resourceSnapshot, atTarget bool, interval float64) {
	if !s.OK {
		if atTarget {
			w.ResourceUnhealthy = true
		}
		return
	}
	if s.Stamp <= w.previous.Stamp {
		return
	}
	if time.Since(time.UnixMilli(int64(s.Stamp*1000))) > time.Duration(max(1., interval*2)*float64(time.Second)) {
		w.ResourceUnhealthy = true
		return
	}
	previous := w.previous
	w.ResourceSamples++
	w.ContainerMemoryLimit = s.Limit
	w.CPULimit = s.CPULimit
	w.PIDLimit = s.PIDLimit
	w.PeakContainerMemory = max(w.PeakContainerMemory, s.PeakMemory, s.Memory)
	w.PeakSwap = max(w.PeakSwap, s.Swap)
	w.PeakPIDs = max(w.PeakPIDs, s.PIDs)
	if previous.OK {
		if s.CPU < previous.CPU || s.Periods < previous.Periods || s.ThrottledPeriods < previous.ThrottledPeriods || s.OOM < previous.OOM || s.MemoryHits < previous.MemoryHits || s.PIDHits < previous.PIDHits || s.Limit != previous.Limit || s.CPULimit != previous.CPULimit || s.PIDLimit != previous.PIDLimit {
			w.ResourceUnhealthy = true
			return
		}
		w.OOMEvents += s.OOM - previous.OOM
		w.MemoryLimitEvents += s.MemoryHits - previous.MemoryHits
		w.PIDLimitEvents += s.PIDHits - previous.PIDHits
		if s.CPULimit > 0 {
			utilization := (s.CPU - previous.CPU) / (s.Stamp - previous.Stamp) / s.CPULimit
			throttle := 0.
			if periods := s.Periods - previous.Periods; periods > 0 {
				throttle = float64(s.ThrottledPeriods-previous.ThrottledPeriods) / float64(periods)
			}
			w.PeakCPUUtilization = max(w.PeakCPUUtilization, utilization)
			if atTarget && w.previousSteady {
				w.ResourceSteadySamples++
				w.cpuSamples = append(w.cpuSamples, utilization)
				w.throttleSamples = append(w.throttleSamples, throttle)
				w.SteadyCPUP95 = percentile(append([]float64(nil), w.cpuSamples...), .95)
				w.SteadyThrottleP95 = percentile(append([]float64(nil), w.throttleSamples...), .95)
			}
		}
	}
	w.previous = s
	w.previousSteady = atTarget
}
func resourceReasons(w *WorkerEvidence, c Config) []string {
	var reasons []string
	if w.ResourceUnhealthy || w.ResourceSteadySamples < 2 {
		reasons = append(reasons, "insufficient healthy container resource samples")
	}
	if w.ContainerMemoryLimit != c.WorkerMemoryBytes || w.CPULimit <= 0 || math.Abs(w.CPULimit-c.WorkerCPUs) > .01 {
		reasons = append(reasons, "enforced CPU/memory limits do not match test budgets")
	}
	if float64(w.PeakContainerMemory) > float64(c.WorkerMemoryBytes)*(1-c.Headroom) {
		reasons = append(reasons, "container memory headroom exceeded")
	}
	if w.SteadyCPUP95 > c.MaxCPUUtilization {
		reasons = append(reasons, "steady CPU utilization p95 exceeded threshold")
	}
	if w.SteadyThrottleP95 > c.MaxThrottleFraction {
		reasons = append(reasons, "steady CPU throttling p95 exceeded threshold")
	}
	if w.PeakSwap > 0 || w.OOMEvents > 0 || w.MemoryLimitEvents > 0 || w.PIDLimitEvents > 0 {
		reasons = append(reasons, "swap, OOM, memory-limit, or task-limit events occurred")
	}
	if w.PIDLimit == 0 || float64(w.PeakPIDs) > .8*float64(w.PIDLimit) {
		reasons = append(reasons, "container task headroom insufficient")
	}
	return reasons
}
