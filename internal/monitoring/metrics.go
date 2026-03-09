package monitoring

import "github.com/prometheus/client_golang/prometheus"

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

	// Cache state
	HitRatio          *prometheus.GaugeVec // labels: cache, namespace
	BackendHealth     *prometheus.GaugeVec // labels: cache, namespace, backend
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
	}, []string{"cache", "namespace", "result"})
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
	}, []string{"cache", "namespace", "type", "result"})
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
	}, []string{"pod", "result"})
	reg.MustRegister(m.BroadcastTotal)

	m.PartialFailureTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "vinyl_partial_failure_total",
		Help: "Total number of partial broadcast failures (some pods unreachable).",
	}, []string{"cache", "namespace"})
	reg.MustRegister(m.PartialFailureTotal)

	m.HitRatio = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "vinyl_cache_hit_ratio",
		Help: "Current cache hit ratio (hits / (hits + misses)).",
	}, []string{"cache", "namespace"})
	reg.MustRegister(m.HitRatio)

	m.BackendHealth = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "vinyl_backend_health",
		Help: "Backend health status (1 = healthy, 0 = unhealthy).",
	}, []string{"cache", "namespace", "backend"})
	reg.MustRegister(m.BackendHealth)

	m.VCLVersionsLoaded = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "vinyl_vcl_versions_loaded",
		Help: "Number of VCL versions currently loaded in Varnish.",
	}, []string{"cache", "namespace"})
	reg.MustRegister(m.VCLVersionsLoaded)

	m.ReconcileTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "vinyl_reconcile_total",
		Help: "Total number of reconcile operations.",
	}, []string{"cache", "namespace", "result"})
	reg.MustRegister(m.ReconcileTotal)

	m.ReconcileDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "vinyl_reconcile_duration_seconds",
		Help:    "Duration of reconcile operations in seconds.",
		Buckets: prometheus.DefBuckets,
	})
	reg.MustRegister(m.ReconcileDuration)

	return m
}
