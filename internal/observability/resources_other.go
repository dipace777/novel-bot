//go:build !linux

package observability

import "errors"

func containerResources() (resourceSample, error) {
	return resourceSample{}, errors.New("cgroup v2 resource metrics require Linux")
}
