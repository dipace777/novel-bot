//go:build !unix

package observability

import (
	"context"
	"errors"
)

func processMemory(context.Context) ([]processRow, error) {
	return nil, errors.New("browser process-group RSS sampling requires Unix")
}
