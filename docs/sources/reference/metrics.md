# Metrics reference

The cloud-vinyl operator exposes Prometheus metrics on port `8080` at `/metrics`.

(metrics-operator)=

## Metrics

These `vinyl_*` metrics describe the **operator domain** (reconcile, VCL push, invalidation).
They are always recorded.
The operator aggregates duration histograms over all caches.
They carry no labels.

The cache's namespace is in `cache_namespace` and the broadcast target in `pod_addr`.
Prometheus attaches the operator pod's own `namespace` and `pod` as target labels.
Unless the scrape honors labels, it renames colliding scraped labels to `exported_namespace` and `exported_pod`.
Before 1.1 that happened to the operator's labels.
See the {ref}`1.1 upgrade notes <upgrade-1-1-metric-labels>`.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `vinyl_reconcile_total` | Counter | `cache`, `cache_namespace`, `result` | Total reconcile loop invocations. `result` is `success` or `error`. |
| `vinyl_reconcile_duration_seconds` | Histogram | — | Reconcile loop duration in seconds. |
| `vinyl_vcl_push_total` | Counter | `cache`, `cache_namespace`, `result` | VCL push attempts, counted per peer. `result` is `success` or `error`. |
| `vinyl_vcl_push_duration_seconds` | Histogram | — | VCL push duration in seconds (whole push operation). |
| `vinyl_invalidation_total` | Counter | `cache`, `cache_namespace`, `type`, `result` | Invalidation requests. `type` is `purge`, `ban`, or `xkey`. `result` is `success` or `error`. |
| `vinyl_invalidation_duration_seconds` | Histogram | — | Invalidation operation duration in seconds. |
| `vinyl_broadcast_total` | Counter | `cache`, `cache_namespace`, `pod_addr`, `result` | Broadcast requests to individual cache pods. `pod_addr` is the pod's address (`ip:port`) the request went to. `result` is `success` or `error`. |
| `vinyl_partial_failure_total` | Counter | `cache`, `cache_namespace` | Broadcasts where some but not all pods failed. |
| `vinyl_objects_purged_total` | Counter | `cache`, `cache_namespace`, `type` | Objects actually removed by invalidation requests, as reported by the cache's `X-Vinyl-Purged` response header. It advances only on a *known* count. A broadcast where no pod reported a parseable count adds nothing instead of a fabricated 0. `type` is `purge` or `xkey` only. The operator sends `ban` to the agent's `POST /ban`, never to `varnishd`'s purge synth. A ban never sets this header and never appears here. A healthy cluster that issues purges but never advances this counter is the signal #103 exists to surface. 200 OK and zero removed are different things. Zero is often legitimate (see below), but *never knowing* the count is not. |
| `vinyl_objects_purged_unknown_total` | Counter | `cache`, `cache_namespace`, `type` | Individual pod purge responses that answered 2xx but did not carry a parseable `X-Vinyl-Purged` count: a pod that *didn't say*, as opposed to one that reported a known 0. `type` is `purge` or `xkey` only, same reason as above. It stays separate from `vinyl_objects_purged_total` on purpose. A regression can drop the header on only a subset of pods (a partial VCL rollout, or the broadcast-path shape behind #101). That nudges the sum of the other counter down a little and stays invisible. This counter climbs instead, on exactly the pods that stopped reporting. |
| `vinyl_invalidation_unknown_host_total` | Counter | none | Invalidation requests rejected with `404 unknown cache` because their `Host` names no registered cache, most often the bare `<cache>-invalidation` Service name without the namespace. Clients such as `plone.cachepurging` do not log failed purges. A rising counter is the signal that one of them is misconfigured. The counter has no labels on purpose. The client controls the host. |
| `vinyl_vcl_versions_loaded` | Gauge | `cache`, `cache_namespace` | Number of VCL versions currently loaded in the cache (drift indicator). |

```{note}
varnishd's own `MAIN.n_purges` and `MAIN.n_obj_purged` stay at 0 for cloud-vinyl purges.
`vmod_purge` does not count into them.
Whether purges remove anything shows only in `vinyl_objects_purged_total`.
```

