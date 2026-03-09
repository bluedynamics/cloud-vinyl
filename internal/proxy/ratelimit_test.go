package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTokenBucketRateLimiter(t *testing.T) {
	t.Run("burst of N requests allowed, N+1 rejected", func(t *testing.T) {
		const burst = 5
		// Very low rate (1 request per minute) so replenishment is negligible
		// during the test.
		rl := NewTokenBucketRateLimiter(1, burst)

		allowed := 0
		for range burst + 1 {
			if rl.Allow("ns/cache") {
				allowed++
			}
		}
		// Exactly burst requests should be allowed; the (burst+1)-th is rejected.
		assert.Equal(t, burst, allowed, "expected exactly burst=%d requests to be allowed", burst)
	})

	t.Run("different keys have independent buckets", func(t *testing.T) {
		const burst = 3
		rl := NewTokenBucketRateLimiter(1, burst)

		for range burst {
			assert.True(t, rl.Allow("ns/cache-a"))
		}
		// cache-a bucket exhausted, but cache-b is fresh.
		assert.False(t, rl.Allow("ns/cache-a"))
		assert.True(t, rl.Allow("ns/cache-b"))
	})

	t.Run("NoopRateLimiter always allows", func(t *testing.T) {
		var rl NoopRateLimiter
		for range 1000 {
			assert.True(t, rl.Allow("any-key"))
		}
	})
}
