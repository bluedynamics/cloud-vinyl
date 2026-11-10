# Supported versions

This page lists the daemon versions cloud-vinyl supports and what it requires of a daemon image.
`spec.flavor` selects the lineage: `vinyl` (Vinyl Cache, in an image cloud-vinyl publishes) or `varnish` (the docker-library `varnish` image).
See {ref}`explanation-lineages` for the difference and {ref}`run-vinyl-cache` for how to choose.

(versions-support-matrix)=

## Support matrix

The {term}`runtime catalog` of the chart ships these combinations.
Each has a daemon, an exporter and a tracer image, pinned by digest.

| Flavor | Version | Status | Daemon image |
|--------|---------|--------|--------------|
| `varnish` | 9.1.0 | **Supported**, default | `docker.io/library/varnish:9.1.0` |
| `vinyl` | 9.1.0 | **Supported** | `ghcr.io/bluedynamics/vinyl-cache:9.1.0-r1` |

The end-to-end suite runs against both rows.
The admission webhook rejects a {term}`flavor` and version that is not in the catalog.

Versions outside the catalog are not supported.
To run one anyway, add a catalog entry in the chart values (see {ref}`helm-values-runtimes`).
Its daemon, exporter and tracer must all be of exactly that version.
A `spec.image` with another version keeps the sidecars of the catalog's default version (see {ref}`replace-an-image`).

(tracing-version-match)=

## Sidecar versions

The exporter's `varnishstat` and the tracer's log tool must come from the same lineage and version as the daemon.
A log tool of another version prints nothing and reports no error.
The tracer then runs, and no spans arrive.
Each release bundles the tools of the catalog's version, 9.1.0, for both flavors.

(versions-image-requirements)=

## Image requirements

A daemon image must meet these requirements.

| Image must have | Reason |
|-----------------|--------|
| `varnishd` (flavor `vinyl`: `vinyld`) on `PATH` | The operator sets the container command to the daemon binary and passes every flag itself, including `-F`, `-f`, `-a`, `-n` and `-s`. The image's entrypoint does not run, so its environment variables (such as `VARNISH_HTTP_PORT` and `VARNISH_SIZE`) have no effect. |
| `vcl 4.1` | The generated VCL and the bootstrap VCL both declare `vcl 4.1`. |
| `-j none` | The pods run as UID 65532. The default jail requires root for chroot setup. |

Every version in the support matrix meets them.

The daemon's instance directory is `/var/lib/varnish/varnishd`, passed as `-n` to the daemon and to both sidecars.
The operator enables HTTP/2 with `-p feature=+http2`.
`varnishParameters: {feature: "-http2"}` turns it off.
Do not name a storage `synth`, because the 9.1 daemon defines a storage of that name.
The admission webhook rejects it.
