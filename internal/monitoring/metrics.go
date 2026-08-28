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
	// labelType partitions the invalidation counters by request kind
	// (purge, ban, xkey — though ObjectsPurgedTotal/ObjectsPurgedUnknownTotal
	// never see "ban"; see their doc comments below).
	labelType = "type"
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
	//
	// type is purge or xkey only. ban is never a value here: BAN is routed
	// to the agent's POST /ban (see internal/proxy/handler.go's handleBAN),
	// not varnishd's purge synth, and never sets X-Vinyl-Purged.
	ObjectsPurgedTotal *prometheus.CounterVec // labels: cache, namespace, type (purge|xkey)
	// ObjectsPurgedUnknownTotal counts individual pod responses that
	// answered 2xx (the purge call itself succeeded) but did not carry a
	// parseable X-Vinyl-Purged header — a pod that "did not say", as
	// distinct from one that reported a known 0. Deliberately a separate
	// counter from ObjectsPurgedTotal rather than folding into it: a
	// regression that drops the header on only a subset of pods (a partial
	// VCL rollout, or the broadcast-path shape #101 was) only nudges the sum
	// down a little and would otherwise be invisible in ObjectsPurgedTotal
	// alone; this counter climbs instead, on exactly the pods that stopped
	// saying. Also purge/xkey only — ban never carries this header by
	// design, not by regression, so counting it here would be permanent
	// noise rather than a signal (see objectsPurgedCapable in
	// internal/proxy/handler.go).
	ObjectsPurgedUnknownTotal *prometheus.CounterVec // labels: cache, namespace, type (purge|xkey)

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
	}, []string{labelCache, labelNamespace, labelType, labelResult})
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
		Help: "Total number of cache objects actually removed by invalidation requests, as reported by Varnish. type is purge or xkey; ban never sets this.",
	}, []string{labelCache, labelNamespace, labelType})
	reg.MustRegister(m.ObjectsPurgedTotal)

	m.ObjectsPurgedUnknownTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "vinyl_objects_purged_unknown_total",
		Help: "Total number of individual pod purge responses that answered 2xx but did not carry a parseable X-Vinyl-Purged count. type is purge or xkey; ban never sets this header by design.",
	}, []string{labelCache, labelNamespace, labelType})
	reg.MustRegister(m.ObjectsPurgedUnknownTotal)

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
