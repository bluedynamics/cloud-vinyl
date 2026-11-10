# Route requests to backends

This guide shows you how to choose a director, route requests in snippets, migrate old snippets, and tune retries and debounce.
For the reasons behind the design, see {ref}`backend-routing-why-one-director`.
For defaults and field details, see the [VinylCache spec reference](../reference/vinylcache-spec.md).

(route-requests-choose-director)=

## Choose a director type

If you set no `spec.backends[].director`, the generator emits a `shard` director for that backend.
This default suits any application with cache locality.
If another type fits your workload better, set `director.type`:

| Type | Use when |
|------|----------|
| `shard` | Default. Sticky consistent-hashing, good for Plone and any app with cache locality. |
| `round_robin` | Stateless backends where any pod can serve any request equally. |
| `random` | Rarely needed. Prefer `round_robin` for stateless workloads. |
| `hash` | Consistent hashing on a specific header (for example session affinity). Set `hash.header` for the default route. For other routes call `<backend>.backend(<key>)` from your `vcl_recv` snippet. |
| `fallback` | Primary/standby topology. The first healthy backend wins. |

The spec reference lists the director defaults and the default route per type.

(route-requests-mixed-workload)=

## Route a mixed workload

If one cache fronts a Plone origin (sticky) and a stateless API (round-robin), declare two backends and route in `vclRecv`.

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

(route-requests-snippet-migration)=

## Migrate snippets from per-pod names

If a snippet uses fixed per-pod backend names such as `plone_0` and `plone_1`, replace them with `<backend>.backend()`.
The call dispatches through the per-backend director.

```{important}
Snippets with per-pod names break during rollouts, because `_0` may not exist in the updated pod set.
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

If the backend uses a `hash` director, pass the key explicitly:

```vcl
sub vcl_recv {
  set req.backend_hint = plone.backend(req.http.X-Session-ID);
}
```

(route-requests-retry)=

## Set or disable retries

If you want a different number of retries per backend, set `retry.maxRetries`.
Use `0` to disable retries:

```yaml
spec:
  backends:
    - name: plone
      serviceRef:
        name: plone-service
      retry:
        maxRetries: 2   # default 1, maximum 3
```

If you also want to change the hop between cache pods, set `spec.cluster.retry.maxRetries`.
A request is then passed to another peer when the responsible one is gone.

```{note}
The retry moves to another backend in `vcl_backend_fetch`, before your `vclBackendFetch` snippet.
A snippet that sets `bereq.backend` unconditionally sends the retry back to the same backend.
A `shard` hint resolved with `resolve=LAZY` in `vcl_recv` is not recognized and gets no retry.
The default `plone.backend()` is recognized.
```

See "Watch fetch retries" in [Set up monitoring](setup-monitoring.md) for how retries show up in the metrics.
{ref}`backend-routing-retry` explains when a retry happens.

(route-requests-debounce)=

## Raise the debounce window

If your backend pods have a long warm-up, raise `spec.debounce.duration` (default **1s**).
The operator then avoids pushing VCL mid-rollout.
See {ref}`backend-routing-debounce` for what the window does.
