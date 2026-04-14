# VinylCache Spec Reference

`VinylCache` is the central custom resource managed by cloud-vinyl.

**API group:** `vinyl.bluedynamics.eu/v1alpha1`
**Kind:** `VinylCache`
**Scope:** Namespaced

## Spec fields

### Top-level

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `replicas` | integer | yes | Number of Vinyl Cache pods in the StatefulSet. |
| `backends` | list | yes | One or more backend services. |
| `director` | object | no | Director configuration (defaults: `type: shard`). |
| `cluster` | object | no | Clustering / peer-routing configuration. |
| `invalidation` | object | no | Cache invalidation configuration. |
| `debounce.duration` | duration | no | Wait after last change before VCL push (default: `1s`). |
| `retry.maxAttempts` | integer | no | Maximum VCL push retry attempts (default: `3`). |
| `retry.backoffBase` | duration | no | Initial retry backoff (default: `5s`). |
| `retry.backoffMax` | duration | no | Maximum retry backoff (default: `5m`). |
| `proxyProtocol.enabled` | boolean | no | Enable PROXY protocol v2 on port 8081. |
| `proxyProtocol.port` | integer | no | PROXY protocol port (default: `8081` when enabled). |
| `service.annotations` | object | no | Annotations on the traffic Service. |
| `pod.labels` | object | no | Extra labels on Vinyl Cache pods. |

### backends

Each entry in `spec.backends` defines an upstream service:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Unique backend identifier (used in VCL). |
| `serviceRef.name` | string | yes | Kubernetes Service name in the same namespace. |
| `port` | integer | no | Overrides the Service port; defaults to the Service's first port. |
| `connectTimeout` | duration | no | Connection timeout (default: `1s`). |
| `firstByteTimeout` | duration | no | Time-to-first-byte timeout (default: `60s`). |
| `betweenBytesTimeout` | duration | no | Between-bytes timeout (default: `60s`). |
| `maxConnections` | integer | no | Maximum concurrent connections to this backend. |
| `healthCheck.url` | string | no | Health probe URL (e.g. `/healthz`). |
| `healthCheck.interval` | duration | no | Health probe interval (default: `5s`). |
| `healthCheck.timeout` | duration | no | Health probe timeout (default: `1s`). |
| `healthCheck.threshold` | integer | no | Consecutive successes required to mark healthy. |
| `healthCheck.window` | integer | no | Rolling window for health evaluation. |

### `backends[].director`

Per-backend director override. If unset, the generator emits a `shard` director with default parameters, grouping all resolved per-pod backends for this `serviceRef`.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | enum | no | One of `shard`, `round_robin`, `random`, `hash`, `fallback`. Defaults to `shard`. |
| `shard.warmup` | float | no | `0.0`–`1.0`; share of traffic sent to alternate backend to warm its cache. Default `0.1`. |
| `shard.rampup` | duration | no | Ramp-up window after adding a backend. Default `30s`. |
| `shard.by` | enum | no | `HASH` (default) or `URL`. Request-time selector passed to `<backend>.backend(by=...)`. |
| `shard.healthy` | enum | no | `CHOSEN` (default) or `ALL`. Which backends must be healthy for the director to route. |
| `hash.header` | string | no | Header used as hash key for the `hash` director. |

See the [per-backend directors how-to](../how-to/per-backend-directors.md) for when to override each type.

### director

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `type` | string | `shard` | Director type. Currently only `shard` is supported. |
| `shard.warmup` | float | `0.1` | Fraction of requests sent to alternate backend to pre-populate its cache. |
| `shard.rampup` | duration | `30s` | Traffic throttle duration for newly healthy backends. |
| `shard.by` | string | `HASH` | Shard key source (`HASH`, `URL`, `COOKIE`, `CUSTOM`). |
| `shard.healthy` | string | `CHOSEN` | Health evaluation strategy (`CHOSEN`, `IGNORE`, `ALL`). |

### cluster

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | boolean | `false` | Enable cluster peer routing between pods. |
| `peerRouting.type` | string | `shard` | Director type for peer-to-peer routing. |

### invalidation

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `purge.soft` | boolean | `true` | Use soft purge (stale-while-revalidate). |
| `xkey` | object | nil | Xkey (surrogate key) configuration. When set, `vmod_xkey` is loaded. |
| `xkey.softPurge` | boolean | `true` | Use soft purge for xkey invalidation. |

## Status fields

| Field | Type | Description |
|-------|------|-------------|
| `phase` | string | `Pending`, `Ready`, `Degraded`, or `Error`. |
| `readyReplicas` | string | `<ready>/<total>` replica count string. |
| `vclHash` | string | SHA-256 hash of the currently active VCL. |
| `conditions` | list | Standard Kubernetes conditions (`Ready`, `VCLSynced`). |

## Example

```yaml
apiVersion: vinyl.bluedynamics.eu/v1alpha1
kind: VinylCache
metadata:
  name: my-cache
  namespace: production
spec:
  replicas: 3
  backends:
    - name: api
      host: api-service.production.svc.cluster.local
      port: 8080
      healthCheck:
        url: /healthz
        interval: 5s
        threshold: 3
  director:
    type: shard
    shard:
      warmup: 0.1
      rampup: 30s
  cluster:
    enabled: true
  invalidation:
    purge:
      soft: true
    xkey:
      softPurge: true
  debounce:
    duration: 5s
  retry:
    maxAttempts: 3
    backoffBase: 5s
    backoffMax: 5m
```
