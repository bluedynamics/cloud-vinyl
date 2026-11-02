# VCL Lifecycle

Varnish Configuration Language (VCL) is compiled by the Varnish daemon at runtime.
cloud-vinyl automates the VCL lifecycle: generation, compilation, activation, and drift detection.

## Generation

When the operator reconciles a `VinylCache`, it feeds the spec into the VCL generator:

1. **Collect peers** — list all `Ready` pods in the StatefulSet.
2. **Build template data** — combine spec fields (backends, director settings, invalidation config,
   cluster peers) into a `TemplateData` struct.
3. **Render templates** — 15 Go templates produce the full VCL program
   (`main.vcl` + subroutines for `vcl_init`, `vcl_recv`, `vcl_hash`, `vcl_hit`, `vcl_miss`,
   `vcl_pass`, `vcl_purge`, `vcl_pipe`, `vcl_synth`, `vcl_backend_fetch`,
   `vcl_backend_response`, `vcl_backend_error`, `vcl_deliver`, `vcl_fini`).
4. **Hash** — SHA-256 of the rendered VCL is used as a content-addressable identifier.

(cluster-snippets)=

## Custom snippets in cluster mode

With `spec.cluster.enabled`, each client request passes through Varnish twice.
The pod that receives it (the relay hop) sends it with `return(pass)` to the pod
that owns the URL's shard, and that pod (the owner hop) does the cache lookup and,
on a miss, the backend fetch. Both hops run the same VCL, so the generator decides
which custom snippets run on which hop:

| Snippet | Relay hop | Owner hop |
|---|---|---|
| `vclRecv` | no | yes |
| `vclHash`, `vclPass`, `vclDeliver` | no | yes |
| `vclBackendFetch`, `vclBackendResponse` | no | yes |
| `vclHit`, `vclMiss`, `vclPurge` | never reached | yes |
| `vclBackendError`, `vclSynth` | yes | yes |

Snippets that belong to the lookup and the fetch run on the owner hop only. On
the relay hop they would undo the owner's work: a `vclDeliver` snippet that sets
`x-cache` from `obj.hits` would overwrite the owner's `HIT` with `MISS`, because
a passed request has no hits, and a URL rewrite in `vclBackendFetch` would be
applied twice. `vclBackendError` and `vclSynth` run on both hops, because when
the owner pod fails, the relay hop builds the error page.

The relay hop marks the request with the `X-Vinyl-Shard` header and forwards it
to the owner, which removes it in `vcl_recv`. The generator wraps the owner-only
snippets in `if (!req.http.X-Vinyl-Shard) { ... }`, or `bereq.http` on the
backend side. A `return` inside a snippet still works. Without cluster mode,
there is only one hop and the snippets are rendered without the guard.

## Push

For each ready pod the operator:

1. Calls `GET /vcl/active` on the vinyl-agent to get the currently active hash.
2. If the hash matches, skips the pod (no-op).
3. Otherwise, calls `POST /vcl/push` with the VCL content and a name derived from the hash.
4. Varnish compiles and activates the new VCL. The old VCL version is discarded by Varnish
   once no requests reference it.

Pushes to all pods happen in parallel. If some pods fail but at least one succeeds, the
operator records a `Degraded` status but does not return an error (those pods will be retried
on the next reconcile). If all pods fail, the reconcile returns an error and triggers a retry.

## Debouncing

Rapid changes to a `VinylCache` spec (or rapid endpoint churn during rolling restarts) would cause
many VCL regenerations in quick succession. The operator debounces: it waits
`spec.debounce.duration` (default 5 s) after the last change before pushing new VCL.

## Drift detection

Pods that diverge from the desired VCL (e.g. after a manual `varnishadm vcl.use`) are detected
on the next reconcile cycle via the hash comparison in the push step.
The `VinylCacheVCLDrift` PrometheusRule alert fires if more than 2 VCL versions are loaded.
