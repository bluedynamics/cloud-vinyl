# Per-backend directors

This guide explains how each `spec.backends[]` entry expands to one director per backend, and when to override the default `shard` director.

(per-backend-directors-what-changed)=

## What changed and why it matters

Each `spec.backends[]` entry is resolved via `EndpointSlice` to the current set of backend pods.
The generator emits one per-pod `backend` declaration plus a director that groups them.
In VCL, you address backends by `<backend>.backend()`.
Do not use hardcoded per-pod names.

This removes the class of guru-meditation failures that occurred during rolling updates.
VCL snippets referring to `<backend>_0` suddenly pointed at a pod that no longer existed.
See issue #11 for background.

(default-routing)=

## Default behavior

If `spec.backends[].director` is unset, the generator emits a `shard` director for that backend.
This is the right default for any application with cache locality.
The same URL consistently lands on the same backend pod, which maximizes origin cache-hit rate.

The defaults below apply only when `director:` is set on the backend.
Without a `director:` block the director runs with the vmod defaults: no warmup, no ramp-up, `healthy=CHOSEN`.

| Parameter | Default with `director:` set |
|-----------|---------|
| `by` | `URL` |
| `healthy` | `CHOSEN` |
| `warmup` | `0.1` |
| `rampup` | `30s` |

### Requests without a snippet

Requests that no `vclRecv` snippet routes go to the **first** entry of `spec.backends`, through that backend's director.
The generated `vcl_recv` sets the backend hint before your snippet runs.
A snippet's own `set req.backend_hint = ...` still wins.
With clustering, the hint also applies to the owner hop and to ESI fragments.

What the director gets as its key:

| Type | Default route |
|------|---------------|
| `shard` | `<backend>.backend(by=URL)`, plus `healthy=<value>` when `shard.healthy` is set. The same URL stays on the same pod. |
| `round_robin`, `random`, `fallback` | `<backend>.backend()` |
| `hash` | `<backend>.backend(req.http.<header>)` when `hash.header` is set, otherwise `<backend>.backend(req.url)`. |

`hash.header` is written into the VCL as `req.http.<header>`.
It must be a name VCL accepts there: a letter, then letters, digits, `-` or `_`.
The CRD and the admission webhook reject anything else, including header names that HTTP allows but VCL does not, such as `x.key`.

Other backends are reachable only through a snippet.
A cache with a single backend needs no snippet at all.

(per-backend-directors-override)=

## When to override

| Type | Use when |
|------|----------|
| `shard` | Default. Sticky consistent-hashing, good for Plone and any app with cache locality. |
| `round_robin` | Stateless backends where any pod can serve any request equally. |
| `random` | Rarely needed. Prefer `round_robin` for stateless workloads. |
| `hash` | Consistent hashing on a specific header (for example session affinity). Set `hash.header` for the default route. For other routes call `<backend>.backend(<key>)` from your `vcl_recv` snippet. |
| `fallback` | Primary/standby topology. The first healthy backend wins. |

(per-backend-directors-mixed-workload)=

## Example: mixed workload

Two backends in one cache cluster: a Plone origin (sticky) and a stateless API (round-robin).

```yaml
apiVersion: vinyl.bluedynamics.eu/v1
kind: VinylCache
metadata:
  name: my-cache
spec:
  replicas: 2
  backends:
    - name: plone
      port: 8080
      serviceRef:
        name: plone-service
      director:
        type: shard
    - name: api
      port: 3000
      serviceRef:
        name: api-service
      director:
        type: round_robin
  vcl:
    snippets:
      vclRecv: |
        if (req.url ~ "^/api/") {
          set req.backend_hint = api.backend();
          return(pass);
        }
        set req.backend_hint = plone.backend();
```

(per-backend-directors-snippet-migration)=

## Breaking change: snippet migration

