//go:build linux

package observability

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
)

func processMemory(ctx context.Context) ([]processRow, error) {
	dirs, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var rows []processRow
	for _, entry := range dirs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || !entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err != nil {
			continue
		} // Raced an exiting process or outside PID visibility.
		row, err := parseProcStat(pid, string(data), uint64(os.Getpagesize()))
		if err == nil {
			rows = append(rows, row)
		}
	}
	return rows, nil
}
