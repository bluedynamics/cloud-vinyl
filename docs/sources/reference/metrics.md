# Metrics Reference

The cloud-vinyl operator exposes Prometheus metrics on port `8080` at `/metrics`.

## Metrics

These `vinyl_*` metrics describe the **operator domain** (reconcile, VCL push,
invalidation). They are always recorded. Duration histograms are aggregated over
all caches and therefore carry no labels.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `vinyl_reconcile_total` | Counter | `cache`, `namespace`, `result` | Total reconcile loop invocations. `result` is `success` or `error`. |
| `vinyl_reconcile_duration_seconds` | Histogram | — | Reconcile loop duration in seconds. |
| `vinyl_vcl_push_total` | Counter | `cache`, `namespace`, `result` | VCL push attempts, counted per peer. `result` is `success` or `error`. |
| `vinyl_vcl_push_duration_seconds` | Histogram | — | VCL push duration in seconds (whole push operation). |
| `vinyl_invalidation_total` | Counter | `cache`, `namespace`, `type`, `result` | Invalidation requests. `type` is `purge`, `ban`, or `xkey`; `result` is `success` or `error`. |
| `vinyl_invalidation_duration_seconds` | Histogram | — | Invalidation operation duration in seconds. |
| `vinyl_broadcast_total` | Counter | `pod`, `result` | Broadcast requests to individual Varnish pods. `result` is `success` or `error`. |
| `vinyl_partial_failure_total` | Counter | `cache`, `namespace` | Broadcasts where some but not all pods failed. |
| `vinyl_vcl_versions_loaded` | Gauge | `cache`, `namespace` | Number of VCL versions currently loaded in Varnish (drift indicator). |

## Varnish cache metrics (exporter)

Cache hit ratio and backend health are **not** operator-side metrics. They come
from the `prometheus_varnish_exporter` sidecar (enabled via
`spec.monitoring.exporter.enabled`), which reads `varnishstat` (VSM) and exposes
native `varnish_*` metrics scraped via the generated ServiceMonitor. The most
relevant ones:

| Metric | Type | Description |
|--------|------|-------------|
| `varnish_main_cache_hit` | Counter | Cumulative cache hits (`MAIN.cache_hit`). |
| `varnish_main_cache_miss` | Counter | Cumulative cache misses (`MAIN.cache_miss`). |
| `varnish_backend_happy` | Gauge | Backend health-probe window (`VBE.*.happy`); `0` means the backend is sick. Label: `backend`. |

Compute the hit ratio in PromQL/Grafana rather than relying on an operator gauge:

```promql
sum(rate(varnish_main_cache_hit[5m])) by (namespace)
  / (sum(rate(varnish_main_cache_hit[5m])) by (namespace)
     + sum(rate(varnish_main_cache_miss[5m])) by (namespace))
```

## Alerts

When `monitoring.prometheusRules.enabled: true`, the following alerts are deployed:

| Alert | Severity | Condition |
|-------|----------|-----------|
| `VinylCacheVCLSyncFailed` | warning | VCL push error rate > 0 for 5 m |
| `VinylCachePartialVCLSync` | warning | Partial failure counter > 0 for 2 m |
| `VinylCacheAllPodsUnreachable` | critical | Push errors and reconcile errors both > 0 for 1 m |
| `VinylCacheBackendUnhealthy` | warning | `varnish_backend_happy == 0` for 5 m |
| `VinylCacheLowHitRatio` | warning | exporter-derived hit ratio < 50% for 15 m |
| `VinylCacheHighInvalidationRate` | warning | >100 invalidations/s for 5 m |
| `VinylCacheReconcileErrors` | warning | Reconcile error rate > 0 for 5 m |
| `VinylCacheOperatorDown` | critical | No reconcile metrics present for 5 m |
| `VinylCacheVCLDrift` | warning | >2 VCL versions loaded for 10 m |
| `VinylCacheBroadcastFailures` | warning | >10% broadcast error rate for 5 m |
