(explanation-lineages)=

# Varnish and Vinyl lineages

## Two codebases, one set of version numbers

The open source project formerly called Varnish Cache is now
[Vinyl Cache](https://vinyl-cache.org/). Varnish Software continues its own product as
Varnish, and its docker-library `varnish` image follows that line. Since 9.x both use the
same version numbers for different code. The names differ too: Vinyl renames the binaries
(`vinyld`, `vinylstat`, `vinyllog`), the library (`libvinylapi.so.3`) and the default
paths, and it publishes no container images.

The operator-facing protocols are unchanged between the lineages: the admin CLI,
`varnishstat -j` and the log tags. The operator passes the full daemon command line
itself, so only the binary name differs. cloud-vinyl supports both lineages, selected per
VinylCache. We publish the Vinyl image because there is no other.

## Why the flavor is explicit

The operator could try to guess the lineage from an image name. It does not, because tags
and repository names are free-form, and a wrong guess fails late: a Vinyl cache with
sidecars built for Varnish starts, but `varnishlog` against a foreign shared memory log
prints nothing and reports no error. You get a cache that runs and no metrics or traces.

So the lineage is a field, and the lineage decides everything that depends on it: the
daemon command, the statistics tool of the exporter and the log tool of the tracer. A flavor
and version combination the catalog does not contain is rejected when you apply it.
Mixing lineages inside one pod is not supported.

## Why digests

Every daemon, exporter and tracer image of the catalog is pinned by digest in the runtime catalog. A
tag can move: docker-library rebuilds `varnish:9.1.0` for Debian fixes, and a mutable tag
can change what runs between two pod restarts. With digests, what runs is exactly what was
tested, and a moved tag cannot change it.

The digests are the multi-arch index digests. When a rebuild or a new version is released,
the catalog changes, and so does what your caches run, but only through a chart release.

## The Vinyl image

`ghcr.io/bluedynamics/vinyl-cache` is built from `Dockerfile.vinyl` in this repository on
`debian:trixie-slim`, from the release tarball verified by SHA-256, with jemalloc. It
includes `std`, `directors`, `purge`, `xkey` and `uuid`, and a C compiler, because
`vinyld` compiles VCL to C at runtime. It is published for amd64 and arm64.

Every build gets an immutable tag `<version>-r<N>`, for example `9.1.0-r1`, and the
moving tag `<version>` points at the newest revision. A rebuild for a security fix without
a new Vinyl release raises `N`. Pin the `-rN` tag if you set an image yourself, follow the
moving tag if you want rebuilds.

## Exporter and tracer images

The exporter and tracer images contain the statistics and log tools of one lineage and
version. Their tags name both: `varnish-exporter:1.8.3-varnish9.1.0`,
`varnish-exporter:1.8.3-vinyl9.1.0`. The tracer follows the operator release, with a
`-vinyl` suffix for Vinyl. The log tool cannot read across versions, so tracing needs an
exact version match. See {ref}`tracing-version-match`.

The runtime catalog ties the three images of one flavor and version together. That is its
purpose: you choose a lineage and version, and the combination has been tested.
