package ratelimit

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

type Limiter struct {
	rdb *redis.Client
}

func New(rdb *redis.Client) *Limiter {
	return &Limiter{rdb: rdb}
}

// Allow increments the per-minute counter for the key and returns false if it
// exceeds limit. limit <= 0 means unlimited.
func (l *Limiter) Allow(ctx context.Context, keyID uuid.UUID, limit int) (allowed bool, remaining int, err error) {
	if limit <= 0 {
		return true, -1, nil
	}
	bucket := time.Now().UTC().Unix() / 60
	rk := fmt.Sprintf("rl:%s:%d", keyID.String(), bucket)
	pipe := l.rdb.TxPipeline()
	incr := pipe.Incr(ctx, rk)
	pipe.Expire(ctx, rk, 70*time.Second)
	if _, err := pipe.Exec(ctx); err != nil {
		return false, 0, err
	}
	count := int(incr.Val())
	if count > limit {
		return false, 0, nil
	}
	return true, limit - count, nil
}
