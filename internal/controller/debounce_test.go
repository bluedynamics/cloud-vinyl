package controller

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/types"
)

// fixedClock returns a `now` function that reads from the given pointer, so
// tests can advance the clock deterministically without real sleeps.
func fixedClock(t *time.Time) func() time.Time {
	return func() time.Time { return *t }
}

func TestDebouncer_FirstCallReadyImmediately(t *testing.T) {
	d := newDebouncer()
	key := types.NamespacedName{Name: "a", Namespace: "ns"}
	assert.Equal(t, time.Duration(0), d.remaining(key, 500*time.Millisecond))
}

func TestDebouncer_TouchThenReadAfterWindow(t *testing.T) {
	d := newDebouncer()
	clock := time.Unix(0, 0)
	d.now = fixedClock(&clock)

	key := types.NamespacedName{Name: "a", Namespace: "ns"}
	d.touch(key)

	// Mid-window: still waiting.
	clock = clock.Add(200 * time.Millisecond)
	assert.Greater(t, d.remaining(key, 500*time.Millisecond), time.Duration(0))

	// Past the window: zero.
	clock = clock.Add(400 * time.Millisecond) // total 600ms > 500ms window
	assert.Equal(t, time.Duration(0), d.remaining(key, 500*time.Millisecond))
}

func TestDebouncer_TouchExtends(t *testing.T) {
	d := newDebouncer()
	clock := time.Unix(0, 0)
	d.now = fixedClock(&clock)

	key := types.NamespacedName{Name: "a", Namespace: "ns"}
	d.touch(key)
	clock = clock.Add(300 * time.Millisecond)
	d.touch(key) // churn — extends window

	remaining := d.remaining(key, 500*time.Millisecond)
	assert.Equal(t, 500*time.Millisecond, remaining,
		"second touch must restart the window from the new 'now'")
}
