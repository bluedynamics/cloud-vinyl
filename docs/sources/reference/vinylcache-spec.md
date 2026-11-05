# VinylCache Spec Reference

`VinylCache` is the central custom resource managed by cloud-vinyl.

**API group:** `vinyl.bluedynamics.eu/v1`
**Kind:** `VinylCache`
**Scope:** Namespaced

`vinyl.bluedynamics.eu/v1alpha1` is still served throughout the 1.x series, with the
same schema, and is deprecated: the API server returns a warning for every request
through it. Existing objects and manifests keep working. Move manifests to `v1` by
changing `apiVersion` only.

## Spec fields

### Top-level

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `replicas` | integer | yes | Number of Vinyl Cache pods in the StatefulSet. |
| `flavor` | string | no | Cache daemon family: `varnish` or `vinyl`. Defaults to `varnish`, so existing objects read back as `varnish`. Selects the daemon, exporter and tracer images from the runtime catalog and the binaries they use (`varnishd` or `vinyld`). See {ref}`run-vinyl-cache` and the table below. |
| `version` | string | no | Daemon version, for example `9.1.0`. Must be listed in the runtime catalog for the flavor; the admission webhook rejects any other value and names the versions it knows. Empty means the catalog's default version for the flavor, resolved at reconcile time and not written back, so it follows the chart's default on operator upgrades. Pin it to stay on a release. |
| `image` | string | no | Overrides the **daemon** image of the resolved catalog entry. The exporter and tracer still follow `flavor` and `version`. Use it for an emergency fix or an older version; see {ref}`replace-an-image`. The operator runs the flavor's daemon (`varnishd` or `vinyld`) from the image's `PATH` as the container command and passes every flag itself; the image's entrypoint does not run. The webhook warns when `image` is set without `version`, and when the repository name of the image names the other lineage. See [Varnish version support](varnish-versions.md). |
| `storage` | list | no | Cache storages, each with `name`, `type` (`malloc` or `file`), `size` and, for `file`, an optional `path`. A `file` storage without `path` gets a volume provisioned by the operator and needs a `size`; see {ref}`file-storage-provisioned`. When set, these are the only cache storages. When empty, varnishd creates its default storage `s0` (malloc, 100 MB). The name `synth` is reserved. |
| `backends` | list | yes | One or more backend services. |
| `director` | object | no | Director configuration (defaults: `type: shard`). |
| `cluster` | object | no | Clustering / peer-routing configuration. |
| `varnishParameters` | object | no | Runtime parameters passed to `varnishd` via `-p` flags. See the `varnishParameters` section below. |
| `esi` | object | no | Edge Side Includes (ESI) processing. See the `esi` section below. |
| `invalidation` | object | no | Cache invalidation configuration. |
| `debounce.duration` | duration | no | Wait after last change before VCL push (default: `1s`). |
| `retry.maxAttempts` | integer | no | Maximum VCL push retry attempts (default: `3`). |
| `retry.backoffBase` | duration | no | Initial retry backoff (default: `5s`). |
| `retry.backoffMax` | duration | no | Maximum retry backoff (default: `5m`). |
| `proxyProtocol.enabled` | boolean | no | Adds a PROXY protocol listener (v1 and v2) on `proxyProtocol.port`. Without it, varnishd has no PROXY listener. The traffic Service and the NetworkPolicies do not include this port. |
| `proxyProtocol.port` | integer | no | PROXY protocol port (default: `8081` when enabled). Must not be a port the operator binds in the pod: `8080`, `6082`, `9090`, `9131` or `9464`. |
| `service.annotations` | object | no | Annotations on the traffic Service. |
| `pod.labels` | object | no | Extra labels on Vinyl Cache pods. |
| `pod.affinity` | object | no | Pod affinity, as on a Kubernetes pod. When `podAntiAffinity` is unset, the operator fills it in from `pod.podAntiAffinity` and the operator default; `nodeAffinity` and `podAffinity` are passed through. See [Spread cache pods across nodes](../how-to/spread-pods.md). |
| `pod.podAntiAffinity.type` | string | no | `preferred`, `required` or `none`. Overrides the operator default (`preferred`) for this cache. Rejected together with `pod.affinity.podAntiAffinity`. |
| `pod.podAntiAffinity.topologyKey` | string | no | Node label to spread across. Overrides the operator default (`kubernetes.io/hostname`). |
| `pod.podAntiAffinity.weight` | integer | no | Weight of a preferred rule, 1 to 100. Overrides the operator default (`100`). |
| `pod.volumes` | list | no | Additional pod volumes appended to operator-managed defaults. Reserved names rejected by webhook. See [SSD-backed storage how-to](../how-to/ssd-backed-storage.md). |
| `pod.volumeMounts` | list | no | Additional mounts on the varnish container. Each `name` must reference a `spec.pod.volumes` entry or a `spec.volumeClaimTemplates` claim name. Reserved mount paths rejected by webhook. |
| `pod.env` | list | no | Environment variables on the varnish container (`value` or `valueFrom`). An entry named like an operator default replaces it; `MALLOC_CONF` replaces the operator's jemalloc tuning, an empty value restores jemalloc's defaults. See [Size memory for malloc storage](../how-to/size-memory.md). |
| `monitoring` | object | no | Metrics scraping and alerting. See the `monitoring` section below. |
| `volumeClaimTemplates` | list | no | StatefulSet-native per-replica PVC templates. Reference the claim `name` from `spec.pod.volumeMounts`. Immutable after creation. |

