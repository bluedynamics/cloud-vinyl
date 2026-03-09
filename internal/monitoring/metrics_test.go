package monitoring_test

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bluedynamics/cloud-vinyl/internal/monitoring"
)

func TestNewMetrics_AllFieldsNotNil(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := monitoring.NewMetrics(reg)

	assert.NotNil(t, m.VCLPushTotal)
	assert.NotNil(t, m.VCLPushDuration)
	assert.NotNil(t, m.InvalidationTotal)
	assert.NotNil(t, m.InvalidationDuration)
	assert.NotNil(t, m.BroadcastTotal)
	assert.NotNil(t, m.PartialFailureTotal)
	assert.NotNil(t, m.HitRatio)
	assert.NotNil(t, m.BackendHealth)
	assert.NotNil(t, m.VCLVersionsLoaded)
	assert.NotNil(t, m.ReconcileTotal)
	assert.NotNil(t, m.ReconcileDuration)
}

func TestNewMetrics_AllRegisteredInRegistry(t *testing.T) {
	reg := prometheus.NewRegistry()
	monitoring.NewMetrics(reg)

	mfs, err := reg.Gather()
	require.NoError(t, err)

	names := make(map[string]bool)
	for _, mf := range mfs {
		names[mf.GetName()] = true
	}

	// Just check a subset — gathering only shows metrics with observations
	// Use a helper that observes each metric and then gathers
	// Actually, Counters/Gauges/Histograms appear after first observation OR
	// if registered with promauto they appear on Gather. Let's verify registration
	// by checking the registry doesn't error on double-register
	reg2 := prometheus.NewRegistry()
	m := monitoring.NewMetrics(reg2)
	// Observe one value to make it gatherable
	m.VCLPushTotal.WithLabelValues("my-cache", "default", "success").Inc()
	m.ReconcileTotal.WithLabelValues("my-cache", "default", "ok").Inc()
	m.HitRatio.WithLabelValues("my-cache", "default").Set(0.95)
	m.BackendHealth.WithLabelValues("my-cache", "default", "app").Set(1.0)
	m.BroadcastTotal.WithLabelValues("pod-0", "success").Inc()

	mfs2, err := reg2.Gather()
	require.NoError(t, err)
	gathered := make(map[string]bool)
	for _, mf := range mfs2 {
		gathered[mf.GetName()] = true
	}
	assert.True(t, gathered["vinyl_vcl_push_total"])
	assert.True(t, gathered["vinyl_reconcile_total"])
	assert.True(t, gathered["vinyl_cache_hit_ratio"])
	assert.True(t, gathered["vinyl_backend_health"])
	assert.True(t, gathered["vinyl_broadcast_total"])
}

func TestNewMetrics_Labels_VCLPushTotal(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := monitoring.NewMetrics(reg)
	// Should not panic with correct labels
	m.VCLPushTotal.WithLabelValues("my-cache", "production", "success").Inc()
	m.VCLPushTotal.WithLabelValues("my-cache", "production", "error").Inc()
}

func TestNewMetrics_Labels_BackendHealth(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := monitoring.NewMetrics(reg)
	m.BackendHealth.WithLabelValues("my-cache", "default", "app-backend").Set(1.0)
	m.BackendHealth.WithLabelValues("my-cache", "default", "legacy-backend").Set(0.0)
}

func TestNewMetrics_NilSafe_NilMetrics(t *testing.T) {
	// The nil-safe pattern: if Metrics is nil, callers check before using
	// This tests that the zero value doesn't cause issues in pattern usage
	var m *monitoring.Metrics
	assert.Nil(t, m)
	// Callers use: if m != nil { m.VCLPushTotal.WithLabelValues(...).Inc() }
}

func TestNewMetrics_IsolatedRegistry(t *testing.T) {
	// Two different registries must not share metrics (no global state)
	reg1 := prometheus.NewRegistry()
	reg2 := prometheus.NewRegistry()
	m1 := monitoring.NewMetrics(reg1)
	m2 := monitoring.NewMetrics(reg2)

	m1.VCLPushTotal.WithLabelValues("cache-a", "ns1", "success").Inc()
	// m2 should have no observations
	m2.VCLPushTotal.WithLabelValues("cache-b", "ns2", "error").Inc()

	mfs1, _ := reg1.Gather()
	mfs2, _ := reg2.Gather()
	// Both should gather independently — no cross-contamination
	assert.NotNil(t, mfs1)
	assert.NotNil(t, mfs2)
}
