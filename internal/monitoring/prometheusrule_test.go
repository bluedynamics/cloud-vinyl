package monitoring_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bluedynamics/cloud-vinyl/internal/monitoring"
)

func TestGeneratePrometheusRule_Contains10Alerts(t *testing.T) {
	rule := monitoring.GeneratePrometheusRule("my-cache", "cloud-vinyl", monitoring.Options{})
	require.Len(t, rule.Spec.Groups, 1)
	assert.Len(t, rule.Spec.Groups[0].Rules, 10)
}

func TestGeneratePrometheusRule_Namespace(t *testing.T) {
	rule := monitoring.GeneratePrometheusRule("my-cache", "my-namespace", monitoring.Options{})
	assert.Equal(t, "my-namespace", rule.Namespace)
	assert.Equal(t, "my-cache-alerts", rule.Name)
}

func TestGeneratePrometheusRule_AlertNames(t *testing.T) {
	rule := monitoring.GeneratePrometheusRule("my-cache", "cloud-vinyl", monitoring.Options{})
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
	rule := monitoring.GeneratePrometheusRule("my-cache", "cloud-vinyl", monitoring.Options{})
	for _, r := range rule.Spec.Groups[0].Rules {
		severity, ok := r.Labels["severity"]
		assert.True(t, ok, "alert %s has no severity label", r.Alert)
		assert.Contains(t, []string{"critical", "warning"}, severity,
			"alert %s has invalid severity %s", r.Alert, severity)
	}
}

func TestGeneratePrometheusRule_AllAlertsHaveSummary(t *testing.T) {
	rule := monitoring.GeneratePrometheusRule("my-cache", "cloud-vinyl", monitoring.Options{})
	for _, r := range rule.Spec.Groups[0].Rules {
		_, ok := r.Annotations["summary"]
		assert.True(t, ok, "alert %s has no summary annotation", r.Alert)
	}
}

func TestGeneratePrometheusRule_VCLSyncFailed_UsesCorrectMetric(t *testing.T) {
	rule := monitoring.GeneratePrometheusRule("my-cache", "cloud-vinyl", monitoring.Options{})
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
	rule := monitoring.GeneratePrometheusRule("my-cache", "cloud-vinyl", monitoring.Options{})
	expr, ok := findAlertExpr(rule, "VinylCacheLowHitRatio")
	require.True(t, ok)
	assert.Contains(t, expr, "varnish_main_cache_hit")
	assert.NotContains(t, expr, "vinyl_cache_hit_ratio")
}

func TestPrometheusRule_BackendHealthUsesExporterMetric(t *testing.T) {
	rule := monitoring.GeneratePrometheusRule("my-cache", "cloud-vinyl", monitoring.Options{})
	expr, ok := findAlertExpr(rule, "VinylCacheBackendUnhealthy")
	require.True(t, ok)
	assert.Contains(t, expr, "varnish_backend_happy")
	assert.NotContains(t, expr, "vinyl_backend_health")
}

// Every cache-specific alert must select only the cache's own series, or a
// per-cache rule alerts on every cache in the cluster (#143).
func TestGeneratePrometheusRule_SelectsOnlyTheCache(t *testing.T) {
	rule := monitoring.GeneratePrometheusRule("my-cache", "shop", monitoring.Options{})
	global := map[string]bool{"VinylCacheOperatorDown": true}
	exporter := map[string]bool{"VinylCacheLowHitRatio": true, "VinylCacheBackendUnhealthy": true}
	for _, r := range rule.Spec.Groups[0].Rules {
		expr := r.Expr.String()
		switch {
		case global[r.Alert]:
			assert.NotContains(t, expr, "my-cache", r.Alert)
		case exporter[r.Alert]:
			assert.Contains(t, expr, `namespace="shop",pod=~"my-cache-[0-9]+"`, r.Alert)
		default:
			// Both labels: a cache name alone matches same-named caches
			// in other namespaces (#155).
			assert.Contains(t, expr, `cache="my-cache",cache_namespace="shop"`, r.Alert)
		}
	}
}

func TestGeneratePrometheusRule_AllPodsUnreachableFiltersBothSides(t *testing.T) {
	expr, ok := findAlertExpr(monitoring.GeneratePrometheusRule("my-cache", "shop", monitoring.Options{}), "VinylCacheAllPodsUnreachable")
	require.True(t, ok)
	assert.Equal(t,
		`rate(vinyl_vcl_push_total{result="error",cache="my-cache",cache_namespace="shop"}[5m]) > 0 and vinyl_reconcile_total{result="error",cache="my-cache",cache_namespace="shop"} > 0`,
		expr)
}

