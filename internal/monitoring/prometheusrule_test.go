package monitoring_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bluedynamics/cloud-vinyl/internal/monitoring"
)

func TestGeneratePrometheusRule_Contains10Alerts(t *testing.T) {
	rule := monitoring.GeneratePrometheusRule("cloud-vinyl")
	require.Len(t, rule.Spec.Groups, 1)
	assert.Len(t, rule.Spec.Groups[0].Rules, 10)
}

func TestGeneratePrometheusRule_Namespace(t *testing.T) {
	rule := monitoring.GeneratePrometheusRule("my-namespace")
	assert.Equal(t, "my-namespace", rule.Namespace)
	assert.Equal(t, "cloud-vinyl-alerts", rule.Name)
}

func TestGeneratePrometheusRule_AlertNames(t *testing.T) {
	rule := monitoring.GeneratePrometheusRule("cloud-vinyl")
	alertNames := make(map[string]bool)
	for _, r := range rule.Spec.Groups[0].Rules {
		alertNames[r.Alert] = true
	}

	// All 10 alerts from architektur.md §8.5 must be present
	expected := []string{
		"VinylCacheVCLSyncFailed",
		"VinylCachePartialVCLSync",
		"VinylCacheAllPodsUnreachable",
		"VinylCacheBackendUnhealthy",
		"VinylCacheLowHitRatio",
		"VinylCacheHighInvalidationRate",
		"VinylCacheReconcileErrors",
		"VinylCacheOperatorDown",
		"VinylCacheVCLDrift",
		"VinylCacheBroadcastFailures",
	}
	for _, name := range expected {
		assert.True(t, alertNames[name], "missing alert: %s", name)
	}
}

func TestGeneratePrometheusRule_Severities(t *testing.T) {
	rule := monitoring.GeneratePrometheusRule("cloud-vinyl")
	for _, r := range rule.Spec.Groups[0].Rules {
		severity, ok := r.Labels["severity"]
		assert.True(t, ok, "alert %s has no severity label", r.Alert)
		assert.Contains(t, []string{"critical", "warning"}, severity,
			"alert %s has invalid severity %s", r.Alert, severity)
	}
}

func TestGeneratePrometheusRule_AllAlertsHaveSummary(t *testing.T) {
	rule := monitoring.GeneratePrometheusRule("cloud-vinyl")
	for _, r := range rule.Spec.Groups[0].Rules {
		_, ok := r.Annotations["summary"]
		assert.True(t, ok, "alert %s has no summary annotation", r.Alert)
	}
}

func TestGeneratePrometheusRule_VCLSyncFailed_UsesCorrectMetric(t *testing.T) {
	rule := monitoring.GeneratePrometheusRule("cloud-vinyl")
	var found bool
	for _, r := range rule.Spec.Groups[0].Rules {
		if r.Alert == "VinylCacheVCLSyncFailed" {
			assert.Contains(t, r.Expr.String(), "vinyl_vcl_push_total")
			found = true
		}
	}
	assert.True(t, found, "VinylCacheVCLSyncFailed alert not found")
}

func findAlertExpr(rule *monitoring.PrometheusRule, name string) (string, bool) {
	for _, r := range rule.Spec.Groups[0].Rules {
		if r.Alert == name {
			return r.Expr.String(), true
		}
	}
	return "", false
}

func TestPrometheusRule_HitRatioUsesExporterMetric(t *testing.T) {
	rule := monitoring.GeneratePrometheusRule("cloud-vinyl")
	expr, ok := findAlertExpr(rule, "VinylCacheLowHitRatio")
	require.True(t, ok)
	assert.Contains(t, expr, "varnish_main_cache_hit")
	assert.NotContains(t, expr, "vinyl_cache_hit_ratio")
}

func TestPrometheusRule_BackendHealthUsesExporterMetric(t *testing.T) {
	rule := monitoring.GeneratePrometheusRule("cloud-vinyl")
	expr, ok := findAlertExpr(rule, "VinylCacheBackendUnhealthy")
	require.True(t, ok)
	assert.Contains(t, expr, "varnish_backend_happy")
	assert.NotContains(t, expr, "vinyl_backend_health")
}
