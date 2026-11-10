# Varnish and Vinyl Cache version support

cloud-vinyl runs two lineages of the cache daemon, selected with `spec.flavor`: `varnish` (the docker-library `varnish` image) and `vinyl` (Vinyl Cache, in an image we publish).
This page states which versions the operator is built and tested against.
See {ref}`explanation-lineages` for the difference and {ref}`run-vinyl-cache` for how to choose.

(varnish-versions-support-matrix)=

## Support matrix

The runtime catalog of the chart ships these combinations.
Each has a daemon, an exporter and a tracer image, pinned by digest.

| Flavor | Version | Status | Daemon image |
|--------|---------|--------|--------------|
| `varnish` | 9.1.0 | **Supported**, default | `docker.io/library/varnish:9.1.0` |
| `vinyl` | 9.1.0 | **Supported** | `ghcr.io/bluedynamics/vinyl-cache:9.1.0-r1` |

The end-to-end suite runs against both rows.
The admission webhook rejects a flavor and version that is not in the catalog.
For a daemon version the catalog does not list, add an entry in the chart values (see {ref}`helm-values-runtimes`).

Older Varnish versions are not in the catalog.
They still run with `spec.image` as a daemon override.
The sidecars then follow the catalog's default version, and the webhook warns about it (see {ref}`replace-an-image`).

| Varnish | Status | Notes |
|---------|--------|-------|
| 9.1.x | **Supported** | In the catalog. |
| 9.0.x | Works, not tested in CI | Needs `spec.image`. Tracing needs a tracer built for 9.0, see below. |
| 8.0.x | Works, end of life | Needs `spec.image`. No upstream updates since September 2026, and affected by [VSV00020](https://vinyl-cache.org/security/VSV00020.html). Tracing needs a tracer built for 8.0. |
| 7.6.x | Works, end of life | Needs `spec.image`. No longer receives upstream updates. |
| 7.7.x | Works, end of life | Needs `spec.image`. No longer receives upstream updates. |
| 6.0.x LTS | **Not supported** | See below. |

The default cache in the documentation and end-to-end fixtures is `varnish` 9.1.0.

The Varnish versions above are those of the docker-library `varnish` image, which Varnish Software builds.
The open source project formerly called Varnish Cache is now [Vinyl Cache](https://vinyl-cache.org/).
It uses the same version numbers for different builds.
For Vinyl we publish `ghcr.io/bluedynamics/vinyl-cache`, with the immutable tags `<version>-r<N>` and a moving tag `<version>`.

(varnish-versions-upgrading-9-1)=

## Upgrading to Varnish 9.1

cloud-vinyl 0.9.0 and earlier cannot start a 9.1 image.
Their bootstrap VCL calls `synthetic()`, which Varnish 9.1 removed.
varnishd exits when the VCL it loads at startup does not compile.
Upgrade the operator first, then move `spec.image` to 9.1.

Do not name a storage `synth` in `spec.storage`.
Varnish 9.1 defines a storage of that name, and the admission webhook rejects it.

(varnish-versions-why-6-0-unsupported)=

## Why 6.0 LTS is not supported

Two independent mechanisms break on the 6.0 series.

**The metrics exporter cannot be built for it.**
The exporter sidecar bundles a `varnishstat` copied from the cache image, together with `libvarnishapi`.
Varnish 7.6 through 9.1 ship `libvarnishapi.so.3`.
Varnish 6.0 ships `libvarnishapi.so.1`.
`Dockerfile.exporter` copies the `.so.3` generation and fails the build when the base image does not provide it.

**The agent cannot read `vcl.list`.**
Varnish 6.0 collapses the state and temperature fields into a single column:

```text
varnish 6.0.18:  active      auto/warm          0 boot
varnish 8.0.2:   active   auto   warm   0   boot
```

The agent parses the five-column layout.
Against a 6.0 varnishd it reports an error rather than a VCL name.
The `/health` endpoint returns `503`, and the pod stays out of the Service endpoints.

(varnish-versions-mixing)=

## Mixing varnishstat and varnishd versions

The exporter sidecar shells out to `varnishstat`, which reads the shared memory segment written by `varnishd`.
Both must belong to the same `libvarnishapi` generation.

Within the `.so.3` generation, every combination of Varnish 7.6, 8.0 and 9.0 works in both directions, as verified.
8.0.2 with 9.1.0 works too.
`varnish_up` reports `1` and the full counter set is exported.
Across the 6.0 boundary, `varnishstat` reports `Could not get hold of varnishd, is it running?` and no `varnish_*` metrics are produced.

```{warning}
This tolerance is a property of the current `libvarnishapi` generation and carries no guarantee.
Keep `VARNISH_IMAGE` in `Dockerfile.exporter` aligned with the cache image you run.
```

(tracing-version-match)=

## Tracing needs an exact version match

The tracer sidecar reads the shared memory log with a `varnishlog` copied from the cache image it was built against.
`varnishlog` does not read across versions.
A 9.1.0 `varnishlog` against an 8.0.2 `varnishd`, or the reverse, prints nothing and reports no error.
The tracer runs, and no spans arrive.

The tracer image comes from the runtime catalog entry of the cache's flavor and version.
Each release bundles the log tool of 9.1.0 (`varnishlog` or `vinyllog`).
With `spec.tracing` enabled, run a 9.1 cache, or add a catalog entry with a tracer built for your version.

(varnish-versions-version-specific)=

## Version-specific behavior

| Requirement | Reason |
|-------------|--------|
| `varnishd` (flavor `vinyl`: `vinyld`) on `PATH` | The operator sets the container command to the daemon binary and passes every flag itself, including `-F`, `-f`, `-a`, `-n` and `-s`. The image's entrypoint does not run, so its environment variables (`VARNISH_HTTP_PORT`, `VARNISH_SIZE` and so on) have no effect. |
| `vcl 4.1` | The generated VCL and the bootstrap VCL both declare `vcl 4.1`. |
| `-j none` | The pods run as UID 65532. The default jail requires root for chroot setup. |

All three are satisfied by every version in the support matrix above.

varnishd's instance directory is `/var/lib/varnish/varnishd`, passed as `-n` to varnishd and to both sidecars.
HTTP/2 is enabled with `-p feature=+http2`.
`varnishParameters: {feature: "-http2"}` turns it off.
