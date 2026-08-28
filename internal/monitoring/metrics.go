package monitoring

import "github.com/prometheus/client_golang/prometheus"

const (
	// labelCache is the Prometheus label carrying the VinylCache name. Every
	// metric in this package is partitioned by it.
	labelCache = "cache"
	// labelNamespace is the namespace of that VinylCache.
	labelNamespace = "namespace"
	// labelResult partitions the operation counters into success and error.
	labelResult = "result"
)

// Metrics holds all Prometheus metrics for cloud-vinyl.
// Pass a prometheus.Registerer to NewMetrics — never use the global registry.
// All fields are safe to use after NewMetrics returns.
// Nil-safe pattern for callers: if m != nil { m.VCLPushTotal.WithLabelValues(...).Inc() }
type Metrics struct {
	// VCL push metrics
	VCLPushTotal    *prometheus.CounterVec // labels: cache, namespace, result (success|error)
	VCLPushDuration prometheus.Histogram   // aggregated over all caches

	// Invalidation metrics
	InvalidationTotal    *prometheus.CounterVec // labels: cache, namespace, type (purge|ban|xkey), result
	InvalidationDuration prometheus.Histogram
	BroadcastTotal       *prometheus.CounterVec // labels: pod, result (success|error)
	PartialFailureTotal  *prometheus.CounterVec // labels: cache, namespace
	// ObjectsPurgedTotal is the cumulative count of objects Varnish actually
	// removed, summed from the X-Vinyl-Purged header each pod's purge synth
	// response carries (see internal/proxy/broadcast.go's aggregateObjectsPurged).
	// Only advances on a known count — a broadcast where every pod's count is
	// unknown adds nothing, rather than adding 0 and masking a broken
	// signal. This is the graphable answer to #103: a purge that removes
	// nothing is legitimate (see InvalidationTotal for pass/fail), but this
	// total sitting at zero while purges are being issued is not.
	ObjectsPurgedTotal *prometheus.CounterVec // labels: cache, namespace, type (purge|ban|xkey)

	// Cache state.
	// Note: hit-ratio and backend-health are NOT operator-side gauges — they come
	// from the prometheus_varnish_exporter sidecar (varnish_main_cache_hit/miss,
	// backend health) and are computed in PromQL/Grafana.
	VCLVersionsLoaded *prometheus.GaugeVec // labels: cache, namespace

	// Operator
	ReconcileTotal    *prometheus.CounterVec // labels: cache, namespace, result
	ReconcileDuration prometheus.Histogram
}

// NewMetrics creates and registers all metrics with the given registerer.
// Use prometheus.NewRegistry() for tests, prometheus.DefaultRegisterer for production.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{}

	m.VCLPushTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "vinyl_vcl_push_total",
		Help: "Total number of VCL push attempts.",
	}, []string{labelCache, labelNamespace, labelResult})
	reg.MustRegister(m.VCLPushTotal)

	m.VCLPushDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "vinyl_vcl_push_duration_seconds",
		Help:    "Duration of VCL push operations in seconds.",
		Buckets: prometheus.DefBuckets,
	})
	reg.MustRegister(m.VCLPushDuration)

	m.InvalidationTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "vinyl_invalidation_total",
		Help: "Total number of cache invalidation requests.",
	}, []string{labelCache, labelNamespace, "type", labelResult})
	reg.MustRegister(m.InvalidationTotal)

	m.InvalidationDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "vinyl_invalidation_duration_seconds",
		Help:    "Duration of cache invalidation operations in seconds.",
		Buckets: prometheus.DefBuckets,
	})
	reg.MustRegister(m.InvalidationDuration)

	m.BroadcastTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "vinyl_broadcast_total",
		Help: "Total number of broadcast requests to individual Varnish pods.",
	}, []string{"pod", labelResult})
	reg.MustRegister(m.BroadcastTotal)

	m.PartialFailureTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "vinyl_partial_failure_total",
		Help: "Total number of partial broadcast failures (some pods unreachable).",
	}, []string{labelCache, labelNamespace})
	reg.MustRegister(m.PartialFailureTotal)

	m.ObjectsPurgedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "vinyl_objects_purged_total",
		Help: "Total number of cache objects actually removed by invalidation requests, as reported by Varnish.",
	}, []string{labelCache, labelNamespace, "type"})
	reg.MustRegister(m.ObjectsPurgedTotal)

	m.VCLVersionsLoaded = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "vinyl_vcl_versions_loaded",
		Help: "Number of VCL versions currently loaded in Varnish.",
	}, []string{labelCache, labelNamespace})
	reg.MustRegister(m.VCLVersionsLoaded)

	m.ReconcileTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "vinyl_reconcile_total",
		Help: "Total number of reconcile operations.",
	}, []string{labelCache, labelNamespace, labelResult})
	reg.MustRegister(m.ReconcileTotal)

	m.ReconcileDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "vinyl_reconcile_duration_seconds",
		Help:    "Duration of reconcile operations in seconds.",
		Buckets: prometheus.DefBuckets,
	})
	reg.MustRegister(m.ReconcileDuration)

	return m
}
