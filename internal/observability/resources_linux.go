//go:build linux

package observability

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func containerResources() (resourceSample, error) {
	membership, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return resourceSample{}, err
	}
	for _, line := range strings.Split(string(membership), "\n") {
		if path, ok := strings.CutPrefix(line, "0::"); ok {
			return readCgroup(filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(path, "/")))
		}
	}
	return resourceSample{}, fmt.Errorf("cgroup v2 unavailable")
}
