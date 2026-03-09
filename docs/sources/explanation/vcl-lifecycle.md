# VCL Lifecycle

Varnish Configuration Language (VCL) is compiled by the Varnish daemon at runtime.
cloud-vinyl automates the VCL lifecycle: generation, compilation, activation, and drift detection.

## Generation

When the operator reconciles a `VinylCache`, it feeds the spec into the VCL generator:

1. **Collect peers** — list all `Ready` pods in the StatefulSet.
2. **Build template data** — combine spec fields (backends, director settings, invalidation config,
   cluster peers) into a `TemplateData` struct.
3. **Render templates** — 14 Go templates produce the full VCL program
   (`main.vcl` + subroutines for `vcl_init`, `vcl_recv`, `vcl_hash`, `vcl_hit`, `vcl_miss`,
   `vcl_pass`, `vcl_purge`, `vcl_pipe`, `vcl_synth`, `vcl_backend_fetch`,
   `vcl_backend_response`, `vcl_deliver`, `vcl_fini`).
4. **Hash** — SHA-256 of the rendered VCL is used as a content-addressable identifier.

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