(reference-flavor)=

### Flavor

| | `varnish` | `vinyl` |
|---|---|---|
| daemon command | `varnishd` | `vinyld` |
| exporter `-varnishstat-path` | default (`varnishstat`) | `/usr/bin/vinylstat` |
| tracer `VARNISHLOG_PATH` | `varnishlog` | `vinyllog` |

`spec.monitoring.exporter.image` keeps working and wins over the exporter of the catalog.
The webhook warns when it names an exporter of the other lineage: a `vinyl` cache needs
a tag such as `1.8.3-vinyl9.1.0`, a `varnish` cache one such as `1.8.3-varnish9.1.0`.
Changing `flavor` or `version` is a rolling restart.

### backends

Each entry in `spec.backends` defines an upstream service:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Unique backend identifier (used in VCL). |
| `serviceRef.name` | string | yes | Kubernetes Service name in the same namespace. |
| `port` | integer | no | Overrides the Service port; defaults to the Service's first port. |
| `connectionParameters.connectTimeout` | duration | no | Connection timeout. Unset: varnishd's `connect_timeout` parameter. |
| `connectionParameters.firstByteTimeout` | duration | no | Time-to-first-byte timeout. Unset: varnishd's `first_byte_timeout`. |
| `connectionParameters.betweenBytesTimeout` | duration | no | Between-bytes timeout. Unset: varnishd's `between_bytes_timeout`. |
| `connectionParameters.maxConnections` | integer | no | Maximum concurrent connections to this backend. |
| `probe.url` | string | no | Health probe URL (e.g. `/healthz`). Without `probe`, the backend is not probed. |
| `probe.interval` | duration | no | Health probe interval (default: `5s`). |
| `probe.timeout` | duration | no | Health probe timeout (default: `2s`). |
| `probe.window` | integer | no | Number of recent probes evaluated (default: `5`, at most `64`). |
| `probe.threshold` | integer | no | Successful probes within the window required to mark the backend healthy (default: `3`). Must not exceed the effective window; varnishd would refuse the VCL. |
| `probe.expectedResponse` | integer | no | Expected HTTP status of the probe (default: `200`). |
| `retry.maxRetries` | integer | no | Retries of a `GET`/`HEAD` fetch on another backend when no response arrived (default: `1`, `0` disables, maximum `3`). Not allowed above `0` for `hash` and `fallback` directors. See [Per-backend directors](../how-to/per-backend-directors.md). |

