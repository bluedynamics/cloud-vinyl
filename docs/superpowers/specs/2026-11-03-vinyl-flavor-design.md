# Vinyl Cache flavor (1.1)

Issue: #106 (plan comment of 2026-10-25), with #59 as a prerequisite.
Status: draft for review, 2026-11-03, revised 2026-11-04 (runtime catalog).

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

- A VinylCache names its lineage (`flavor`) and daemon `version`, nothing more.
  In the normal case nobody writes an image reference: the operator knows
  which daemon, exporter and tracer images belong to that flavor and version.
- Every image the operator deploys by default is pinned by digest, so what runs
  is exactly what was tested, and a moved tag cannot change it (#91; the same
  class of problem as a mutable `:latest`).
- New daemon versions and security rebuilds arrive with a chart release. No
  operator code change is needed for them.
- In an emergency, for example a security fix before our next release, an
  operator can replace an image themselves, cluster-wide or for one cache, without
  waiting for us.
- We publish a Vinyl image that runs everything the generated VCL needs:
  `std`, `directors`, `purge`, `xkey`, and `uuid` for tracing; built with
  jemalloc so the `MALLOC_CONF` tuning from #114 applies.
- Both lineages are proven in CI with an E2E run.
- Existing VinylCaches keep working unchanged after the operator upgrade.

## Non-goals

- Mixing lineages inside one pod. Whether one lineage's tools can read the
  other's VSM is untested; `varnishlog` against a foreign VSM prints nothing and
  reports no error.
- Guessing lineage or version from an image name.
- Per-namespace catalogs, or a catalog as its own resource type
  (`CacheRuntime`). Both were considered on 2026-11-04 and rejected as more
  mechanism than two lineages with one or two versions each need. Either can
  be added later without changing the VinylCache API.
- Supporting EOL series (8.0 keeps working but is not a target).
- Narrowing RBAC, a conversion webhook.

## Anti-goals

- A cache running sidecars that do not match its daemon and silently read
  nothing. An unknown flavor/version combination is rejected at admission.
- Breaking an existing VinylCache on upgrade.
- Making the normal case require knowledge of image names or digests.

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

The normal case. `flavor` defaults to `varnish`, `version` to the catalog's
default version for that flavor:

```yaml
spec:
  flavor: vinyl
  version: 9.1.0
```

An existing cache keeps working as written today:

```yaml
spec:
  image: varnish:9.1.0   # flavor defaults to varnish, version to its default
```

- `spec.flavor`: enum `varnish | vinyl`, defaulted to `varnish` by the CRD, so
  existing objects read back as `varnish`.
- `spec.version`: optional string. Empty means the catalog's default version
  for the flavor. Resolved at reconcile time, not written back, so an empty
  version follows the chart's default when the chart moves on (documented as
  such: pin `version` to stay put).
- `spec.image`: becomes optional. When set, it replaces only the **daemon**
  image of the resolved catalog entry. Sidecars still come from the catalog
  entry of `flavor`/`version`. This is the per-cache emergency override, and
  also what keeps existing caches unchanged. No warning for a reference without
  digest: every existing cache has one, and a warning on every apply would be
  noise. The docs recommend a digest.
- **`spec.image` set, `version` empty:** the sidecars come from the default
  version of the flavor. For a cache running another daemon version, for example
  `varnish:8.0.2`, that is the same situation as today: today's exporter and
  tracer are also built for 9.1.0 only, and such caches already override the
  exporter. So no regression, but the webhook now says it: it warns when `image`
  is set and `version` is empty, naming the version the sidecars are built for
  and suggesting to set `version`. Deriving the version from the tag was
  rejected (tags are free-form); requiring `version` with `image` would reject
  every existing cache.
- Changing `flavor` or `version` is a rolling restart like an image change
  today. During the rollout pods of both lineages can run side by side; that is
  safe because peers only talk HTTP to each other.

### The runtime catalog

A table from flavor and version to the three images, each with digest. It lives
in the chart values and is pre-filled by us with the combinations the release
was tested with:

