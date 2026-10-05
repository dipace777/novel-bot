package redis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"novel-bot/internal/limits"
)

var _ limits.RateLimiter = (*Directory)(nil)

func (d *Directory) Allow(ctx context.Context, scope, identity string, count int, window time.Duration) (time.Duration, error) {
	if scope == "" || identity == "" || count <= 0 || window < time.Millisecond {
		return 0, errors.New("invalid rate limit")
	}
	ctx, cancel := bounded(ctx)
	defer cancel()
	// Do not persist raw IP addresses or arbitrary header contents in key names.
	sum := sha256.Sum256([]byte(scope + "\x00" + identity))
	retry, err := rateScript.Run(ctx, d.client, []string{d.prefix + "rate:" + hex.EncodeToString(sum[:])}, count, window.Milliseconds()).Int64()
	return time.Duration(retry) * time.Millisecond, err
}
