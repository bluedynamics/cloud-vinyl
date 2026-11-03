package monitoring

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	// labelSeverity is the alert label Alertmanager routes on.
	labelSeverity = "severity"
	// severityWarning is the only severity these rules currently emit.
	severityWarning = "warning"
	// annotationSummary and annotationDescription are the two annotations every
	// rule in this package sets.
	annotationSummary     = "summary"
	annotationDescription = "description"
	// appName is the operator name used in the standard app.kubernetes.io
	// labels and as the rule group name.
	appName = "cloud-vinyl"
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

// LegacyPrometheusRuleName is the name every VinylCache's rule had before
// #143, when two caches in one namespace overwrote each other's rule.
const LegacyPrometheusRuleName = "cloud-vinyl-alerts"

const (
	// DefaultLowHitRatioThreshold is the default hit ratio of
	// VinylCacheLowHitRatio; the chart's monitoring.prometheusRules.lowHitRatio
	// uses the same value.
	DefaultLowHitRatioThreshold = 0.5
	// DefaultLowHitRatioFor is the default duration of VinylCacheLowHitRatio.
	DefaultLowHitRatioFor = "15m"
)

// Options tunes the generated rule. The zero value yields the defaults.
type Options struct {
	// LowHitRatioThreshold is the hit ratio (0 to 1) below which
	// VinylCacheLowHitRatio fires. Zero means the default.
	LowHitRatioThreshold float64
	// LowHitRatioFor is the Prometheus duration the ratio must stay below the
	// threshold. Empty means the default.
	LowHitRatioFor string
}

// lowHitRatio resolves the threshold as a PromQL number, the threshold as a
// percentage for the description, and the for duration, applying defaults.
func (o Options) lowHitRatio() (threshold, percent, duration string) {
	t := o.LowHitRatioThreshold
	if t <= 0 {
		t = DefaultLowHitRatioThreshold
	}
	duration = o.LowHitRatioFor
	if duration == "" {
		duration = DefaultLowHitRatioFor
	}
	threshold = strconv.FormatFloat(t, 'f', -1, 64)
	// Rounded, so 0.07 reads 7% and not 7.000000000000001%.
	percent = strconv.FormatFloat(math.Round(t*1e6)/1e4, 'f', -1, 64)
	return threshold, percent, duration
}

// PrometheusRuleName is the name of the rule generated for one VinylCache.
func PrometheusRuleName(cache string) string {
	return cache + "-alerts"
}

// lowHitRatioExpr returns the VinylCacheLowHitRatio expression. The ratio is
// per pod, as each pod holds its own shard; label_replace adds the cache name
// from the StatefulSet pod name, so the alert can name the cache (#143).
// podSelector is a PromQL label matcher list without braces, or "".
func lowHitRatioExpr(podSelector, threshold string) string {
	hit := fmt.Sprintf("sum by (namespace, pod) (rate(varnish_main_cache_hit%s[5m]))", braces(podSelector))
	miss := fmt.Sprintf("sum by (namespace, pod) (rate(varnish_main_cache_miss%s[5m]))", braces(podSelector))
	return fmt.Sprintf(`%s < %s`, withCacheLabel(fmt.Sprintf("%s / (%s + %s)", hit, hit, miss)), threshold)
}

// withCacheLabel derives the cache label of an exporter series from its pod
// label: the exporter knows nothing about VinylCaches.
func withCacheLabel(expr string) string {
	return fmt.Sprintf(`label_replace(%s, "cache", "$1", "pod", "(.+)-[0-9]+")`, expr)
}

// braces wraps a non-empty matcher list in braces.
func braces(matchers string) string {
	if matchers == "" {
		return ""
	}
	return "{" + matchers + "}"
}

// join combines the non-empty matcher lists into one.
func join(matchers ...string) string {
	var out []string
	for _, m := range matchers {
		if m != "" {
			out = append(out, m)
		}
	}
	return strings.Join(out, ",")
}

// GeneratePrometheusRule generates the PrometheusRule for one VinylCache: all
// ten alerts from architektur.md §8.5, each selecting only that cache's series.
// The operator's metrics carry a cache label; the exporter's are selected by
// the cache's namespace and StatefulSet pod names. VinylCacheOperatorDown and
// VinylCacheBroadcastFailures watch the operator as a whole and stay
// unfiltered; identical alerts from several caches' rules are deduplicated by
// Alertmanager. opts tunes VinylCacheLowHitRatio.
func GeneratePrometheusRule(cache, namespace string, opts Options) *PrometheusRule {
	return &PrometheusRule{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "monitoring.coreos.com/v1",
			Kind:       "PrometheusRule",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      PrometheusRuleName(cache),
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":    appName,
				"app.kubernetes.io/part-of": appName,
			},
		},
		Spec: PrometheusRuleSpec{
			Groups: []RuleGroup{
				{
					Name: appName,
					Rules: allAlerts(
						fmt.Sprintf(`cache=%q`, cache),
						fmt.Sprintf(`namespace=%q,pod=~%q`, namespace, regexp.QuoteMeta(cache)+"-[0-9]+"),
						opts,
					),
				},
			},
		},
	}
}

