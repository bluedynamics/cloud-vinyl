package monitoring

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// PrometheusRule is a minimal representation of monitoring.coreos.com/v1 PrometheusRule.
// We define our own struct to avoid the heavy prometheus-operator dependency.
// In production, convert this to the actual CRD type when applying to Kubernetes.
type PrometheusRule struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PrometheusRuleSpec `json:"spec"`
}

// PrometheusRuleSpec holds the alert rule groups.
type PrometheusRuleSpec struct {
	Groups []RuleGroup `json:"groups"`
}

// RuleGroup is a named group of alert rules.
type RuleGroup struct {
	Name  string `json:"name"`
	Rules []Rule `json:"rules"`
}

// Rule represents a single Prometheus alerting rule.
type Rule struct {
	Alert       string             `json:"alert,omitempty"`
	Expr        intstr.IntOrString `json:"expr"`
	For         string             `json:"for,omitempty"`
	Labels      map[string]string  `json:"labels,omitempty"`
	Annotations map[string]string  `json:"annotations,omitempty"`
}

// GeneratePrometheusRule generates a PrometheusRule with all 10 alerts from architektur.md §8.5.
func GeneratePrometheusRule(namespace string) *PrometheusRule {
	return &PrometheusRule{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "monitoring.coreos.com/v1",
			Kind:       "PrometheusRule",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cloud-vinyl-alerts",
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":    "cloud-vinyl",
				"app.kubernetes.io/part-of": "cloud-vinyl",
			},
		},
		Spec: PrometheusRuleSpec{
			Groups: []RuleGroup{
				{
					Name:  "cloud-vinyl",
					Rules: allAlerts(),
				},
			},
		},
	}
}

func allAlerts() []Rule {
	return []Rule{
		{
			Alert:  "VinylCacheVCLSyncFailed",
			Expr:   intstr.FromString(`rate(vinyl_vcl_push_total{result="error"}[5m]) > 0`),
			For:    "5m",
			Labels: map[string]string{"severity": "warning"},
			Annotations: map[string]string{
				"summary":     "VCL sync failed on {{ $labels.cache }}",
				"description": "VCL push errors detected on cache {{ $labels.cache }} in namespace {{ $labels.namespace }}.",
			},
		},
		{
			Alert:  "VinylCachePartialVCLSync",
			Expr:   intstr.FromString(`vinyl_partial_failure_total > 0`),
			For:    "2m",
			Labels: map[string]string{"severity": "warning"},
			Annotations: map[string]string{
				"summary":     "Partial VCL sync on {{ $labels.cache }}",
				"description": "Some pods in cache {{ $labels.cache }} did not receive the latest VCL.",
			},
		},
		{
			Alert:  "VinylCacheAllPodsUnreachable",
			Expr:   intstr.FromString(`rate(vinyl_vcl_push_total{result="error"}[5m]) > 0 and vinyl_reconcile_total{result="error"} > 0`),
			For:    "1m",
			Labels: map[string]string{"severity": "critical"},
			Annotations: map[string]string{
				"summary":     "All Varnish pods unreachable for {{ $labels.cache }}",
				"description": "Cannot reach any Varnish pod in cache {{ $labels.cache }}. Cache is not serving requests correctly.",
			},
		},
		{
			Alert:  "VinylCacheBackendUnhealthy",
			Expr:   intstr.FromString(`vinyl_backend_health == 0`),
			For:    "5m",
			Labels: map[string]string{"severity": "warning"},
			Annotations: map[string]string{
				"summary":     "Backend {{ $labels.backend }} unhealthy",
				"description": "Backend {{ $labels.backend }} for cache {{ $labels.cache }} is reporting unhealthy.",
			},
		},
		{
			Alert:  "VinylCacheLowHitRatio",
			Expr:   intstr.FromString(`vinyl_cache_hit_ratio < 0.5`),
			For:    "15m",
			Labels: map[string]string{"severity": "warning"},
			Annotations: map[string]string{
				"summary":     "Low cache hit ratio on {{ $labels.cache }}",
				"description": "Cache hit ratio for {{ $labels.cache }} has been below 50% for 15 minutes.",
			},
		},
		{
			Alert:  "VinylCacheHighInvalidationRate",
			Expr:   intstr.FromString(`rate(vinyl_invalidation_total[5m]) > 100`),
			For:    "5m",
			Labels: map[string]string{"severity": "warning"},
			Annotations: map[string]string{
				"summary":     "High invalidation rate on {{ $labels.cache }}",
				"description": "More than 100 invalidation requests per second on cache {{ $labels.cache }}.",
			},
		},
		{
			Alert:  "VinylCacheReconcileErrors",
			Expr:   intstr.FromString(`rate(vinyl_reconcile_total{result="error"}[5m]) > 0`),
			For:    "5m",
			Labels: map[string]string{"severity": "warning"},
			Annotations: map[string]string{
				"summary":     "Reconcile errors for {{ $labels.cache }}",
				"description": "The operator is experiencing reconcile errors for VinylCache {{ $labels.cache }}.",
			},
		},
		{
			Alert:  "VinylCacheOperatorDown",
			Expr:   intstr.FromString(`absent(vinyl_reconcile_total)`),
			For:    "5m",
			Labels: map[string]string{"severity": "critical"},
			Annotations: map[string]string{
				"summary":     "cloud-vinyl operator is down",
				"description": "No reconcile metrics reported. The cloud-vinyl operator may be down.",
			},
		},
		{
			Alert:  "VinylCacheVCLDrift",
			Expr:   intstr.FromString(`vinyl_vcl_versions_loaded > 2`),
			For:    "10m",
			Labels: map[string]string{"severity": "warning"},
			Annotations: map[string]string{
				"summary":     "VCL drift detected on {{ $labels.cache }}",
				"description": "More than 2 VCL versions loaded on {{ $labels.cache }}. Manual intervention may have occurred.",
			},
		},
		{
			Alert:  "VinylCacheBroadcastFailures",
			Expr:   intstr.FromString(`rate(vinyl_broadcast_total{result="error"}[5m]) > 0.1`),
			For:    "5m",
			Labels: map[string]string{"severity": "warning"},
			Annotations: map[string]string{
				"summary":     "Broadcast failures on {{ $labels.cache }} pods",
				"description": "More than 10% of broadcast requests to Varnish pods are failing for cache {{ $labels.cache }}.",
			},
		},
	}
}
