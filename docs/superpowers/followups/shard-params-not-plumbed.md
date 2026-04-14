# Follow-up: plumb `shard.by` / `shard.healthy` through to per-backend director VCL

Flagged by the final code review on #11.

## Problem

`VinylCacheSpec.Director.Shard.By` (`HASH|URL`) and `.Healthy` (`CHOSEN|ALL`)
are defaulted by the webhook and persisted on the resource, but the generator
does not emit them into the per-backend director in `vcl_init.vcl.tmpl`.
Users who set `by: URL` or `healthy: ALL` on a per-backend director see
their configuration silently ignored.

The cluster-peer director does consume `Spec.Director.Shard.Warmup` and
`Rampup` via `set_warmup` / `set_rampup`, but does not consume `by`/`healthy`
either — `by=URL` would need to be passed at request time to `.backend(by=URL)`
(see `vcl_recv.vcl.tmpl:20`), and `healthy` is a runtime toggle on the
shard director's `reconfigure()`.

## Proposed fix

1. Extend `generator.DirectorInfo` with `By` and `Healthy` fields
   (we removed `By` as dead code in 15d077e1 — re-adding it under this ticket).
2. Emit `.set_healthy(<healthy>);` inside the shard branch of `vcl_init.vcl.tmpl`
   when set.
3. Emit `.backend(by=<by>)` in the default `vcl_recv` routing (if we ever
   add default routing — currently handled by user snippet).
4. Add tests for each.

## References

- `internal/generator/generator.go` — `DirectorInfo` struct.
- `internal/generator/templates/vcl_init.vcl.tmpl` — shard branch.
- `api/v1alpha1/vinylcache_types.go:239-251` — CRD fields.
- Final review minor finding M3.