(metrics-exporter)=

## Cache metrics (exporter)

Cache hit ratio and backend health come from the `prometheus_varnish_exporter` sidecar.
The operator does not export them.
Enable the sidecar with `spec.monitoring.exporter.enabled`.
It reads `varnishstat` ({term}`VSM`) and exposes native `varnish_*` metrics, which Prometheus scrapes via the generated ServiceMonitor.
Since 1.1 the default image ships a build of the maintained [otto-de fork](https://github.com/otto-de/prometheus_varnish_exporter).
The original exporter has had no release since 2021.
The metric names are the same.
The most relevant ones:

| Metric | Type | Description |
|--------|------|-------------|
| `varnish_main_cache_hit` | Counter | Cumulative cache hits (`MAIN.cache_hit`). |
| `varnish_main_cache_miss` | Counter | Cumulative cache misses (`MAIN.cache_miss`). |
| `varnish_backend_happy` | Gauge | Raw probe-history bitmap (`VBE.*.happy`), not a boolean. Each probe shifts one bit in, so a consistently failing backend only reaches `0` after the full 64-bit window has drained (~64 probe intervals). A backend declared without a `.probe` never produces a meaningful value. Label: `backend`. |

Compute the hit ratio in PromQL/Grafana rather than relying on an operator gauge:

```promql
sum(rate(varnish_main_cache_hit[5m])) by (namespace)
  / (sum(rate(varnish_main_cache_hit[5m])) by (namespace)
     + sum(rate(varnish_main_cache_miss[5m])) by (namespace))
```

(metrics-alerts)=

## Alerts

The same ten alerts come from two places:

- The chart, with `monitoring.prometheusRules.enabled: true`.
  It creates one `PrometheusRule` in the operator namespace that covers every cache in the cluster.
- The operator, with `spec.monitoring.prometheusRules.enabled: true` on a VinylCache.
  It creates a `PrometheusRule` named `<cache>-alerts` next to the cache that covers only that cache.
  `VinylCacheOperatorDown` watches the operator as a whole and is not filtered.
  Before 1.0.2 the operator named every cache's rule `cloud-vinyl-alerts`.
  The operator deletes that rule.

Enable one of the two per cache, or the alerts fire twice.
See {ref}`tune-hit-ratio-alert`.

The exporter's metrics carry no cache name.
`VinylCacheLowHitRatio` and `VinylCacheBackendUnhealthy` derive a `cache` label from the pod name.
The alerts name the cache.
Alertmanager can route on it.
Prometheus evaluates the hit ratio per pod, as each pod holds its own shard.

| Alert | Severity | Condition |
|-------|----------|-----------|
| `VinylCacheVCLSyncFailed` | warning | VCL push error rate > 0 for 5 m |
| `VinylCachePartialVCLSync` | warning | Partial failure counter > 0 for 2 m |
| `VinylCacheAllPodsUnreachable` | critical | Push errors and reconcile errors both > 0 for 1 m |
| `VinylCacheBackendUnhealthy` | warning | `varnish_backend_happy == 0` for 5 m |
| `VinylCacheLowHitRatio` | warning | exporter-derived hit ratio of a pod < 50% for 15 m by default. The chart takes threshold and duration from `monitoring.prometheusRules.lowHitRatio`. The operator's per-cache rule takes them from `spec.monitoring.prometheusRules.lowHitRatio`. |
| `VinylCacheHighInvalidationRate` | warning | >100 invalidations/s for 5 m |
| `VinylCacheReconcileErrors` | warning | Reconcile error rate > 0 for 5 m |
| `VinylCacheOperatorDown` | critical | No reconcile metrics present for 5 m |
| `VinylCacheVCLDrift` | warning | >2 VCL versions loaded for 10 m |
| `VinylCacheBroadcastFailures` | warning | > 0.1 failed broadcast requests per second for a cache, for 5 m |
