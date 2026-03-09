package proxy

import (
	"sync"

	"golang.org/x/time/rate"
)

// RateLimiter is the interface for per-key rate limiting.
type RateLimiter interface {
	Allow(key string) bool
}

// TokenBucketRateLimiter implements a per-key token-bucket rate limiter using
// golang.org/x/time/rate. Each key gets its own independent limiter.
type TokenBucketRateLimiter struct {
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
	rps      rate.Limit // requests per second
	burst    int
}

// NewTokenBucketRateLimiter creates a new rate limiter.
// rpm is requests per minute; burst is the maximum allowed burst size.
func NewTokenBucketRateLimiter(rpm, burst int) *TokenBucketRateLimiter {
	return &TokenBucketRateLimiter{
		limiters: make(map[string]*rate.Limiter),
		rps:      rate.Limit(float64(rpm) / 60.0),
		burst:    burst,
	}
}

// Allow returns true if the given key is under the rate limit.
func (r *TokenBucketRateLimiter) Allow(key string) bool {
	r.mu.Lock()
	l, ok := r.limiters[key]
	if !ok {
		l = rate.NewLimiter(r.rps, r.burst)
		r.limiters[key] = l
	}
	r.mu.Unlock()
	return l.Allow()
}

// NoopRateLimiter is a rate limiter that always allows all requests.
// Useful when rate limiting is disabled.
type NoopRateLimiter struct{}

// Allow always returns true.
func (n *NoopRateLimiter) Allow(_ string) bool { return true }
