//go:build unix && !linux

package observability

import (
	"context"
	"os/exec"
)

func processMemory(ctx context.Context) ([]processRow, error) {
	data, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,pgid=,rss=").Output()
	if err != nil {
		return nil, err
	}
	return parsePS(string(data))
}
