// Package client (ratelimit.go) — общий token-bucket лимит скорости со стороны
// клиента (9.1 ТЗ): делится между всеми обслуживающими соединениями; 0 = без лимита.
package client

import (
	"context"
	"sync"
	"time"
)

type limiter struct {
	mu     sync.Mutex
	rate   float64
	cap    float64
	tokens float64
	last   time.Time
}

func newLimiter(bytesPerSec, burst int64) *limiter {
	if bytesPerSec <= 0 {
		return nil
	}
	c := float64(burst)
	if c < float64(bytesPerSec) {
		c = float64(bytesPerSec)
	}
	return &limiter{rate: float64(bytesPerSec), cap: c, tokens: c, last: time.Now()}
}

func (l *limiter) wait(ctx context.Context, n int) error {
	if l == nil || n <= 0 {
		return nil
	}
	for {
		l.mu.Lock()
		now := time.Now()
		l.tokens += now.Sub(l.last).Seconds() * l.rate
		l.last = now
		if l.tokens > l.cap {
			l.tokens = l.cap
		}
		if l.tokens >= float64(n) {
			l.tokens -= float64(n)
			l.mu.Unlock()
			return nil
		}
		deficit := float64(n) - l.tokens
		l.mu.Unlock()
		wait := time.Duration(deficit / l.rate * float64(time.Second))
		if wait <= 0 {
			wait = time.Millisecond
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}
