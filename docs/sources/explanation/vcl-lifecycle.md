# VCL lifecycle

The Vinyl Cache daemon compiles its configuration language, VCL, at runtime.
cloud-vinyl automates the VCL lifecycle: generation, compilation, activation, and drift detection.

(vcl-lifecycle-generation)=

## Generation

When the operator reconciles a `VinylCache`, it feeds the spec into the VCL generator:

1. **Collect peers**: list all `Ready` pods in the StatefulSet.
2. **Build template data**: combine spec fields (backends, director settings, invalidation settings, cluster peers) into a `TemplateData` struct.
3. **Render templates**: 15 Go templates produce the full VCL program.
   The program consists of `main.vcl` plus subroutines for `vcl_init`, `vcl_recv`, `vcl_hash`, `vcl_hit`, `vcl_miss`, `vcl_pass`, `vcl_purge`, `vcl_pipe`, `vcl_synth`, `vcl_backend_fetch`, `vcl_backend_response`, `vcl_backend_error`, `vcl_deliver`, and `vcl_fini`.
4. **Hash**: the SHA-256 of the rendered VCL serves as a content-addressable identifier.

(cluster-snippets)=

## Custom snippets in cluster mode

With `spec.cluster.enabled`, each client request passes through the cache twice.
The pod that receives it (the relay hop) sends it with `return(pass)` to the pod that owns the URL's shard.
That pod (the owner hop) does the cache lookup and, on a miss, the backend fetch.
Both hops run the same VCL.
The generator therefore decides which custom snippets run on which hop:

| Snippet | Relay hop | Owner hop |
|---|---|---|
| `vclRecv` | no | yes |
| `vclHash`, `vclPass`, `vclDeliver` | no | yes |
| `vclBackendFetch`, `vclBackendResponse` | no | yes |
| `vclHit`, `vclMiss`, `vclPurge` | never reached | yes |
| `vclBackendError`, `vclSynth` | yes | yes |

Snippets that belong to the lookup and the fetch run on the owner hop only.
On the relay hop they would undo the owner's work.
A `vclDeliver` snippet that sets `x-cache` from `obj.hits` would overwrite the owner's `HIT` with `MISS`, because a passed request has no hits.
A URL rewrite in `vclBackendFetch` would be applied twice.
`vclBackendError` and `vclSynth` run on both hops, because the relay hop builds the error page when the owner pod fails.

The relay hop marks the request with the `X-Vinyl-Shard` header and forwards it to the owner, which removes it in `vcl_recv`.
The generator wraps the owner-only snippets in an `if (!req.http.X-Vinyl-Shard)` block (`bereq.http` on the backend side).
A `return` inside a snippet still works.
Without cluster mode there is only one hop, and the snippets are rendered without the guard.

(cluster-esi)=

### ESI fragments in cluster mode

With `spec.esi.enabled` as well, fragments are sharded like any other URL.
The page is expanded on its owner hop, because the relay hop receives the already expanded body.
An ESI subrequest starts from the headers its parent was received with.
On the owner hop these include `X-Vinyl-Shard` from the relay.
Left alone, the fragment would take the owner-hop path and be looked up and cached on the page's owner.
Every pod that owns a page including a shared fragment (navigation, footer) would then fetch it from the backend itself.

The generated `vcl_recv` therefore drops the header for ESI subrequests (`req.esi_level > 0`), before the shard decision.
The fragment is then relayed to its own shard owner like a fresh client request.
It arrives there as a normal request (`esi_level` 0) carrying `X-Vinyl-Shard`.
The owner serves it from its cache and does not route again.
A relayed fragment cannot loop between pods for the same reason.
The guard is only rendered when both cluster mode and ESI are enabled.

(vcl-lifecycle-push)=

## Push

For each ready pod the operator:

1. Calls `GET /vcl/active` on the vinyl-agent to get the currently active hash.
2. If the hash matches, skips the pod (no-op).
3. Otherwise, calls `POST /vcl/push` with the VCL content and a name derived from the hash.
4. The daemon compiles and activates the new VCL.
   It discards the old VCL version once no requests reference it.

Pushes to all pods happen in parallel.
If some pods fail but at least one succeeds, the operator records a `Degraded` status but does not return an error.
Those pods are retried on the next reconcile.
If all pods fail, the reconcile returns an error and triggers a retry.

(vcl-lifecycle-debouncing)=

## Debouncing

Rapid changes to a `VinylCache` spec (or rapid endpoint churn during rolling restarts) would cause many VCL regenerations in quick succession.
The operator debounces: it waits `spec.debounce.duration` (default 1 s) after the last change before pushing new VCL.

(vcl-lifecycle-drift-detection)=

## Drift detection

The operator detects pods that diverge from the desired VCL, for example after a manual `varnishadm vcl.use`.
It finds them on the next reconcile cycle via the hash comparison in the push step.
The `VinylCacheVCLDrift` PrometheusRule alert fires if more than 2 VCL versions are loaded.
