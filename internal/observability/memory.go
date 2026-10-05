package observability

import (
	"fmt"
	"strconv"
	"strings"
)

type processRow struct {
	PID, Group int
	RSS        uint64
}

// ps emits RSS in KiB on macOS and Linux. No commands or process arguments are read.
func parsePS(data string) ([]processRow, error) {
	var rows []processRow
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 3 {
			return nil, fmt.Errorf("invalid process memory row")
		}
		pid, e1 := strconv.Atoi(fields[0])
		group, e2 := strconv.Atoi(fields[1])
		rss, e3 := strconv.ParseUint(fields[2], 10, 64)
		if e1 != nil || e2 != nil || e3 != nil || rss > ^uint64(0)/1024 {
			return nil, fmt.Errorf("invalid process memory value")
		}
		rows = append(rows, processRow{pid, group, rss * 1024})
	}
	return rows, nil
}

// /proc stat fields start with state (field 3) after the final closing ')'.
func parseProcStat(pid int, data string, pageSize uint64) (processRow, error) {
	end := strings.LastIndexByte(data, ')')
	if end < 0 || pageSize == 0 {
		return processRow{}, fmt.Errorf("invalid process stat")
	}
	fields := strings.Fields(data[end+1:])
	if len(fields) < 22 {
		return processRow{}, fmt.Errorf("short process stat")
	}
	group, e1 := strconv.Atoi(fields[2])
	pages, e2 := strconv.ParseUint(fields[21], 10, 64)
	if e1 != nil || e2 != nil || pages > ^uint64(0)/pageSize {
		return processRow{}, fmt.Errorf("invalid process RSS")
	}
	return processRow{pid, group, pages * pageSize}, nil
}
