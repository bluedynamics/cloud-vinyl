package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeStub creates an executable that cats a real fixture once, then exits,
// simulating varnishlog dying (varnishd restart).
func writeStub(t *testing.T) string {
	t.Helper()
	fixture, err := filepath.Abs("../../internal/tracer/vsl/testdata/miss_then_hit.txt")
	require.NoError(t, err)
	dir := t.TempDir()
	stub := filepath.Join(dir, "varnishlog-stub")
	script := "#!/bin/sh\ncat \"" + fixture + "\"\nexit 1\n"
	require.NoError(t, os.WriteFile(stub, []byte(script), 0o755))
	return stub
}

func TestSupervise_ParsesOutputAndRestartsOnExit(t *testing.T) {
	stub := writeStub(t)
	var groups int
	restarts := make(chan struct{}, 8)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s := &supervisor{
		binary:  stub,
		backoff: 10 * time.Millisecond,
		onGroup: func() { groups++ },
		onRestart: func() {
			restarts <- struct{}{}
			if len(restarts) >= 2 {
				cancel() // saw at least two spawns: restart works
			}
		},
	}
	s.run(ctx)
	assert.GreaterOrEqual(t, groups, 2, "fixture has two request groups")
}