How long idle backend connections are kept is varnishd's global `backend_idle_timeout`; set
it through `varnishParameters`, e.g. `{backend_idle_timeout: "50s"}`. varnishd has no
per-backend setting for it.

### `backends[].director`

Per-backend director override. If unset, the generator emits a `shard` director with default parameters, grouping all resolved per-pod backends for this `serviceRef`.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | enum | no | One of `shard`, `round_robin`, `random`, `hash`, `fallback`. Defaults to `shard`. |
| `shard.warmup` | float | no | `0.0`–`1.0`; share of traffic sent to alternate backend to warm its cache. Default `0.1`. |
| `shard.rampup` | duration | no | Ramp-up window after adding a backend. Default `30s`. |
| `shard.by` | enum | no | `URL` (default and only supported value). Request-time selector passed to `<backend>.backend(by=...)`. |
| `shard.healthy` | enum | no | `CHOSEN` (vmod default) returns a healthy backend if possible; alternatives skipped on the way are not health-checked. `ALL` also checks health while selecting an alternative. |
| `hash.header` | string | no | Request header whose value is the hash key of the `hash` director. Must be a valid HTTP header name. Without it the URL is hashed. |

The first entry of `backends` is the default route: requests that no `vclRecv` snippet routes
go through its director, using `shard.healthy` and `hash.header` as above. See
{ref}`default-routing`.

See the [per-backend directors how-to](../how-to/per-backend-directors.md) for when to override each type.

### director

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `type` | string | `shard` | Director type. Currently only `shard` is supported. |
| `shard.warmup` | float | `0.1` | Fraction of requests sent to alternate backend to pre-populate its cache. |
| `shard.rampup` | duration | `30s` | Traffic throttle duration for newly healthy backends. |
| `shard.by` | string | `URL` | Shard key source. `URL` is the only supported value; `HASH` was removed (it hashed `req.hash`, which cluster routing in `vcl_recv` never populates — see #92). |
| `shard.healthy` | string | `CHOSEN` | Health evaluation strategy (`CHOSEN`, `ALL`). |

> Note on `shard.by` and `shard.healthy`:
> - **Cluster-peer director** (when `cluster.enabled: true`): honored automatically —
>   the operator emits `<director>.backend(by=<by>, healthy=<healthy>)` in `vcl_recv`.
> - **Per-backend directors**: honored for the default route of the first
>   backend (requests no `vclRecv` snippet routes). For routes you pick in a
>   snippet they remain request-time arguments, e.g.
>   `set req.backend_hint = plone.backend(by=URL, healthy=CHOSEN);`.

### cluster

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | boolean | `false` | Enable cluster peer routing between pods. |
| `peerRouting.type` | string | `shard` | Director type for peer-to-peer routing. |
| `retry.maxRetries` | integer | `1` | Retries on another Varnish peer when the peer sent no response; `0` disables, maximum `3`. |

In cluster mode a request passes through two pods, and most custom VCL snippets
run on the owning pod only. See
[Custom snippets in cluster mode](cluster-snippets).

### vcl.snippets

Snippets that reference a per-pod backend name such as `plone_0` are admitted,
but the admission webhook returns a warning for each snippet field and backend
involved: per-pod names are renumbered whenever the endpoints change. Use
`<backend>.backend()` instead, see
[Per-backend directors](../how-to/per-backend-directors.md).

### varnishParameters

A `map[string]string`. Each entry becomes one `-p` flag on `varnishd`: the key
must be a bare varnishd parameter name (lowercase letters, digits, and
underscores, starting with a letter — the admission webhook rejects anything
else, including padding whitespace or uppercase), and the value is passed
through unchanged. For example:

```yaml
varnishParameters:
  thread_pool_min: "100"
  thread_pool_max: "1000"
  feature: "+esi_disable_xml_check"
```

renders as `-p feature=+esi_disable_xml_check -p thread_pool_max=1000 -p thread_pool_min=100`
on the varnish container — note the args come out sorted by key (`feature` <
`thread_pool_max` < `thread_pool_min`), not in the order they were written in
YAML, so the pod template does not churn between reconciles for the same
params.

Two parameters are blocked by the admission webhook regardless of value,
because they allow arbitrary code execution at VCL-compile time:
`vcc_allow_inline_c` and `cc_command`.

ESI (Edge Side Includes) processing is **not** controlled through this map —
see the `esi` section below. `varnishParameters`' `feature` key is still relevant
alongside it, for the one real varnishd feature bit ESI sometimes needs
(`esi_disable_xml_check`); see that section for when and why.

### esi

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | boolean | `false` | Enable Edge Side Includes (ESI) processing. |

```yaml
esi:
  enabled: true
```

Setting `esi.enabled: true` makes the generated VCL set `beresp.do_esi = true`
whenever a backend response carries `Surrogate-Control: content="ESI/1.0"`.
ESI is core varnishd functionality, not a VMOD, and — unlike most varnishd
behaviour toggles — it is not controlled by any `varnishParameters`/`-p
feature=...` flag either: real varnishd has no `esi` feature bit, so passing
`{"feature": "+esi"}` (an earlier, now-removed convention) crashes varnishd
outright with `Unknown feature bit (+esi)` rather than enabling anything.
`esi.enabled` replaces that convention and carries no such risk — it never
reaches varnishd's argv at all.

If response bodies don't reliably start with `<` (varnishd's ESI
implementation sniffs the body to decide whether to scan it for `<esi:...>`
tags), also set the real varnishd feature token alongside it:

```yaml
esi:
  enabled: true
varnishParameters:
  feature: "+esi_disable_xml_check"
```

### invalidation

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `purge.soft` | boolean | `true` | Use soft purge (stale-while-revalidate). |
| `purge.allowedSources` | list | `[]` | CIDRs permitted to send `PURGE`. `127.0.0.1` and the operator pod IP are always included. |
| `purge.hosts` | list | `[]` | Site hostnames a `PURGE` through the invalidation proxy applies to when the request has no `X-Vinyl-Purge-Host` header; one purge is sent per host. Needed for clients that cannot set headers, such as `plone.cachepurging`. At most 16, lowercase hostnames. See [Purge and BAN the cache](../how-to/purge-ban.md). |
| `xkey` | object | nil | Xkey (surrogate key) configuration. |
| `xkey.enabled` | boolean | `false` | Loads `vmod_xkey` and handles xkey `PURGE` requests. The operator's invalidation API purges keys hard. |
| `ban.enabled` | boolean | `false` | When `true`, emit a `vinyl_ban_allowed` ACL and a `BAN` handler in `vcl_recv` that dispatches `std.ban(req.http.X-Vinyl-Ban)`. Also emits ban-lurker-friendly `x-url`/`x-host` headers on stored objects. |
| `ban.allowedSources` | list | `[]` | CIDRs permitted to send `BAN` (in addition to `127.0.0.1` and the operator pod IP). |
| `ban.rateLimitPerMinute` | integer | `0` | Inert in v0.4.2 — field is accepted but not enforced; rate-limiting is tracked as follow-up work. |

> Note on BAN security: any client whose source IP is in `vinyl_ban_allowed`
> can invalidate the entire cache with an arbitrary ban expression. Scope
> `ban.allowedSources` tightly to trusted callers only. Ban expressions
> should prefer `obj.http.x-url` / `obj.http.x-host` over `req.*` fields —
> only the former can be processed by the ban lurker, so `req.*` bans
> accumulate without being compacted.
>
> Clients send BAN requests like:
>
> ```bash
> curl -X BAN -H 'X-Vinyl-Ban: obj.http.x-url ~ "^/news/"' http://varnish.svc/
> ```