// allAlerts returns the alerts. cacheSel selects the operator's series of one
// cache, podSel the exporter's series of its pods.
func allAlerts(cacheSel, podSel string, opts Options) []Rule {
	threshold, percent, lowHitFor := opts.lowHitRatio()
	return []Rule{
		{
			Alert:  "VinylCacheVCLSyncFailed",
			Expr:   intstr.FromString(fmt.Sprintf(`rate(vinyl_vcl_push_total{%s}[5m]) > 0`, join(`result="error"`, cacheSel))),
			For:    "5m",
			Labels: map[string]string{labelSeverity: severityWarning},
			Annotations: map[string]string{
				annotationSummary:     "VCL sync failed on {{ $labels.cache }}",
				annotationDescription: "VCL push errors detected on cache {{ $labels.cache }} in namespace {{ $labels.namespace }}.",
			},
		},
		{
			Alert:  "VinylCachePartialVCLSync",
			Expr:   intstr.FromString(fmt.Sprintf(`vinyl_partial_failure_total%s > 0`, braces(cacheSel))),
			For:    "2m",
			Labels: map[string]string{labelSeverity: severityWarning},
			Annotations: map[string]string{
				annotationSummary:     "Partial VCL sync on {{ $labels.cache }}",
				annotationDescription: "Some pods in cache {{ $labels.cache }} did not receive the latest VCL.",
			},
		},
		{
			Alert:  "VinylCacheAllPodsUnreachable",
			Expr:   intstr.FromString(fmt.Sprintf(`rate(vinyl_vcl_push_total{%[1]s}[5m]) > 0 and vinyl_reconcile_total{%[1]s} > 0`, join(`result="error"`, cacheSel))),
			For:    "1m",
			Labels: map[string]string{labelSeverity: "critical"},
			Annotations: map[string]string{
				annotationSummary:     "All Varnish pods unreachable for {{ $labels.cache }}",
				annotationDescription: "Cannot reach any Varnish pod in cache {{ $labels.cache }}. Cache is not serving requests correctly.",
			},
		},
		{
			Alert:  "VinylCacheBackendUnhealthy",
			Expr:   intstr.FromString(withCacheLabel(fmt.Sprintf(`varnish_backend_happy%s`, braces(podSel))) + ` == 0`),
			For:    "5m",
			Labels: map[string]string{labelSeverity: severityWarning},
			Annotations: map[string]string{
				annotationSummary:     "Backend {{ $labels.backend }} unhealthy",
				annotationDescription: "Backend {{ $labels.backend }} for cache {{ $labels.cache }} is reporting unhealthy.",
			},
		},
		{
			Alert:  "VinylCacheLowHitRatio",
			Expr:   intstr.FromString(lowHitRatioExpr(podSel, threshold)),
			For:    lowHitFor,
			Labels: map[string]string{labelSeverity: severityWarning},
			Annotations: map[string]string{
				annotationSummary:     "Low cache hit ratio on {{ $labels.cache }}",
				annotationDescription: fmt.Sprintf("Cache hit ratio of pod {{ $labels.pod }} of cache {{ $labels.cache }} in namespace {{ $labels.namespace }} has been below %s%% for %s.", percent, lowHitFor),
			},
		},
		{
			Alert:  "VinylCacheHighInvalidationRate",
			Expr:   intstr.FromString(fmt.Sprintf(`rate(vinyl_invalidation_total%s[5m]) > 100`, braces(cacheSel))),
			For:    "5m",
			Labels: map[string]string{labelSeverity: severityWarning},
			Annotations: map[string]string{
				annotationSummary:     "High invalidation rate on {{ $labels.cache }}",
				annotationDescription: "More than 100 invalidation requests per second on cache {{ $labels.cache }}.",
			},
		},
		{
			Alert:  "VinylCacheReconcileErrors",
			Expr:   intstr.FromString(fmt.Sprintf(`rate(vinyl_reconcile_total{%s}[5m]) > 0`, join(`result="error"`, cacheSel))),
			For:    "5m",
			Labels: map[string]string{labelSeverity: severityWarning},
			Annotations: map[string]string{
				annotationSummary:     "Reconcile errors for {{ $labels.cache }}",
				annotationDescription: "The operator is experiencing reconcile errors for VinylCache {{ $labels.cache }}.",
			},
		},
		{
			Alert:  "VinylCacheOperatorDown",
			Expr:   intstr.FromString(`absent(vinyl_reconcile_total)`),
			For:    "5m",
			Labels: map[string]string{labelSeverity: "critical"},
			Annotations: map[string]string{
				annotationSummary:     "cloud-vinyl operator is down",
				annotationDescription: "No reconcile metrics reported. The cloud-vinyl operator may be down.",
			},
		},
		{
			Alert:  "VinylCacheVCLDrift",
			Expr:   intstr.FromString(fmt.Sprintf(`vinyl_vcl_versions_loaded%s > 2`, braces(cacheSel))),
			For:    "10m",
			Labels: map[string]string{labelSeverity: severityWarning},
			Annotations: map[string]string{
				annotationSummary:     "VCL drift detected on {{ $labels.cache }}",
				annotationDescription: "More than 2 VCL versions loaded on {{ $labels.cache }}. Manual intervention may have occurred.",
			},
		},
		{
			Alert:  "VinylCacheBroadcastFailures",
			Expr:   intstr.FromString(`rate(vinyl_broadcast_total{result="error"}[5m]) > 0.1`),
			For:    "5m",
			Labels: map[string]string{labelSeverity: severityWarning},
			Annotations: map[string]string{
				annotationSummary:     "Broadcast failures on {{ $labels.cache }} pods",
				annotationDescription: "More than 10% of broadcast requests to Varnish pods are failing for cache {{ $labels.cache }}.",
			},
		},
	}
}
