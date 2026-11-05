# Per-backend directors

This guide explains how each `spec.backends[]` entry expands to one director
per backend, and when to override the default `shard` director.

## What changed and why it matters

Each `spec.backends[]` entry is resolved via `EndpointSlice` to the current
set of backend pods, and the generator emits one per-pod `backend`
declaration plus a director that groups them. In VCL, backends are
addressed by `<backend>.backend()`, not by hardcoded per-pod names.

This removes the class of guru-meditation failures that occurred during
rolling updates, where VCL snippets referring to `<backend>_0` suddenly
pointed at a pod that no longer existed. See issue #11 for background.

(default-routing)=

## Default behaviour

If `spec.backends[].director` is unset, the generator emits a `shard`
director for that backend. This is the right default for any application with
cache locality: the same URL consistently lands on the same backend pod, which
maximises origin cache-hit rate.

The defaults below apply only when `director:` is set on the backend. Without a
`director:` block the director runs with the vmod defaults: no warmup, no
rampup, `healthy=CHOSEN`.

| Parameter | Default with `director:` set |
|-----------|---------|
| `by` | `URL` |
| `healthy` | `CHOSEN` |
| `warmup` | `0.1` |
| `rampup` | `30s` |

### Requests without a snippet

Requests that no `vclRecv` snippet routes go to the **first** entry of
`spec.backends`, through that backend's director. The generated `vcl_recv`
sets the backend hint before your snippet runs, so a snippet's own
`set req.backend_hint = ...` still wins. With clustering, the hint also applies
to the owner hop and to ESI fragments.

What the director gets as its key:

| Type | Default route |
|------|---------------|
| `shard` | `<backend>.backend(by=URL)`, plus `healthy=<value>` when `shard.healthy` is set. The same URL stays on the same pod. |
| `round_robin`, `random`, `fallback` | `<backend>.backend()` |
| `hash` | `<backend>.backend(req.http.<header>)` when `hash.header` is set, otherwise `<backend>.backend(req.url)`. |

`hash.header` is written into the VCL as `req.http.<header>`, so it must be a
name VCL accepts there: a letter, then letters, digits, `-` or `_`. The CRD and
the admission webhook reject anything else, including header names that HTTP
allows but VCL does not, such as `x.key`.

Other backends are reachable only through a snippet. A cache with a single
backend needs no snippet at all.

## When to override

| Type | Use when |
|------|----------|
| `shard` | Default. Sticky consistent-hashing, good for Plone and any app with cache locality. |
| `round_robin` | Stateless backends where any pod can serve any request equally. |
| `random` | Rarely needed; prefer `round_robin` for stateless workloads. |
| `hash` | Consistent hashing on a specific header (e.g. session affinity). Set `hash.header` for the default route; for other routes call `<backend>.backend(<key>)` from your `vcl_recv` snippet. |
| `fallback` | Primary/standby topology; the first healthy backend wins. |

## Example: mixed workload

Two backends in one cache cluster — a Plone origin (sticky) and a
stateless API (round-robin):

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

## Breaking change: snippet migration

```{important}
VCL snippets that hardcoded per-pod backend names (e.g. `plone_0`,
`plone_1`) will break during rollouts. The set of resolved pods comes
from `EndpointSlice` and is re-numbered as pods come and go — `_0` may
not exist in the updated pod set.

Migrate to `<backend>.backend()`, which dispatches through the
per-backend director.

The admission webhook helps you find leftovers: when any
`spec.vcl.snippets` field contains `<backend>_<N>` for a backend in
`spec.backends`, applying the VinylCache prints a warning naming the field
and the replacement. It is only a warning, the resource is still admitted.
```

**Before — fragile:**

```vcl
sub vcl_recv {
  set req.backend_hint = plone_0;
}
```

**After — uses the director:**

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

## Retry on another backend

When a backend pod disappears without draining (OOMKill, crash, node
failure), Varnish keeps sending it requests until the health probe marks it
sick or the operator pushes a VCL without it. To bridge that gap, the
generated VCL retries a fetch once on another backend of the same group:

- only `GET` and `HEAD` requests,
- only when no response arrived: connection refused, connection closed
  before the response, or a connect timeout,
- not after a `first_byte_timeout`, which would double the wait and the load
  on a backend that is already slow.

`shard` directors ask for the next alternative backend of the same key, so
the retry never lands on the backend that just failed. `round_robin` and
`random` ask the director again, which usually picks another backend. `hash`
and `fallback` directors would pick the same backend, so they never retry.

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

`spec.cluster.retry.maxRetries` does the same for the hop between Varnish
pods, so a request is passed to another peer when the responsible one is
gone.

```{note}
The retry moves to another backend in `vcl_backend_fetch`, before your
`vclBackendFetch` snippet. A snippet that sets `bereq.backend`
unconditionally sends the retry back to the same backend. Likewise, a
`shard` hint resolved with `resolve=LAZY` in `vcl_recv` is not recognised
and gets no retry; the default `plone.backend()` is.
```

See "Watch fetch retries" in [Set up monitoring](setup-monitoring.md) for how
retries show up in the metrics.

(backends-without-endpoints)=

## Backends without endpoints

A backend whose Service has no ready endpoints, for example because its
Deployment is scaled to zero, does not keep the cache from starting. The
operator still pushes the VCL, the cache pods become ready, and the other
backends keep working.

For the backend without endpoints, the generated VCL contains one placeholder
backend (`noendpoints_<backend>`) that is never healthy. Requests routed to
that backend answer `503` instead of reaching an origin. When endpoints
return, the operator regenerates the VCL and the route recovers without a
restart.

The `VinylCache` reports it in its status:

- `BackendsAvailable` is `False` with reason `BackendWithoutEndpoints`, and the
  message names the backends.
- `phase` is `Degraded`.
- `Ready` stays `True` while all replicas are ready.

The placeholder shows up as an unhealthy backend, so the
`VinylCacheBackendUnhealthy` alert fires for it after its usual delay.

```{note}
Requests that no `vclRecv` snippet routes go to the first backend's director
(see {ref}`default-routing`). If the first backend has no endpoints, those
requests answer `503` instead of reaching a pod of another backend. The
`BackendsAvailable` condition, the `Degraded` phase and the
`VinylCacheBackendUnhealthy` alert show it.
```

## Debounce

Backend pod churn (scale events, rolling updates) triggers reconciles.
The operator coalesces change events inside a short window before pushing
new VCL, so a scale from 2 → 10 pods produces a single VCL push, not ten.

```{note}
The window is configured via `spec.debounce.duration` (default **1s**).
Raise it if your backend pods have a long warm-up and you want to avoid
pushing VCL mid-rollout.
```

See also: [VinylCache spec reference](../reference/vinylcache-spec.md).
