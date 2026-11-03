# Vinyl Cache flavor (1.1)

Issue: #106 (plan comment of 2026-10-25), with #59 as a prerequisite.
Status: draft for review, 2026-11-03.

## Why

The FOSS project formerly called Varnish Cache is now Vinyl Cache, at 9.1.0. Varnish
Software's docker-library `varnish` image follows the vendor lineage under the
same version numbers but different code. cloud-vinyl runs `varnish:9.1.0` today
(since #121) and cannot run Vinyl at all: Vinyl renames the binaries
(`vinyld`, `vinylstat`, `vinyllog`), the library (`libvinylapi.so.3`) and the
default paths, and publishes no container images.

1.1 makes both lineages first-class, selected per VinylCache. The project's own
name already points at Vinyl; supporting it is the natural direction, and it
removes the dependency on a single vendor's image.

## Goals

- A VinylCache selects its lineage explicitly. Every lineage-specific piece of
  the pod (daemon binary, sidecar images) follows from that one choice.
- We publish a Vinyl image that runs everything the generated VCL needs:
  `std`, `directors`, `purge`, `xkey`, and `uuid` for tracing; built with
  jemalloc so the `MALLOC_CONF` tuning from #114 applies.
- Exporter and tracer sidecars exist per lineage, version-matched to the daemon.
- Both lineages are proven in CI with an E2E run.
- Existing VinylCaches keep working unchanged after the operator upgrade, with
  no spec change and no pod restart caused by the flavor work itself.

## Non-goals

- Mixing lineages inside one pod. Whether one lineage's tools can read the
  other's VSM is untested; `varnishlog` against a foreign VSM prints nothing and
  reports no error.
- Guessing the lineage from the image name.
- Supporting EOL series (8.0 keeps working but is not a target).
- Narrowing RBAC, multi-version daemon support within a lineage beyond what the
  image tag gives, a conversion webhook.

## Anti-goals

- A silent misconfiguration: a VinylCache whose flavor and image disagree must
  fail visibly (pod CrashLoop with a clear message, or an admission error),
  never run with sidecars that silently read nothing.
- Breaking an existing VinylCache on upgrade.

## Facts established (measured, see #106 comment and the 2026-11-03 spike)

- Operator-facing protocols are unchanged between the lineages: CLI auth and
  commands (`vcl.list` layout, `vcl.inline`, `ban`), varnishstat JSON v1 with
  additive counters only, the VSL tag set.
- The operator already owns the full daemon command line (#121): `-F`, `-f`,
  `-n <workdir>`, `-a`, `-j none`, `-T`, `-S`, `-p feature=+http2`, `-s ...`.
  No image entrypoint is involved, so only the binary name differs.
- Spike 2026-11-03 (`debian:trixie`):
  - `vinyl-cache-9.1.0.tgz` from vinyl-cache.org builds with
    `--prefix=/usr --with-jemalloc`. `ldd vinyld` shows libjemalloc. It installs
    `vinyld`, `vinylstat`, `vinyllog`, `vinyladm`,
    `/usr/lib/libvinylapi.so.3`, `vinylapi.pc` and `vinyl.m4`. vmods go to
    `/usr/lib/vinyl-cache/vmods/`.
  - varnish-modules 0.29.0 builds against it unchanged (xkey included).
  - libvmod-uuid builds from `nigoroll/libvmod-uuid@9820da2`, the head of the
    open PR otto-de/libvmod-uuid#8, once `autoconf-archive` is installed.
  - A VCL importing `std, directors, purge, xkey, uuid` compiles, and
    `uuid.uuid_v4()` returns an id at runtime.
  - `vinyld` runs as UID 65532 with exactly the operator's command line,
    including `-n /var/lib/varnish/varnishd`, `-j none`, `-T`/`-S`,
    `-p feature=+http2` and `-s s0=malloc,...`. As UID 65532 against that
    workdir, `vinylstat -j` prints JSON v1 and `vinyllog -g request` sees the
    requests. The admin CLI with the secret file answers `vcl.list`.
- The otto-de exporter (v1.8.3) takes the same `-n` and `-web.listen-address`
  flags the operator passes today. It also has `-varnishstat-path`. Its
  `varnishstat -V` parser takes the first version number in the line, so
  `vinylstat (vinyl-cache-9.1.0 revision ...)` parses as 9.1.0. The exporter
  needs no code change for Vinyl.

## Design

### API

```yaml
spec:
  flavor: varnish   # or: vinyl. Default: varnish.
  image: ghcr.io/bluedynamics/vinyl-cache:9.1.0
```

- `spec.flavor`, an enum `varnish | vinyl`, defaulted to `varnish` by the CRD
  (so existing objects read back as `varnish` and nothing changes for them).
- Mutable. Changing it is a rolling restart like an image change; cache contents
  are lost per pod as with any restart. During the rollout, pods of both
  lineages run side by side; that is safe because peers only talk HTTP to each
  other.
- `spec.image` stays required and user-chosen. The operator cannot verify that
  the image matches the flavor at admission time; see "Mismatch detection".

### What the flavor decides

| | `varnish` | `vinyl` |
|---|---|---|
| daemon command | `varnishd` | `vinyld` |
| exporter default image | `ghcr.io/bluedynamics/varnish-exporter:<exp>-varnish<ver>` | `ghcr.io/bluedynamics/varnish-exporter:<exp>-vinyl<ver>` |
| tracer default image | operator setting for varnish | operator setting for vinyl |
| tracer log binary | `varnishlog` | `vinyllog` |

Everything else stays identical, including the workdir. The operator passes `-n`
explicitly to the daemon and to every sidecar, so the working directory is the
operator's choice and needs no lineage-specific path. It keeps today's
`/var/lib/varnish/varnishd` for both lineages, which means existing pods see no
change at all.

The flavor lives in one small table in the controller (a struct per flavor),
not in `if` chains spread over the pod builder.

### Mismatch detection

A flavor/image mismatch must not run silently:

- **Daemon:** the container command is the flavor's binary. A `vinyl` flavor on
  a `varnish:*` image fails to start with "executable file not found", which
  shows as CrashLoopBackOff and in the pod events. That is visible but not
  friendly.
- **Webhook warning (decided):** warn, never reject, when the last path
  segment of the image repository (`varnish` in `docker.io/library/varnish`)
  contains `varnish` while the flavor is `vinyl`, or contains `vinyl` while the
  flavor is `varnish`. Only the last segment, because organisation names such as
  `bluedynamics/vinyl-cache` or a registry host must not trigger it. This is a
  hint. The real guard is that the wrong binary does not exist in the image, so
  the pod crash-loops.
- **Status:** when the varnish container waits in CrashLoopBackOff and its last
  termination message mentions a missing executable, the operator reports a
  condition naming the flavor. Builder's call whether this is cheap enough for
  1.1. The warning above is the minimum.

### Our Vinyl image

`ghcr.io/bluedynamics/vinyl-cache:<vinyl-version>`, built from
`Dockerfile.vinyl` in this repository:

- Base `debian:trixie-slim`, the same Debian as the distroless sidecars.
- Vinyl from the release tarball, pinned by version and verified by SHA-256 in
  the Dockerfile. Built with `--with-jemalloc`.
- varnish-modules (xkey) and libvmod-uuid built in the same build stage, pinned
  by version or commit.
- Runtime stage: the binaries, `libvinylapi`, the vmods, runtime libraries
  (jemalloc, pcre2, libedit, ossp-uuid), a C compiler (vinyld compiles VCL to C
  at runtime and needs `cc`), and a non-root user 65532 matching the pod's
  security context.
- Multi-arch: amd64 and arm64, like every other image of the project.
- Published by its own workflow, triggered by changes to `Dockerfile.vinyl` and
  by manual dispatch.
- **Tags (decided 2026-11-03):** every build gets an immutable tag
  `<vinyl-version>-r<N>`, for example `9.1.0-r1`. The workflow refuses to
  overwrite an existing one, following #91. In addition, the moving tag
  `<vinyl-version>` always points at the newest revision. A rebuild for a
  Debian or jemalloc security fix without a new Vinyl release raises `N`. The
  docs and E2E fixtures name the `-rN` tag; users choose between pinning and
  following.

**uuid:** the upstream PR otto-de/libvmod-uuid#8 is open. We build from its head
commit, pinned by SHA, and switch to an upstream release once it is merged. The
alternative of making tracing work without uuid on Vinyl is out of scope.

### Sidecars

- **Exporter (#59 first):** `Dockerfile.exporter` builds from
  `otto-de/prometheus_varnish_exporter` at a release tag instead of
  jonnenauha 1.6.1. Metric names are unchanged (verified upstream in the #59
  discussion). The tag scheme stays `<exporter-version>-<lineage><version>`.
  A second build with the Vinyl image as `VARNISH_IMAGE` copies `vinylstat`
  and `libvinylapi.so.3`. The operator passes
  `-varnishstat-path /usr/bin/vinylstat` for the `vinyl` flavor. The soname
  guard from #91 becomes lineage-aware.
- **Tracer:** `Dockerfile.tracer` gets the same lineage build-arg. The tracer
  already takes the log binary from `VARNISHLOG_PATH`; the operator sets it
  per flavor. One operator-wide tracer image cannot serve two lineages, so
  there is a second image setting:
  - The existing `TRACER_IMAGE` env var and chart value `image.tracer` keep
    their meaning: the varnish lineage. A 1.0 values file keeps working.
  - New: env `TRACER_IMAGE_VINYL` and chart value `image.tracerVinyl`. Its
    repository defaults to the same one, and its tag to `<appVersion>-vinyl`.
  - Without the env var, outside the chart, the operator falls back to a
    built-in default, the same way `TRACER_IMAGE` does today.
  - The release workflow publishes both tracer variants under the same release
    tag. A chart release therefore never points at a tracer image that does
    not exist.
- The exporter image stays overridable per VinylCache, as today. The tracer
  image is not overridable per VinylCache in 1.1; the per-flavor operator
  setting is enough.
- **Versions within a lineage (decided):** each operator release pins one
  daemon version per flavor. That is the version its default sidecar images
  are built for and its E2E run tests: today `varnish 9.1.0`, and in 1.1 also
  `vinyl 9.1.0`. A cache running a different daemon version of the same lineage
  overrides the exporter image per cache, exactly as today with varnish. The
  tracer follows the operator release. The docs list the pinned versions per
  operator release. Deriving the version from the image tag was rejected,
  because tags are free-form.

### Release and E2E

- The release workflow builds the Vinyl tracer variant next to the existing
  one: `cloud-vinyl-tracer:<ver>` for varnish and `cloud-vinyl-tracer:<ver>-vinyl`
  for vinyl.
- **E2E (decided 2026-11-03):** on every PR the `fast` suite also runs against
  Vinyl, as a second matrix leg next to varnish. The leg pulls the published
  `vinyl-cache:<version>-r<N>` from GHCR (public, so fork PRs work too) and
  loads it into kind. Only a PR that changes `Dockerfile.vinyl` builds the
  image in the job instead. The fixtures parameterize flavor and image instead
  of being duplicated. This roughly doubles E2E wall time per PR, accepted so
  that both lineages are always proven.

### Docs

- How-to "Run Vinyl Cache": flavor and image, our image, and how to switch an
  existing cache.
- Reference: `spec.flavor` and the per-flavor table.
- Explanation: the two lineages and why the flavor is explicit.
- Upgrade notes for 1.1: no action needed. This flavor work doesn't touch
  existing caches, though other 1.1 changes do (#155 label rename).

## Order of work (each its own PR)

1. #59: exporter from the otto-de fork, varnish lineage only. This one is independent.
2. `Dockerfile.vinyl` and its publishing workflow, publishing
   `vinyl-cache:9.1.0`.
3. Exporter and tracer Vinyl variants (build-arg plus workflows).
4. Operator: `spec.flavor`, the flavor table, the per-flavor tracer image, and
   the webhook warning.
5. E2E leg on Vinyl.
6. Docs.

Steps 1 and 2 can run in parallel. Step 4 can start in parallel with 3 against
local images.

## Calls made while writing this (not discussed with the maintainer)

- The flavor field is called `spec.flavor` and is mutable; the default `varnish`
  is set by the CRD.
- The workdir stays `/var/lib/varnish/varnishd` for both lineages.
- The image is named `ghcr.io/bluedynamics/vinyl-cache` and based on
  `debian:trixie-slim`.
- uuid is built from the open upstream PR's head commit, pinned by SHA.
- The existing tracer settings keep the varnish meaning. The Vinyl tracer gets
  new settings.
- Each operator release pins one daemon version per flavor. Within a lineage,
  other versions override the exporter per cache.
- The mismatch warning only looks at the last path segment and is a hint, not a
  rejection.

## Left to the builder

- Exact Dockerfile layout and runtime package list (verify with
  `ldd vinyld` and a VCL compile in the final image, not the build stage).
- Exporter: flag vs. renamed binary for `vinylstat`.
- Status condition on flavor mismatch: in or out for 1.1.
- Names of the chart values for the Vinyl tracer image.