// The exporter knows nothing about VinylCaches, so the alerts derive the cache
// label from the StatefulSet pod name; the annotations rely on it (#143).
func TestGeneratePrometheusRule_ExporterAlertsCarryCacheLabel(t *testing.T) {
	rule := monitoring.GeneratePrometheusRule("my-cache", "shop", monitoring.Options{})
	for _, name := range []string{"VinylCacheLowHitRatio", "VinylCacheBackendUnhealthy"} {
		expr, ok := findAlertExpr(rule, name)
		require.True(t, ok)
		assert.Contains(t, expr, `label_replace(`, name)
		assert.Contains(t, expr, `"cache", "$1", "pod", "(.+)-[0-9]+")`, name)
	}
}

func TestGeneratePrometheusRule_LowHitRatioExpr(t *testing.T) {
	expr, ok := findAlertExpr(monitoring.GeneratePrometheusRule("my-cache", "shop", monitoring.Options{}), "VinylCacheLowHitRatio")
	require.True(t, ok)
	sel := `{namespace="shop",pod=~"my-cache-[0-9]+"}`
	hit := `sum by (namespace, pod) (rate(varnish_main_cache_hit` + sel + `[5m]))`
	miss := `sum by (namespace, pod) (rate(varnish_main_cache_miss` + sel + `[5m]))`
	assert.Equal(t,
		`label_replace(`+hit+` / (`+hit+` + `+miss+`), "cache", "$1", "pod", "(.+)-[0-9]+") < 0.5`,
		expr)
}

func TestGeneratePrometheusRule_QuotesDotsInCacheName(t *testing.T) {
	expr, ok := findAlertExpr(monitoring.GeneratePrometheusRule("www.example", "shop", monitoring.Options{}), "VinylCacheBackendUnhealthy")
	require.True(t, ok)
	assert.Contains(t, expr, `pod=~"www\\.example-[0-9]+"`)
}

func findAlert(t *testing.T, rule *monitoring.PrometheusRule, name string) monitoring.Rule {
	t.Helper()
	for _, r := range rule.Spec.Groups[0].Rules {
		if r.Alert == name {
			return r
		}
	}
	require.Failf(t, "alert not found", "%s", name)
	return monitoring.Rule{}
}

func TestGeneratePrometheusRule_LowHitRatioDefaults(t *testing.T) {
	a := findAlert(t, monitoring.GeneratePrometheusRule("my-cache", "shop", monitoring.Options{}), "VinylCacheLowHitRatio")
	assert.Equal(t, "15m", a.For)
	assert.Contains(t, a.Expr.String(), "< 0.5")
	assert.Contains(t, a.Annotations["description"], "below 50% for 15m")
}

func TestGeneratePrometheusRule_LowHitRatioCustom(t *testing.T) {
	opts := monitoring.Options{LowHitRatioThreshold: 0.07, LowHitRatioFor: "1h30m"}
	a := findAlert(t, monitoring.GeneratePrometheusRule("my-cache", "shop", opts), "VinylCacheLowHitRatio")
	assert.Equal(t, "1h30m", a.For)
	assert.True(t, strings.HasSuffix(a.Expr.String(), "< 0.07"), a.Expr.String())
	assert.Contains(t, a.Annotations["description"], "below 7% for 1h30m")
}

func TestGeneratePrometheusRule_LowHitRatioOnlyAffectsThatAlert(t *testing.T) {
	def := monitoring.GeneratePrometheusRule("my-cache", "shop", monitoring.Options{})
	custom := monitoring.GeneratePrometheusRule("my-cache", "shop", monitoring.Options{LowHitRatioThreshold: 0.2, LowHitRatioFor: "1h"})
	for i, r := range def.Spec.Groups[0].Rules {
		if r.Alert == "VinylCacheLowHitRatio" {
			continue
		}
		assert.Equal(t, r, custom.Spec.Groups[0].Rules[i], r.Alert)
	}
}

// The operator's metrics name their cache's namespace cache_namespace, not
// namespace: Prometheus renames a scraped namespace label that collides with
// the operator pod's target label to exported_namespace (#155). No alert on
// operator metrics may rely on namespace.
func TestGeneratePrometheusRule_OperatorAlertsUseCacheNamespace(t *testing.T) {
	rule := monitoring.GeneratePrometheusRule("my-cache", "shop", monitoring.Options{})
	exporter := map[string]bool{"VinylCacheLowHitRatio": true, "VinylCacheBackendUnhealthy": true}
	for _, r := range rule.Spec.Groups[0].Rules {
		if exporter[r.Alert] {
			continue
		}
		assert.NotContains(t, r.Annotations["description"], "$labels.namespace", r.Alert)
		assert.NotRegexp(t, `(^|[{,(\s])namespace=`, r.Expr.String(), r.Alert)
	}
}

func TestGeneratePrometheusRule_BroadcastFailuresPerCache(t *testing.T) {
	expr, ok := findAlertExpr(monitoring.GeneratePrometheusRule("my-cache", "shop", monitoring.Options{}), "VinylCacheBroadcastFailures")
	require.True(t, ok)
	assert.Equal(t,
		`sum by (cache_namespace, cache) (rate(vinyl_broadcast_total{result="error",cache="my-cache",cache_namespace="shop"}[5m])) > 0.1`,
		expr)
}