### monitoring

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | boolean | `false` | Activates Prometheus metrics scraping. |
| `serviceMonitor.enabled` | boolean | `false` | Creates a `ServiceMonitor` for this cache. |
| `prometheusRules.enabled` | boolean | `false` | Creates a `PrometheusRule` named `<cache>-alerts` with the alerts for this cache. |
| `prometheusRules.lowHitRatio.threshold` | float | `0.5` | Hit ratio of a pod below which `VinylCacheLowHitRatio` fires. Greater than `0` and at most `1`. |
| `prometheusRules.lowHitRatio.for` | string | `15m` | How long the ratio must stay below the threshold. A Prometheus duration such as `30s`, `15m`, `1h30m` or `1d`; Go-style fractions like `1.5h` are rejected. |

See [Set up monitoring](../how-to/setup-monitoring.md) for the alerts.

## Status fields

| Field | Type | Description |
|-------|------|-------------|
| `phase` | string | `Pending`, `Ready`, `Degraded`, or `Error`. `Degraded` also covers a backend without endpoints, see `BackendsAvailable` below. |
| `readyReplicas` | integer | Number of pods that passed their readiness checks. |
| `activeVCL.hash` | string | SHA-256 hash of the currently active VCL. |
| `invalidationURL` | string | Address for `PURGE`, `BAN` and xkey requests, e.g. `http://my-cache-invalidation.app:8090`. The host includes the namespace; the bare Service name is rejected. See [Purge and BAN the cache](../how-to/purge-ban.md). |
| `runtime.flavor` | string | Resolved flavor. Shown in the printer column `Flavor`. |
| `runtime.version` | string | Resolved version, including a default that followed the chart. Shown in the printer column `Version`. |
| `runtime.daemon` | string | Resolved daemon image reference, after a `spec.image` override. |
| `runtime.exporter` | string | Resolved exporter image. Set while the exporter is enabled. |
| `runtime.tracer` | string | Resolved tracer image. Set while `spec.tracing` is enabled. |
| `conditions` | list | Standard Kubernetes conditions, see {ref}`vinylcache-conditions`. |

(vinylcache-conditions)=

### Conditions

| Type | Meaning |
|------|---------|
| `Ready` | `True` when all replicas are ready (reason `AllReady`), `False` otherwise (`NotAllReady`, or `ReconcileError` after a failed reconcile). A backend without endpoints does not make the cache not ready. |
| `VCLSynced` | `True` (reason `VCLPushed`) while the VCL is active on the ready pods, `False` (`VCLPushFailed`) when a push failed. |
| `BackendsAvailable` | `True` (reason `BackendsConfigured`) when every backend in `spec.backends` has ready endpoints. `False` (reason `BackendWithoutEndpoints`) when at least one has none; the message lists them. The cache keeps serving, requests routed to those backends answer `503`, and the phase is `Degraded`. See {ref}`backends-without-endpoints`. |
| `RuntimeResolved` | `True` (reason `Resolved`) when the catalog has an entry for the cache's flavor and version. `False` (reason `NotInCatalog`) when it has none any more; the operator then leaves the StatefulSet unchanged. |
| `Progressing` | `True` (reason `WaitingForReplicas`) while not all replicas are ready, `False` (reason `ReconcileComplete`) afterwards. `True` with reason `RuntimeNotInCatalog` while the runtime is not in the catalog and the StatefulSet is held back. |

## Example

```yaml
apiVersion: vinyl.bluedynamics.eu/v1
kind: VinylCache
metadata:
  name: my-cache
  namespace: production
spec:
  replicas: 3
  backends:
    - name: api
      serviceRef:
        name: api-service
      port: 8080
      probe:
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
      enabled: true
  debounce:
    duration: 1s
  retry:
    maxAttempts: 3
    backoffBase: 5s
    backoffMax: 5m
```
