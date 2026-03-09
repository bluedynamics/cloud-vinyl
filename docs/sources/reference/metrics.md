# Metrics Reference

The cloud-vinyl operator exposes Prometheus metrics on port `8080` at `/metrics`.

## Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `vinyl_reconcile_total` | Counter | `namespace`, `cache`, `result` | Total reconcile loop invocations. `result` is `ok` or `error`. |
| `vinyl_reconcile_duration_seconds` | Histogram | `namespace`, `cache` | Reconcile loop duration in seconds. |
| `vinyl_vcl_push_total` | Counter | `namespace`, `cache`, `result` | Total VCL push attempts. `result` is `ok` or `error`. |
| `vinyl_vcl_push_duration_seconds` | Histogram | `namespace`, `cache` | VCL push duration in seconds. |
| `vinyl_backend_health` | Gauge | `namespace`, `cache`, `backend` | Backend health status. `1` = healthy, `0` = unhealthy. |
| `vinyl_cache_hit_ratio` | Gauge | `namespace`, `cache` | Cache hit ratio (0.0–1.0) aggregated across pods. |
| `vinyl_invalidation_total` | Counter | `namespace`, `cache`, `type` | Total invalidation requests. `type` is `purge`, `ban`, or `xkey`. |
| `vinyl_broadcast_total` | Counter | `namespace`, `cache`, `result` | Total broadcast requests from the proxy. |
| `vinyl_broadcast_duration_seconds` | Histogram | `namespace`, `cache` | Broadcast fanout duration in seconds. |
| `vinyl_partial_failure_total` | Counter | `namespace`, `cache` | VCL push operations where some but not all pods failed. |
| `vinyl_vcl_versions_loaded` | Gauge | `namespace`, `cache` | Number of VCL versions currently loaded in Varnish (drift indicator). |

## Alerts

When `monitoring.prometheusRules.enabled: true`, the following alerts are deployed:

| Alert | Severity | Condition |
|-------|----------|-----------|
| `VinylCacheVCLSyncFailed` | warning | VCL push error rate > 0 for 5 m |
| `VinylCachePartialVCLSync` | warning | Partial failure counter > 0 for 2 m |
| `VinylCacheAllPodsUnreachable` | critical | Push errors and reconcile errors both > 0 for 1 m |
| `VinylCacheBackendUnhealthy` | warning | Backend health == 0 for 5 m |
| `VinylCacheLowHitRatio` | warning | Hit ratio < 50% for 15 m |
| `VinylCacheHighInvalidationRate` | warning | >100 invalidations/s for 5 m |
| `VinylCacheReconcileErrors` | warning | Reconcile error rate > 0 for 5 m |
| `VinylCacheOperatorDown` | critical | No reconcile metrics present for 5 m |
| `VinylCacheVCLDrift` | warning | >2 VCL versions loaded for 10 m |
| `VinylCacheBroadcastFailures` | warning | >10% broadcast error rate for 5 m |
