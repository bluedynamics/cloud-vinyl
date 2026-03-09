//go:build integration

package agent_test

// Integration tests against a real varnishd instance via testcontainers-go.
// Run with: go test -tags=integration ./internal/agent/...
//
// These tests are skipped in normal CI runs and require Docker.
// They test the full Varnish admin TCP protocol including:
// - SHA256 challenge-response authentication
// - VCL push and activation via vcl.inline + vcl.use
// - Active VCL detection via vcl.list
// - VCL validation via temporary vcl.inline
// - Ban expression submission
// - VCL discard