```{important}
VCL snippets that hardcoded per-pod backend names (such as `plone_0` and `plone_1`) break during rollouts.
The set of resolved pods comes from `EndpointSlice` and is re-numbered as pods come and go.
`_0` may not exist in the updated pod set.

Migrate to `<backend>.backend()`, which dispatches through the per-backend director.

The admission webhook helps you find leftovers.
When any `spec.vcl.snippets` field contains `<backend>_<N>` for a backend in `spec.backends`, applying the VinylCache prints a warning naming the field and the replacement.
It is only a warning, and the resource is still admitted.
```

Before (fragile):

```vcl
sub vcl_recv {
  set req.backend_hint = plone_0;
}
```

After (uses the director):

```vcl
sub vcl_recv {
  set req.backend_hint = plone.backend();
}
```

For a `hash` director you must pass the key explicitly:

```vcl
sub vcl_recv {
  set req.backend_hint = plone.backend(req.http.X-Session-ID);
}
```

(per-backend-directors-retry)=

## Retry on another backend

A backend pod can disappear without draining (OOMKill, crash, node failure).
The cache then keeps sending it requests until the health probe marks it sick or the operator pushes a VCL without it.
To bridge that gap, the generated VCL retries a fetch once on another backend of the same group:

- Only `GET` and `HEAD` requests.
- Only when no response arrived: connection refused, connection closed before the response, or a connect timeout.
- Never after a `first_byte_timeout`.
  A retry would double the wait and the load on a backend that is already slow.

`shard` directors ask for the next alternative backend of the same key.
The retry never lands on the backend that just failed.
`round_robin` and `random` ask the director again, which usually picks another backend.
`hash` and `fallback` directors would pick the same backend.
They never retry.

Set the number of retries per backend, or disable them with `0`:

```yaml
spec:
  backends:
    - name: plone
      serviceRef:
        name: plone-service
      retry:
        maxRetries: 2   # default 1, maximum 3
```

`spec.cluster.retry.maxRetries` does the same for the hop between cache pods.
A request is then passed to another peer when the responsible one is gone.

```{note}
The retry moves to another backend in `vcl_backend_fetch`, before your `vclBackendFetch` snippet.
A snippet that sets `bereq.backend` unconditionally sends the retry back to the same backend.
A `shard` hint resolved with `resolve=LAZY` in `vcl_recv` is not recognized and gets no retry.
The default `plone.backend()` is recognized.
```

See "Watch fetch retries" in [Set up monitoring](setup-monitoring.md) for how retries show up in the metrics.

(backends-without-endpoints)=

## Backends without endpoints

A backend whose Service has no ready endpoints does not keep the cache from starting.
An example is a backend whose Deployment is scaled to zero.
The operator still pushes the VCL, the cache pods become ready, and the other backends keep working.

For the backend without endpoints, the generated VCL contains one placeholder backend (`noendpoints_<backend>`) that is never healthy.
Requests routed to that backend answer `503` instead of reaching an origin.
When endpoints return, the operator regenerates the VCL and the route recovers without a restart.

The `VinylCache` reports it in its status:

- `BackendsAvailable` is `False` with reason `BackendWithoutEndpoints`, and the message names the backends.
- `phase` is `Degraded`.
- `Ready` stays `True` while all replicas are ready.

The placeholder shows up as an unhealthy backend.
The `VinylCacheBackendUnhealthy` alert fires for it after its usual delay.

Requests that no `vclRecv` snippet routes go to the first backend's director (see {ref}`default-routing`).
If the first backend has no endpoints, those requests answer `503` instead of reaching a pod of another backend.
The `BackendsAvailable` condition, the `Degraded` phase and the `VinylCacheBackendUnhealthy` alert show it.

(per-backend-directors-debounce)=

## Debounce

Backend pod churn (scale events, rolling updates) triggers reconciles.
The operator coalesces change events inside a short window before pushing new VCL.
A scale from 2 to 10 pods produces a single VCL push.

The window is configured via `spec.debounce.duration` (default **1s**).
Raise it if your backend pods have a long warm-up and you want to avoid pushing VCL mid-rollout.

See also: [VinylCache spec reference](../reference/vinylcache-spec.md).
