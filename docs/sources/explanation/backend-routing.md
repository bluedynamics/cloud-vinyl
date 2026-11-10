# Backend routing

This page explains how cloud-vinyl routes requests to backends.
It covers the director per backend, the default route, retries, backends without endpoints, and debounce.
To change any of this, see [Route requests to backends](../how-to/route-requests.md).

(backend-routing-why-one-director)=

## Why one director per backend

Each `spec.backends[]` entry is resolved via `EndpointSlice` to the current set of backend pods.
The generator emits one per-pod `backend` declaration plus a director that groups them.
In VCL, you address backends by `<backend>.backend()`.
Hardcoded per-pod names are not part of that contract.

Earlier versions exposed the per-pod names directly.
The set of resolved pods is re-numbered as pods come and go.
A snippet referring to `<backend>_0` could suddenly point at a pod that no longer existed.
That caused guru-meditation failures during rolling updates.
See issue #11 for background.
One director per backend removes this class of failure.
The director always holds the current pods, whatever their numbers are.

(default-routing)=

## The default route

If `spec.backends[].director` is unset, the generator emits a `shard` director for that backend.
The same URL consistently lands on the same backend pod, which maximizes origin cache-hit rate.
This is the right default for any application with cache locality.

Requests that no `vclRecv` snippet routes go to the **first** entry of `spec.backends`, through that backend's director.
The generated `vcl_recv` sets the backend hint before your snippet runs.
A snippet's own `set req.backend_hint = ...` still wins.
With clustering, the hint also applies to the owner hop and to ESI fragments.

Other backends are reachable only through a snippet.
A cache with a single backend needs no snippet at all.
The reference lists which key each director type gets on the default route.

(backend-routing-retry)=

## When a fetch is retried

A backend pod can disappear without draining (OOMKill, crash, node failure).
The cache then keeps sending it requests until the health probe marks it sick or the operator pushes a VCL without it.
To bridge that gap, the generated VCL retries a fetch once on another backend of the same group.

A retry happens under these conditions:

- Only `GET` and `HEAD` requests.
- Only when no response arrived: connection refused, connection closed before the response, or a connect timeout.
- Never after a `first_byte_timeout`.
  A retry would double the wait and the load on a backend that is already slow.

Whether a retry can pick another backend depends on the director type.
`shard` directors ask for the next alternative backend of the same key.
The retry never lands on the backend that just failed.
`round_robin` and `random` ask the director again, which usually picks another backend.
`hash` and `fallback` directors would pick the same backend.
They never retry.

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

(backend-routing-debounce)=

## Why changes are debounced

Backend pod churn (scale events, rolling updates) triggers reconciles.
Pushing VCL for each event would flood the cache with short-lived configurations.
The operator coalesces change events inside a short window before pushing new VCL.
A scale from 2 to 10 pods produces a single VCL push.
The window is `spec.debounce.duration`, which defaults to 1s.
