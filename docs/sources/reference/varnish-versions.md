# Varnish Version Support

`spec.image` accepts any Varnish image reference. cloud-vinyl does not validate
it, so this page states which versions the operator is built and tested against.

## Support matrix

| Varnish | Status | Notes |
|---------|--------|-------|
| 9.1.x | **Supported** | The end-to-end suite runs against `varnish:9.1.0`. |
| 9.0.x | Works, not tested in CI | Tracing needs a tracer built for 9.0, see below. |
| 8.0.x | Works, end of life | No upstream updates since September 2026, and affected by [VSV00020](https://vinyl-cache.org/security/VSV00020.html). Tracing needs a tracer built for 8.0. |
| 7.6.x | Works, end of life | No longer receives upstream updates. |
| 7.7.x | Works, end of life | No longer receives upstream updates. |
| 6.0.x LTS | **Not supported** | See below. |

The default cache image in the documentation and end-to-end fixtures is
`varnish:9.1.0`.

The versions above are those of the docker-library `varnish` image, which
Varnish Software builds. The open source project formerly called Varnish Cache
is now [Vinyl Cache](https://vinyl-cache.org/). It uses the same version
numbers for different builds, renames the binaries (`vinyld`, `vinylstat`,
`vinyllog`) and publishes no container images. cloud-vinyl does not support
Vinyl Cache images yet, see
[#106](https://github.com/bluedynamics/cloud-vinyl/issues/106).

## Upgrading to Varnish 9.1

cloud-vinyl 0.9.0 and earlier cannot start a 9.1 image. Their bootstrap VCL
calls `synthetic()`, which Varnish 9.1 removed, and varnishd exits when the
VCL it loads at startup does not compile. Upgrade the operator first, then
move `spec.image` to 9.1.

Do not name a storage `synth` in `spec.storage`. Varnish 9.1 defines a storage
of that name, and the admission webhook rejects it.

## Why 6.0 LTS is not supported

Two independent mechanisms break on the 6.0 series.

**The metrics exporter cannot be built for it.** The exporter sidecar bundles a
`varnishstat` copied from the cache image, together with `libvarnishapi`. Varnish
7.6 through 9.1 ship `libvarnishapi.so.3`; Varnish 6.0 ships `libvarnishapi.so.1`.
`Dockerfile.exporter` copies the `.so.3` generation and fails the build when the
base image does not provide it.

**The agent cannot read `vcl.list`.** Varnish 6.0 collapses the state and
temperature fields into a single column:

```text
varnish 6.0.18:  active      auto/warm          0 boot
varnish 8.0.2:   active   auto   warm   0   boot
```

The agent parses the five-column layout. Against a 6.0 varnishd it reports an
error rather than a VCL name, the `/health` endpoint returns `503`, and the pod
stays out of the Service endpoints.

## Mixing varnishstat and varnishd versions

The exporter sidecar shells out to `varnishstat`, which reads the shared memory
segment written by `varnishd`. Both must belong to the same `libvarnishapi`
generation.

Within the `.so.3` generation, every combination of Varnish 7.6, 8.0 and 9.0 has
been verified to work in both directions, and so has 8.0.2 with 9.1.0:
`varnish_up` reports `1` and the full counter set is exported. Across the 6.0
boundary, `varnishstat` reports `Could not get hold of varnishd, is it running?`
and no `varnish_*` metrics are produced.

```{warning}
This tolerance is a property of the current `libvarnishapi` generation, not a
guarantee. You must keep `VARNISH_IMAGE` in `Dockerfile.exporter` aligned with
the cache image you run.
```

## Tracing needs an exact version match

The tracer sidecar reads the shared memory log with a `varnishlog` copied from
the cache image it was built against, and `varnishlog` does not read across
versions. A 9.1.0 `varnishlog` against an 8.0.2 `varnishd`, or the reverse,
prints nothing and reports no error: the tracer runs, and no spans arrive.

The tracer image is set for the whole operator, not per VinylCache, and the
release build bundles `varnishlog` 9.1.0. With `spec.tracing` enabled, run a
9.1 cache image.

## Version-specific behavior

| Requirement | Reason |
|-------------|--------|
| `vcl 4.1` | The generated VCL and the bootstrap VCL both declare `vcl 4.1`. |
| `-j none` | The pods run as UID 65532. The default jail requires root for chroot setup. |

Both are satisfied by every version in the support matrix above.