```yaml
runtimes:
  varnish:
    default: "9.1.0"
    versions:
      "9.1.0":
        daemon:   docker.io/library/varnish:9.1.0@sha256:...
        exporter: ghcr.io/bluedynamics/varnish-exporter:1.8.3-varnish9.1.0@sha256:...
        tracer:   ghcr.io/bluedynamics/cloud-vinyl-tracer:<appVersion>@sha256:...
  vinyl:
    default: "9.1.0"
    versions:
      "9.1.0":
        daemon:   ghcr.io/bluedynamics/vinyl-cache:9.1.0-r1@sha256:...
        exporter: ghcr.io/bluedynamics/varnish-exporter:1.8.3-vinyl9.1.0@sha256:...
        tracer:   ghcr.io/bluedynamics/cloud-vinyl-tracer:<appVersion>-vinyl@sha256:...
```

- **Normal case:** nobody touches it. New versions and rebuilds come with the
  next chart release, which is an ordinary operator upgrade.
- **Emergency, cluster-wide:** override one entry in the values, for example
  `helm upgrade ... --set runtimes.vinyl.versions."9.1.0".daemon=...@sha256:...`.
  All caches on that flavor/version roll to it. Remove the override once our
  release with the fix is out.
- **Emergency, one cache:** `spec.image` (above).
- **Own builds or versions we do not ship:** an additional entry in the values.
- **Digests are enforced at release, not by the chart schema.** The schema
  accepts tags, because CI and E2E point catalog entries at images built in the
  same job, which have no registry digest, and because an operator may need a
  tag in an emergency. The release workflow refuses to package a chart whose
  catalog has an image without digest.
- **How the operator gets it (decided):** the chart renders the catalog into a
  ConfigMap in the operator namespace, mounted as a file into the operator pod.
  The operator reads it at startup and validates it; an invalid catalog stops
  the operator with a clear error instead of running with a partial table. The
  Deployment carries a checksum annotation of the catalog, so `helm upgrade`
  with a changed catalog restarts the operator, which then reconciles every
  cache. No informer is involved, which matters because #145 scopes the
  ConfigMap informer to objects with the cache-name label.
