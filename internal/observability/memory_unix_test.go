//go:build unix

package observability

import (
	"context"
	"os"
	"testing"
	"time"
)

type groupsStub []int

func (g groupsStub) ProcessGroups() []int { return g }
func TestSamplerMeasuresOwnedProcessGroupAndServiceRSS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rows, err := processMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	group := 0
	for _, row := range rows {
		if row.PID == os.Getpid() {
			group = row.Group
		}
	}
	if group == 0 {
		t.Fatal("Go test process not found in OS snapshot")
	}
	m := New("sample-test")
	sampleCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); m.Sample(sampleCtx, groupsStub{group}, time.Hour) }()
	defer func() { stop(); <-done }()
	for {
		families, err := m.registry.Gather()
		if err != nil {
			t.Fatal(err)
		}
		values := map[string]float64{}
		for _, family := range families {
			if len(family.Metric) == 1 {
				values[family.GetName()] = family.Metric[0].GetGauge().GetValue()
			}
		}
		if values["novelbot_browser_memory_sample_success"] == 1 {
			if values["novelbot_browser_rss_bytes"] <= 0 || values["novelbot_worker_service_rss_bytes"] <= 0 || values["novelbot_browser_processes"] < 1 {
				t.Fatal("incomplete process-group sample")
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("memory sample did not succeed")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
