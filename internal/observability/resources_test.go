package observability

import (
	"os"
	"path/filepath"
	"testing"
)

func cgroupFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, value := range map[string]string{
		"memory.current": "1000", "memory.peak": "2000", "memory.max": "4294967296", "memory.swap.current": "0",
		"memory.events": "low 0\nhigh 0\nmax 2\noom 1\noom_kill 1\n",
		"cpu.stat":      "usage_usec 1500000\nuser_usec 1000000\nsystem_usec 500000\nnr_periods 10\nnr_throttled 2\nthrottled_usec 100000\n",
		"cpu.max":       "200000 100000", "pids.current": "42", "pids.max": "512", "pids.events": "max 0\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
func TestCgroupReadsEnforcedResourcesAndKernelPeaks(t *testing.T) {
	root := cgroupFixture(t)
	s, err := readCgroup(root)
	if err != nil {
		t.Fatal(err)
	}
	if !s.OK || s.Memory != 1000 || s.PeakMemory != 2000 || s.MemoryLimit != 4294967296 || s.CPUSeconds != 1.5 || s.CPULimit != 2 || s.ThrottledSeconds != .1 || s.Periods != 10 || s.ThrottledPeriods != 2 || s.OOM != 1 || s.MemoryHits != 2 || s.PIDs != 42 || s.PIDLimit != 512 || s.Stamp <= 0 {
		t.Fatalf("incorrect cgroup sample: %+v", s)
	}
	for _, name := range []string{"memory.max", "pids.max"} {
		os.WriteFile(filepath.Join(root, name), []byte("max"), 0600)
	}
	os.WriteFile(filepath.Join(root, "cpu.max"), []byte("max 100000"), 0600)
	s, err = readCgroup(root)
	if err != nil || s.MemoryLimit != 0 || s.PIDLimit != 0 || s.CPULimit != 0 {
		t.Fatal("unlimited resource values misreported", err)
	}
}
func TestCgroupRejectsMissingAndMalformedSamples(t *testing.T) {
	for name, value := range map[string]string{"memory.current": "bad", "memory.max": "-1", "memory.events": "max 0", "cpu.stat": "usage_usec 1", "cpu.max": "100 0", "pids.events": "max invalid"} {
		t.Run(name, func(t *testing.T) {
			root := cgroupFixture(t)
			os.WriteFile(filepath.Join(root, name), []byte(value), 0600)
			if _, err := readCgroup(root); err == nil {
				t.Fatal("incomplete sample accepted")
			}
		})
	}
	root := cgroupFixture(t)
	os.Remove(filepath.Join(root, "memory.peak"))
	if _, err := readCgroup(root); err == nil {
		t.Fatal("missing kernel peak silently accepted")
	}
}
