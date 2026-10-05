package loadtest

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func resourceConfig() Config {
	c := testConfig()
	c.RequireResources = true
	c.WorkerCPUs = 2
	c.MaxCPUUtilization = .8
	c.MaxThrottleFraction = .2
	c.MetricsURLs = []string{"http://worker"}
	return c
}
func qualifiedResourceStage(c Config) Stage {
	w := &WorkerEvidence{WorkerID: "worker", PeakActive: 2, PeakBrowserRSS: 128 * 1024 * 1024, PeakServiceRSS: 16 * 1024 * 1024, SteadySamples: 3,
		resourceEvidence: resourceEvidence{ResourceSteadySamples: 3, PeakContainerMemory: 200 * 1024 * 1024, ContainerMemoryLimit: c.WorkerMemoryBytes, CPULimit: c.WorkerCPUs, SteadyCPUP95: .5, SteadyThrottleP95: .1, PeakPIDs: 80, PIDLimit: 256}}
	return Stage{Concurrency: 2, Attempted: 2, Completed: 2, Failures: map[string]int{}, Workers: map[string]*WorkerEvidence{"worker": w}}
}
func TestContainerLimitsEventsAndHeadroomGateCapacity(t *testing.T) {
	c := resourceConfig()
	stage := qualifiedResourceStage(c)
	qualify(&stage, c)
	if !stage.Qualified {
		t.Fatal(stage.Reasons)
	}
	for name, change := range map[string]func(*WorkerEvidence){
		"memory":   func(w *WorkerEvidence) { w.PeakContainerMemory = c.WorkerMemoryBytes },
		"mismatch": func(w *WorkerEvidence) { w.CPULimit = 4 },
		"oom":      func(w *WorkerEvidence) { w.OOMEvents = 1 },
		"cpu":      func(w *WorkerEvidence) { w.SteadyCPUP95 = .9 },
		"throttle": func(w *WorkerEvidence) { w.SteadyThrottleP95 = .9 },
		"tasks":    func(w *WorkerEvidence) { w.PeakPIDs = 250 },
		"swap":     func(w *WorkerEvidence) { w.PeakSwap = 1 },
		"stale":    func(w *WorkerEvidence) { w.ResourceUnhealthy = true },
	} {
		t.Run(name, func(t *testing.T) {
			s := qualifiedResourceStage(c)
			change(s.Workers["worker"])
			qualify(&s, c)
			if s.Qualified || len(recommendations([]Stage{s}, c)) > 0 {
				t.Fatal("unsafe capacity recommended")
			}
		})
	}
}
func TestContainerCPUUsesSteadyDeltasAndDetectsRestart(t *testing.T) {
	now := float64(time.Now().UnixNano()) / 1e9
	w := &WorkerEvidence{}
	base := resourceSnapshot{OK: true, Stamp: now - 3, Memory: 100, PeakMemory: 200, Limit: 4096, CPULimit: 2, CPU: 1, Periods: 10, PIDs: 10, PIDLimit: 128}
	w.observeResource(base, false, 2)
	base.Stamp++
	base.CPU += 2
	base.Periods += 10
	base.ThrottledPeriods += 9
	w.observeResource(base, true, 2)
	if w.ResourceSteadySamples != 0 {
		t.Fatal("startup CPU contaminated steady measurement")
	}
	base.Stamp++
	base.CPU += 1
	base.Periods += 10
	base.ThrottledPeriods += 1
	w.observeResource(base, true, 2)
	if w.ResourceSteadySamples != 1 || w.SteadyCPUP95 != .5 || w.SteadyThrottleP95 != .1 {
		t.Fatal("incorrect CPU quota normalization")
	}
	base.Stamp++
	base.CPU = 0
	w.observeResource(base, true, 2)
	if !w.ResourceUnhealthy {
		t.Fatal("worker restart passed resource qualification")
	}
	data, _ := json.Marshal(w)
	if !strings.Contains(string(data), "peak_container_memory_bytes") {
		t.Fatal("container evidence absent from JSON")
	}
}
