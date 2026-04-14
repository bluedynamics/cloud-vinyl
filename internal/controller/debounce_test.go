package controller

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/types"
)

func TestDebouncer_FirstCallReadyImmediately(t *testing.T) {
	d := newDebouncer()
	key := types.NamespacedName{Name: "a", Namespace: "ns"}
	assert.Equal(t, time.Duration(0), d.remaining(key, 500*time.Millisecond))
}

func TestDebouncer_TouchThenReadAfterWindow(t *testing.T) {
	d := newDebouncer()
	key := types.NamespacedName{Name: "a", Namespace: "ns"}
	d.touch(key)
	assert.Greater(t, d.remaining(key, 500*time.Millisecond), time.Duration(0))
	time.Sleep(550 * time.Millisecond)
	assert.Equal(t, time.Duration(0), d.remaining(key, 500*time.Millisecond))
}

func TestDebouncer_TouchExtends(t *testing.T) {
	d := newDebouncer()
	key := types.NamespacedName{Name: "a", Namespace: "ns"}
	d.touch(key)
	time.Sleep(300 * time.Millisecond)
	d.touch(key) // churn — extends window
	remaining := d.remaining(key, 500*time.Millisecond)
	assert.Greater(t, remaining, 400*time.Millisecond,
		"second touch must restart the window")
}
