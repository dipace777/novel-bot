package observability

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func readCgroup(root string) (resourceSample, error) {
	var s resourceSample
	read := func(name string) (string, error) {
		b, err := os.ReadFile(filepath.Join(root, name))
		return strings.TrimSpace(string(b)), err
	}
	for name, target := range map[string]*uint64{"memory.current": &s.Memory, "memory.peak": &s.PeakMemory, "memory.max": &s.MemoryLimit, "memory.swap.current": &s.Swap, "pids.current": &s.PIDs, "pids.max": &s.PIDLimit} {
		raw, err := read(name)
		if err != nil {
			return s, err
		}
		if raw == "max" && (name == "memory.max" || name == "pids.max") {
			continue
		}
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return s, fmt.Errorf("invalid cgroup %s", name)
		}
		*target = n
	}
	for name, fields := range map[string]map[string]*uint64{
		"memory.events": {"oom": &s.OOM, "max": &s.MemoryHits},
		"pids.events":   {"max": &s.PIDHits},
	} {
		raw, err := read(name)
		if err != nil {
			return s, err
		}
		stats, err := parseStats(raw)
		if err != nil {
			return s, err
		}
		for key, target := range fields {
			n, ok := stats[key]
			if !ok {
				return s, fmt.Errorf("missing cgroup %s", key)
			}
			*target = n
		}
	}
	raw, err := read("cpu.stat")
	if err != nil {
		return s, err
	}
	cpu, err := parseStats(raw)
	if err != nil {
		return s, err
	}
	for _, key := range []string{"usage_usec", "throttled_usec", "nr_periods", "nr_throttled"} {
		if _, ok := cpu[key]; !ok {
			return s, fmt.Errorf("missing cgroup CPU field")
		}
	}
	s.CPUSeconds = float64(cpu["usage_usec"]) / 1e6
	s.ThrottledSeconds = float64(cpu["throttled_usec"]) / 1e6
	s.Periods = cpu["nr_periods"]
	s.ThrottledPeriods = cpu["nr_throttled"]
	raw, err = read("cpu.max")
	if err != nil {
		return s, err
	}
	parts := strings.Fields(raw)
	if len(parts) != 2 {
		return s, fmt.Errorf("invalid CPU quota")
	}
	period, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil || period == 0 {
		return s, fmt.Errorf("invalid CPU period")
	}
	if parts[0] != "max" {
		quota, err := strconv.ParseUint(parts[0], 10, 64)
		if err != nil || quota == 0 {
			return s, fmt.Errorf("invalid CPU quota")
		}
		s.CPULimit = float64(quota) / float64(period)
	}
	s.OK = true
	s.Stamp = float64(time.Now().UnixNano()) / 1e9
	return s, nil
}
func parseStats(raw string) (map[string]uint64, error) {
	result := map[string]uint64{}
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("invalid cgroup stats")
		}
		n, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid cgroup stat value")
		}
		result[fields[0]] = n
	}
	return result, nil
}
