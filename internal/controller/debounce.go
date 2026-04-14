/*
Copyright 2026. Licensed under the Apache License, Version 2.0.
*/

package controller

import (
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"
)

// debouncer tracks the last time a VinylCache saw an endpoint-change event, so
// the reconciler can coalesce bursts of EndpointSlice updates during a rollout
// into a single VCL push. It's a per-operator in-memory map; restarts lose the
// timestamps, which is fine — the next reconcile pass just runs immediately.
type debouncer struct {
	mu     sync.Mutex
	lastCh map[types.NamespacedName]time.Time
	now    func() time.Time
}

func newDebouncer() *debouncer {
	return &debouncer{
		lastCh: make(map[types.NamespacedName]time.Time),
		now:    time.Now,
	}
}

// touch records that an endpoint change happened "now" for this VinylCache.
func (d *debouncer) touch(key types.NamespacedName) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastCh[key] = d.now()
}

// remaining returns the duration the reconciler should wait before pushing,
// given a target window. Returns 0 when the window has elapsed since the last
// touch (or when no touch has been recorded at all).
func (d *debouncer) remaining(key types.NamespacedName, window time.Duration) time.Duration {
	if window <= 0 {
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	last, ok := d.lastCh[key]
	if !ok {
		return 0
	}
	elapsed := d.now().Sub(last)
	if elapsed >= window {
		delete(d.lastCh, key)
		return 0
	}
	return window - elapsed
}
