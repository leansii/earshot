// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"sync"
	"time"
)

// limiter is a small per-key token-bucket rate limiter. Each API key gets its
// own bucket, so one noisy app can't exhaust another tenant's budget.
type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	now     func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
	rpm    int
}

func newLimiter() *limiter {
	return &limiter{buckets: map[string]*bucket{}, now: time.Now}
}

// Allow reports whether one request is permitted for key under ratePerMin. A
// non-positive ratePerMin means unlimited.
func (l *limiter) Allow(key string, ratePerMin int) bool {
	if ratePerMin <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		// Start full (burst == one minute's worth), then spend one.
		l.buckets[key] = &bucket{tokens: float64(ratePerMin) - 1, last: now, rpm: ratePerMin}
		return true
	}

	// Refill based on elapsed time.
	refill := now.Sub(b.last).Seconds() * (float64(ratePerMin) / 60.0)
	b.tokens += refill
	if max := float64(ratePerMin); b.tokens > max {
		b.tokens = max
	}
	b.last = now
	b.rpm = ratePerMin

	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}