- **Digests in the repository and the release (decided):**
  - Daemon and exporter images are published by their own workflows before any
    release uses them. Their digests are committed in `values.yaml` on main,
    real ones, never placeholders. A `-rN` rebuild or a new version is a PR that
    updates the digest (by hand or by a bot PR from the image workflow, the
    builder's choice). A CI check resolves every committed digest against GHCR
    and fails on a mismatch with the tag, like `check-generated` does for code.
  - The tracer is built by the release workflow itself. The release job writes
    its digest into the chart before packaging. On main, the tracer entries
    carry the tag only.
  - E2E on a PR overlays the catalog with the images built in the job
    (operator, agent, tracer, and the Vinyl image when `Dockerfile.vinyl`
    changed), referenced by tag and loaded into kind. Daemon and exporter
    entries keep their committed digests and kind pulls them from GHCR directly
    (public), so no digest reference has to survive `kind load`.
- Agent and operator images stay as today (tag = appVersion). They are tied to
  the operator release, not to the daemon, and out of scope here.

### What the flavor decides beyond the images

| | `varnish` | `vinyl` |
|---|---|---|
| daemon command | `varnishd` | `vinyld` |
| exporter `-varnishstat-path` | default (`varnishstat`) | `/usr/bin/vinylstat` |
| tracer `VARNISHLOG_PATH` | `varnishlog` | `vinyllog` |

Everything else stays identical, including the workdir. The operator passes `-n`
explicitly to the daemon and to every sidecar, so the working directory is the
operator's choice. It keeps today's `/var/lib/varnish/varnishd` for both
lineages, so existing pods see no change.

The flavor lives in one small table in the controller (a struct per flavor),
not in `if` chains spread over the pod builder.

### Validation and mismatch detection

- **Admission:** the webhook runs in the operator and has the catalog. It
  rejects a flavor/version combination the catalog does not contain, naming the
  versions it does contain.
- **Catalog changed under a cache:** if an entry a cache uses disappears (values
  edited), reconcile sets the condition `RuntimeResolved=False` on the cache and
  leaves its StatefulSet as it is, instead of failing half-way.
- **Status:** `status.runtime` shows what actually runs: resolved flavor,
  version and the three image references. An empty `spec.version` that follows
  the chart's default is visible there, and in a printer column for the
  version.
- **`spec.image` override of the wrong lineage:** a `vinyl` cache with a
  `varnish:*` image fails to start ("executable file not found"), visible as
  CrashLoopBackOff. In addition the webhook warns when the last path segment of
  the override's repository names the other lineage. Only the last segment, so
  `bluedynamics/vinyl-cache` or a registry host does not trigger it.
- **Exporter override:** `spec.monitoring.exporter.image` keeps working as today
  and wins over the catalog's exporter.

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

- **Exporter (#59, done in #165):** built from the otto-de fork. The tag scheme
  stays `<exporter-version>-<lineage><version>`. A second build with the Vinyl
  image as `VARNISH_IMAGE` bundles `vinylstat` and `libvinylapi.so.3`; the
  soname guard from #91 becomes lineage-aware.
- **Tracer:** `Dockerfile.tracer` gets the same lineage build-arg. The release
  workflow publishes `cloud-vinyl-tracer:<ver>` (varnish) and
  `cloud-vinyl-tracer:<ver>-vinyl` (vinyl). The catalog replaces the
  `TRACER_IMAGE` env var and the operator's `:latest` fallback; without a
  catalog the operator does not start. The chart value `image.tracer` stays for
  one minor release as a deprecated alias that overrides the tracer of the
  **varnish** entries only (what it meant in 1.0), with a deprecation note in
  the chart's NOTES output.

### Release and E2E

- **E2E (decided 2026-11-03):** on every PR the `fast` suite also runs against
  Vinyl, as a second matrix leg next to varnish. The leg pulls the published
  `vinyl-cache:<version>-r<N>` from GHCR (public, so fork PRs work too) and
  loads it into kind. Only a PR that changes `Dockerfile.vinyl` builds the
  image in the job instead. The fixtures select `flavor` and `version` instead
  of image names. This roughly doubles E2E wall time per PR, accepted so that
  both lineages are always proven.
- E2E runs against the catalog the chart ships, so the tested combination and
  the shipped one are the same by construction.

### Docs

- How-to "Run Vinyl Cache": `flavor` and `version`, switching an existing cache.
- How-to "Replace an image in an emergency": cluster-wide via values, per cache
  via `spec.image`, and removing the override afterwards.
- Reference: `spec.flavor`, `spec.version`, `spec.image` (now an override), the
  `runtimes` chart values, the per-flavor table.
- Explanation: the two lineages, why the flavor is explicit, why digests.
- Upgrade notes for 1.1: existing caches need no spec change. Their pods roll
  once, as with every operator release, and this time the exporter also moves
  from 1.6.1 to 1.8.3 (same metric names) and all sidecars are pinned by digest.
  A cache without `version` follows the chart's default version on later
  upgrades. A catalog change rolls every affected cache; each StatefulSet rolls
  one pod at a time, as on every operator upgrade, with no extra throttling
  across caches.

## Order of work (each its own PR)

1. Done: #59, exporter from the otto-de fork (#165).
2. `Dockerfile.vinyl` and its publishing workflow, publishing
   `vinyl-cache:9.1.0-r1`.
3. Exporter and tracer Vinyl variants (build-arg plus workflows).
4. Operator: `spec.flavor`/`spec.version`, optional `spec.image`, the catalog
   (chart values, ConfigMap, loading, validation), the flavor table, webhook
   rules.
5. Release workflow: digests into the chart's catalog.
6. E2E leg on Vinyl.
7. Docs.

Step 2 and the operator part of 4 can run in parallel.

## Calls made while writing this (not discussed with the maintainer)

- Field names `spec.flavor` and `spec.version`; `version` empty means "the
  chart's default for the flavor" and is not written back.
- The catalog reaches the operator as a mounted file read at startup, with a
  restart on change, not as a watched object.
- An invalid catalog stops the operator; an unknown combination is rejected at
  admission; a vanished entry sets a condition and freezes the StatefulSet.
- `spec.image` without digest gets no warning; `spec.image` without `version`
  does.
- Digests are enforced at release time, not by the chart schema; daemon and
  exporter digests are committed on main and checked in CI, the tracer's is
  written at release.
- `status.runtime` and the condition `RuntimeResolved`.
- `image.tracer` stays one minor release as a deprecated alias.
- The workdir stays `/var/lib/varnish/varnishd` for both lineages.
- The image is named `ghcr.io/bluedynamics/vinyl-cache` and based on
  `debian:trixie-slim`; uuid is built from the open upstream PR's head commit,
  pinned by SHA.

## Left to the builder

- Exact Dockerfile layout and runtime package list (verify with
  `ldd vinyld` and a VCL compile in the final image, not the build stage).
- How the release workflow writes digests into the chart.
- Exact chart value names below `runtimes`.
