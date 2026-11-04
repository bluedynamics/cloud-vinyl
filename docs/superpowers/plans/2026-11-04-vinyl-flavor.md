# Vinyl Cache Flavor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A VinylCache names `flavor` and `version`; the operator deploys the matching daemon, exporter and tracer images, digest-pinned, from a chart-shipped runtime catalog, for both the varnish and the Vinyl Cache lineage.

**Architecture:** Five pull requests in order (A to F below, E optional to merge with C). A and B produce images in GHCR; C adds the API fields, the `internal/catalog` package, the per-flavor pod differences and the chart's catalog ConfigMap; D adds the Vinyl E2E leg; E makes the release write and enforce digests; F documents it. The operator reads the catalog file once at startup; a checksum annotation restarts it when the catalog changes.

**Tech Stack:** Go 1.26, controller-runtime, kubebuilder markers, Helm 3 with helm-unittest, GitHub Actions, Docker Buildx, chainsaw, kind.

**Spec:** `docs/superpowers/specs/2026-11-03-vinyl-flavor-design.md` (approved 2026-11-04). Issue #106.

## Global Constraints

- Work only in worktrees `sources/cloud-vinyl-wt/<branch>`, never in `sources/cloud-vinyl/`. One branch and one PR per lettered part.
- Commits, PRs and issue comments end with `Assisted-by: Claude Opus 5.5` (or the model that did the work). Never `Co-Authored-By`, never a noreply address. PRs in English.
- Never force-push. Rebase only branches that were not pushed yet; otherwise merge `origin/main` into the branch.
- After any change under `api/` or a `+kubebuilder` marker: `make manifests generate`, commit the result. CI runs `make check-generated`.
- Every new `hack/*.sh` check: break it on purpose and confirm it goes red before relying on it (CLAUDE.md).
- No control-plane assertions in E2E. chainsaw must not run `curl`/`wget`.
- Image names: `ghcr.io/bluedynamics/vinyl-cache:9.1.0-r1` (immutable) and moving `:9.1.0`; exporter `ghcr.io/bluedynamics/varnish-exporter:1.8.3-vinyl9.1.0`; tracer `ghcr.io/bluedynamics/cloud-vinyl-tracer:<ver>-vinyl`.
- Vinyl sources: `https://vinyl-cache.org/downloads/vinyl-cache-9.1.0.tgz` (SHA-256 verified in the Dockerfile), varnish-modules `0.29.0`, libvmod-uuid `nigoroll/libvmod-uuid@9820da2d7396520bf2ce7723a5317ee55dbaf416`.
- The workdir stays `/var/lib/varnish/varnishd` for both flavors.
- Per flavor: daemon command `varnishd` / `vinyld`; exporter `-varnishstat-path` default / `/usr/bin/vinylstat`; tracer `VARNISHLOG_PATH` `varnishlog` / `vinyllog`.
- Digests are multi-arch index digests (`docker buildx imagetools inspect <ref>`), never a per-arch manifest digest.

## Review Focus

1. **Deleting a cache whose catalog entry vanished.** Finalizer removal is an UPDATE through the validating webhook. If the runtime check rejects updates of an unchanged flavor/version, the cache can never be deleted. Expected: the runtime check only rejects on create, or when `flavor` or `version` changed. Pinned in Task C5.
2. **An existing 1.0 cache with only `spec.image`, no `flavor`, no `version`.** Expected: admitted, flavor `varnish`, version = catalog default, daemon = its image, sidecars from the default entry, one warning. Pinned in Tasks C4 and C5.
3. **Flavor left empty in a Go struct** (fake clients, unit tests, objects predating the CRD default). Expected: treated as `varnish` everywhere (catalog, traits, webhook). Pinned in Tasks C2 and C3.
4. **`helm upgrade` with an unchanged catalog.** Expected: no operator restart, so the checksum must be stable across renders. And a changed entry must change it. Pinned in Task C6.
5. **The upstream `varnish:9.1.0` tag moves** (docker-library rebuilds for Debian fixes). Expected: CI stays green, with a warning that a newer build exists; a digest that does not exist in the repository fails. Pinned in Task C7.

---

## Part A: Vinyl image (PR A, step 2 of the spec)

Branch `feat/vinyl-image`. Independent of the open PRs; can start at once.

### Task A1: `Dockerfile.vinyl` with a smoke test

**Files:**
- Create: `Dockerfile.vinyl`
- Create: `hack/smoke-vinyl-image.sh`
- Modify: `Makefile` (target `docker-build-vinyl`)

**Interfaces:**
- Produces: `Dockerfile.vinyl` with top-level `ARG VINYL_VERSION=9.1.0` and `ARG VINYL_REVISION=1`, which Task A2 reads to derive the tag; image contents `/usr/sbin/vinyld` (or wherever `make install` puts it, on `PATH`), `/usr/bin/vinylstat`, `/usr/bin/vinyllog`, `/usr/lib/libvinylapi.so.3*`, vmods in `/usr/lib/vinyl-cache/vmods/`. Tasks B1 and B2 copy `vinylstat`, `vinyllog` and `libvinylapi.so.3*` from it.
- Produces: `hack/smoke-vinyl-image.sh <image>`, exit 0 on a good image.

- [ ] **Step 1: Write `hack/smoke-vinyl-image.sh`.** It runs the image as UID 65532 and checks, each failing with a clear message:
  - `vinyld -V` prints `vinyl-cache-9.1.0`;
  - `ldd $(command -v vinyld)` lists `libjemalloc`;
  - `vinyld -C -f /tmp/smoke.vcl` succeeds for a VCL that imports `std`, `directors`, `purge`, `xkey`, `uuid` and calls `uuid.uuid_v4()` in `vcl_recv` (`-C` compiles and exits; this also proves `cc` is present);
  - `vinylstat -V` and `vinyllog -V` run (no missing shared library).
- [ ] **Step 2: Run it against a non-Vinyl image to prove the failure path.** Run: `bash hack/smoke-vinyl-image.sh varnish:9.1.0`. Expected: non-zero exit with a message naming `vinyld`.
- [ ] **Step 3: Write `Dockerfile.vinyl`.** Build stage `debian:trixie` with the spike's packages (build-essential, autoconf, automake, autoconf-archive, libtool, pkg-config, python3-docutils, python3-sphinx, libedit-dev, libjemalloc-dev, libncurses-dev, libpcre2-dev, libossp-uuid-dev, git, curl). Download the tarball and verify it with `sha256sum -c` against a pinned hash (compute it once from the official tarball, put it in `ARG VINYL_SHA256`). `./configure --prefix=/usr --with-jemalloc`. Build varnish-modules 0.29.0 (also verify a pinned SHA-256) and libvmod-uuid at the pinned commit. Runtime stage `debian:trixie-slim`: the runtime libraries (`ldd` of `vinyld`, `vinylstat`, `vinyllog` and of each vmod decides the list), `gcc` and `libc6-dev` for VCL compilation, `useradd -u 65532`, `USER 65532:65532`. OCI labels as in `Dockerfile.tracer`. A header comment says what the image is, why it exists (no upstream images), and how `VINYL_REVISION` works (raise it for a rebuild without a new Vinyl release).
- [ ] **Step 4: Build and smoke-test.** Run: `docker build -f Dockerfile.vinyl -t vinyl-cache:smoke . && bash hack/smoke-vinyl-image.sh vinyl-cache:smoke`. Expected: exit 0. Check that the operator's command line also starts: `docker run --rm --user 65532 vinyl-cache:smoke vinyld -F -f '' -n /var/lib/varnish/varnishd -a :8080 -j none -T 127.0.0.1:6082 -p feature=+http2 -s s0=malloc,64m` stays up (stop it with a timeout). If `/var/lib/varnish` is not writable for 65532, create it in the Dockerfile owned by 65532.
- [ ] **Step 5: Add `docker-build-vinyl` to the Makefile** next to `docker-build-tracer`.
- [ ] **Step 6: Commit.** `feat(image): build a Vinyl Cache 9.1.0 image (#106)`.

### Task A2: Publish workflow

**Files:**
- Create: `.github/workflows/release-vinyl.yml`

**Interfaces:**
- Consumes: `ARG VINYL_VERSION` and `ARG VINYL_REVISION` in `Dockerfile.vinyl`; `hack/smoke-vinyl-image.sh`.
- Produces: `ghcr.io/bluedynamics/vinyl-cache:9.1.0-r1` (multi-arch) and moving `:9.1.0`.

- [ ] **Step 1: Write the workflow by the pattern of `release-exporter.yml`.** Triggers: push to main on `Dockerfile.vinyl` or this workflow, and `workflow_dispatch` (no overwrite input: a revision is never overwritten, raise `VINYL_REVISION` instead). Jobs:
  1. `guard-tag`: reads both ARGs with `sed -n 's/^ARG VINYL_VERSION=//p'`, tag `${VERSION}-r${REVISION}`, refuses with `::error::` when the manifest exists in GHCR (same token/curl check as the exporter workflow).
  2. `docker-build`: amd64 on `ubuntu-latest`, arm64 on `ubuntu-24.04-arm`. Build with `load: true` first, run `hack/smoke-vinyl-image.sh` on it, then push `<tag>-linux-<arch>`.
  3. `docker-manifest`: creates `:<version>-r<N>` and moves `:<version>`.
- [ ] **Step 2: Lint the workflow.** Run: `docker run --rm -v "$PWD:/repo" -w /repo rhysd/actionlint:latest`. Expected: no findings for the new file.
- [ ] **Step 3: Commit.** `ci(image): publish vinyl-cache:<version>-r<N> with an immutable tag (#106)`.
- [ ] **Step 4: Open PR A** with a plan comment on #106 first (issue workflow). After merge the workflow publishes; confirm with `docker buildx imagetools inspect ghcr.io/bluedynamics/vinyl-cache:9.1.0-r1`. Make the GHCR package public (ask the maintainer if the workflow cannot).

---

## Part B: Exporter and tracer Vinyl variants (PR B, step 3)

Branch `feat/sidecar-vinyl-variants`, from main **after #165 and PR A are merged and `vinyl-cache:9.1.0-r1` exists**.

### Task B1: Flavor-aware `Dockerfile.exporter` and workflow

**Files:**
- Modify: `Dockerfile.exporter`, `.github/workflows/release-exporter.yml`

**Interfaces:**
- Consumes: `ghcr.io/bluedynamics/vinyl-cache:9.1.0-r1` (Part A).
- Produces: `varnish-exporter:1.8.3-vinyl9.1.0`, which contains `/usr/bin/vinylstat` and `libvinylapi.so.3`. The operator passes `-varnishstat-path /usr/bin/vinylstat` (Task C3).

- [ ] **Step 1: Restructure the Dockerfile.** Global `ARG FLAVOR=varnish`, `ARG VARNISH_IMAGE=varnish:9.1.0`, `ARG VINYL_IMAGE=ghcr.io/bluedynamics/vinyl-cache:9.1.0-r1`. Two source stages `src-varnish` (`FROM ${VARNISH_IMAGE}`) and `src-vinyl` (`FROM ${VINYL_IMAGE}`); each runs its soname guard (`libvarnishapi.so.3` / `libvinylapi.so.3`) and stages its stat binary, its API library and the libs it needs into `/export/usr/...`. Then `FROM src-${FLAVOR} AS daemon` and one `COPY --from=daemon /export/ /` in the final stage. Only the selected source stage is built, which the header comment must say. Keep the #91 guard comments, made flavor-aware.
- [ ] **Step 2: Build both variants and run them.** Run, for each flavor: `docker build -f Dockerfile.exporter --build-arg FLAVOR=<f> -t exp:<f> .`. Then start a daemon of the same flavor with a shared `-n` workdir volume (`docker run -v wd:/var/lib/varnish ...`, varnishd resp. vinyld with the operator's arguments) and run the exporter against it: `docker run --rm -v wd:/var/lib/varnish:ro exp:vinyl -n /var/lib/varnish/varnishd -varnishstat-path /usr/bin/vinylstat -test`. Expected: exit 0 for both flavors. For vinyl without `-varnishstat-path`: non-zero (the default path does not exist), which proves the flag matters.
- [ ] **Step 3: Workflow matrix over flavor.** `guard-tag` becomes a per-flavor matrix. Tag `${EXPORTER_VERSION}-${flavor}${daemon_version}`, where the daemon version comes from the image tag: `varnish:9.1.0` gives `9.1.0`, `vinyl-cache:9.1.0-r1` gives `9.1.0` (strip `-r<N>`). **An existing tag is skipped with `::notice::`, not failed** (decided here, see "Calls" below), so that adding the vinyl variant does not fail on the already published varnish tag. `overwrite_tag` on dispatch keeps working per flavor. Build and manifest jobs run only for flavors the guard marked as to publish.
- [ ] **Step 4: actionlint**, as in A2.
- [ ] **Step 5: Commit.** `feat(exporter): build a Vinyl variant bundling vinylstat (#106)`.

### Task B2: Flavor-aware `Dockerfile.tracer` and release build

**Files:**
- Modify: `Dockerfile.tracer`, `.github/workflows/release.yml` (docker-build and docker-manifest jobs only), `Makefile` (`docker-build-tracer` takes `FLAVOR`)

**Interfaces:**
- Produces: release images `cloud-vinyl-tracer:<ver>` and `cloud-vinyl-tracer:<ver>-vinyl`; the vinyl one has `/usr/bin/vinyllog`. The operator sets `VARNISHLOG_PATH` (Task C3).

- [ ] **Step 1: Same stage pattern as B1** (`src-varnish` / `src-vinyl`, staged `/export`), copying the log binary, the API library and `libpcre2-8.so.0`. The vinyl variant contains `vinyllog` only, no `varnishlog` alias.
- [ ] **Step 2: Build both and check the binary runs.** Run: `docker build -f Dockerfile.tracer --build-arg FLAVOR=vinyl -t tr:vinyl . && docker run --rm --entrypoint /usr/bin/vinyllog tr:vinyl -V`. Expected: prints `vinyl-cache-9.1.0`. Same for varnish with `varnishlog`.
- [ ] **Step 3: release.yml.** Add the two vinyl tracer matrix entries (`build-args: FLAVOR=vinyl`, a `tag-suffix: -vinyl` matrix field used in the per-arch tag). In `docker-manifest` add a tracer-vinyl entry that builds `<ver>-vinyl` from its two per-arch tags and **never** moves `:latest` (decided here). Keep the pre-release rule for the other images.
- [ ] **Step 4: actionlint**, as in A2.
- [ ] **Step 5: Commit and open PR B.** `feat(tracer): build a Vinyl variant reading vinyllog (#106)`. After merge, the exporter workflow publishes `1.8.3-vinyl9.1.0`; confirm with `imagetools inspect`.

---

## Part C: Operator, catalog and chart (PR C, step 4)

Branch `feat/runtime-catalog`, from main **after #163, #165, #166 and #167 are merged** (they touch `statefulset.go`, `main.go` and the chart). Tasks C1 to C6 need no published images; C7 fills in real digests and needs Parts A and B published.

### Task C1: API fields, status and printer columns

**Files:**
- Modify: `api/v1/vinylcache_types.go`; regenerated `zz_generated.deepcopy.go`, CRDs in `config/` and `charts/cloud-vinyl/crds/`

**Interfaces:**
- Produces (package `api/v1`):
  - `type Flavor string`, `FlavorVarnish Flavor = "varnish"`, `FlavorVinyl Flavor = "vinyl"`.
  - `VinylCacheSpec.Flavor Flavor` (`json:"flavor,omitempty"`, `+kubebuilder:validation:Enum=varnish;vinyl`, `+kubebuilder:default=varnish`, `+optional`).
  - `VinylCacheSpec.Version string` (`json:"version,omitempty"`, `+optional`).
  - `VinylCacheSpec.Image string` becomes `json:"image,omitempty"` and `+optional`; keep `MinLength=1`.
  - `type RuntimeStatus struct { Flavor Flavor; Version string; Daemon string; Exporter string (omitempty); Tracer string (omitempty) }`, JSON names lower camel case.
  - `VinylCacheStatus.Runtime *RuntimeStatus` (`json:"runtime,omitempty"`).
  - `ConditionRuntimeResolved = "RuntimeResolved"`.
  - Printer columns `Flavor` (`.status.runtime.flavor`) and `Version` (`.status.runtime.version`), before `Age`.

- [ ] **Step 1: Write the failing CRD test** in `api/v1alpha1/crd_test.go` (it already parses the CRD): `TestCRDFlavorDefaultsToVarnish` asserts the v1 schema has `spec.properties.flavor.default == "varnish"`, `enum == [varnish vinyl]`, and that `image` is no longer in `spec.required`.
- [ ] **Step 2: Run it.** Run: `go test ./api/...`. Expected: FAIL (no `flavor` property).
- [ ] **Step 3: Add the fields and markers**, with doc comments that say what an empty `version` means (the catalog default, not written back) and that `image` replaces only the daemon image. Run `make manifests generate`.
- [ ] **Step 4: Run tests.** Run: `go test ./api/... && make check-generated`. Expected: PASS; `TestCRDVersionsShareOneSchema` still passes.
- [ ] **Step 5: Commit.** `feat(api): spec.flavor, spec.version, optional spec.image, status.runtime (#106)`.

### Task C2: `internal/catalog` package

**Files:**
- Create: `internal/catalog/catalog.go`, `internal/catalog/catalog_test.go`

**Interfaces:**
- Consumes: `vinylv1.Flavor` and its constants (C1).
- Produces:
  - `type Images struct { Daemon, Exporter, Tracer string }` (YAML/JSON keys `daemon`, `exporter`, `tracer`).
  - `type Runtime struct { Flavor vinylv1.Flavor; Version string; Images }`.
  - `type Catalog struct` (unexported fields).
  - `func Parse(data []byte) (*Catalog, error)`, `func Load(path string) (*Catalog, error)`.
  - `func (c *Catalog) Resolve(f vinylv1.Flavor, version string) (Runtime, error)`. Empty `f` means `FlavorVarnish`; empty `version` means the flavor's default; the returned `Runtime` carries the resolved, non-empty version. Errors wrap `ErrNotInCatalog`.
  - `func (c *Catalog) Versions(f vinylv1.Flavor) []string`, sorted.
  - `var ErrNotInCatalog = errors.New("not in the runtime catalog")`.
- File format (what the chart renders, Task C6):
  ```yaml
  varnish:
    default: "9.1.0"
    versions:
      "9.1.0": {daemon: ..., exporter: ..., tracer: ...}
  vinyl: ...
  ```

- [ ] **Step 1: Write the failing tests** (table-driven where natural):
  - `TestParse_Valid`: the two-flavor example parses; `Resolve(FlavorVinyl, "9.1.0")` returns its three images.
  - `TestResolve_EmptyVersionUsesDefault`: `Resolve(FlavorVarnish, "")` returns `Version == "9.1.0"`.
  - `TestResolve_EmptyFlavorIsVarnish`: `Resolve("", "")` equals `Resolve(FlavorVarnish, "")`.
  - `TestResolve_Unknown`: unknown version and a flavor missing from the catalog both return an error matching `errors.Is(err, ErrNotInCatalog)`; the message names the available versions.
  - `TestParse_Rejects`, one case each, error message checked with `Contains`: unknown flavor key (`"bogus"`); unknown field (strict decoding, `sigs.k8s.io/yaml.UnmarshalStrict`); a flavor without versions; `default` not among `versions`; an empty `daemon`, `exporter` or `tracer`; an image with whitespace; an empty document.
  - `TestLoad_MissingFile`: error names the path.
- [ ] **Step 2: Run them.** Run: `go test ./internal/catalog/`. Expected: FAIL (package does not exist).
- [ ] **Step 3: Implement.** One flavor alone is a valid catalog (`varnish` is not mandatory). No image reference library; the checks above are enough.
- [ ] **Step 4: Run tests.** Expected: PASS.
- [ ] **Step 5: Commit.** `feat(catalog): parse and resolve the runtime catalog (#106)`.

### Task C3: Flavor traits and the pod builder

**Files:**
- Create: `internal/controller/flavor.go`, `internal/controller/flavor_test.go`
- Modify: `internal/controller/statefulset.go`, `internal/controller/statefulset_test.go`

**Interfaces:**
- Consumes: `catalog.Runtime` (C2).
- Produces:
  - `type flavorTraits struct { daemonCommand, varnishstatPath, varnishlogPath string }` (`varnishstatPath` empty = exporter default).
  - `func traitsFor(f vinylv1.Flavor) flavorTraits` (empty = varnish) from one `map[vinylv1.Flavor]flavorTraits` with the values from Global Constraints.
  - `func effectiveRuntime(vc *vinylv1.VinylCache, rt catalog.Runtime) catalog.Runtime`: `spec.image` replaces `Daemon`; `spec.monitoring.exporter.image` (repository, tag defaulting to `latest`, as today) replaces `Exporter`. Moves the override logic out of `buildExporterContainer`.
  - `reconcileStatefulSet(ctx, vc, rt catalog.Runtime) error` (rt is already effective); `buildExporterContainer(exp *vinylv1.ExporterSpec, image string, t flavorTraits)`; `buildTracerContainer(vc, image string, t flavorTraits)`.
  - Removed: `defaultExporterImage`, `defaultTracerImage`, the `TRACER_IMAGE` lookup. The `AGENT_IMAGE` fallback becomes `rt.Daemon` instead of `vc.Spec.Image`.

- [ ] **Step 1: Write the failing tests.**
  - `flavor_test.go`: `TestTraitsFor` (varnish, vinyl, empty = varnish); `TestEffectiveRuntime` covering no overrides, `spec.image` only (exporter and tracer unchanged), exporter override only, both.
  - `statefulset_test.go`, via the existing `varnishContainer(t, ss)` helper and container lookups:
    - `TestStatefulSet_VinylFlavor`: daemon `Command == []string{"vinyld"}`, image = `rt.Daemon`; exporter `Args` contain `-varnishstat-path`, `/usr/bin/vinylstat` adjacent; tracer env `VARNISHLOG_PATH=vinyllog`.
    - `TestStatefulSet_VarnishFlavor`: `varnishd`, no `-varnishstat-path`, `VARNISHLOG_PATH=varnishlog`.
    - `TestStatefulSet_WorkdirSameForBothFlavors`: `-n /var/lib/varnish/varnishd` in daemon and exporter args, `VARNISH_WORKDIR` in tracer, for both flavors.
  - Update existing tests that expected `defaultExporterImage` or the `TRACER_IMAGE` env to use images from a package-level test runtime (`testRuntime` in a `_test.go` helper).
- [ ] **Step 2: Run them.** Run: `go test ./internal/controller/ -run 'Traits|EffectiveRuntime|StatefulSet'`. Expected: FAIL to compile.
- [ ] **Step 3: Implement.** The varnish container name stays `varnish` for both flavors (renaming it would roll pods and break `kubectl logs -c varnish` habits).
- [ ] **Step 4: Run the controller unit tests.** Run: `go test ./internal/controller/`. Expected: PASS.
- [ ] **Step 5: Commit.** `feat(controller): per-flavor daemon, exporter and tracer settings (#106)`.

### Task C4: Resolve in Reconcile, condition and status

**Files:**
- Modify: `internal/controller/vinylcache_controller.go`, `internal/controller/status.go`
- Test: `internal/controller/runtime_resolve_test.go` (new)

**Interfaces:**
- Consumes: `catalog.Catalog.Resolve`, `effectiveRuntime`, `reconcileStatefulSet(ctx, vc, rt)`.
- Produces: `VinylCacheReconciler.Catalog *catalog.Catalog` (main.go sets it in C6). Condition `RuntimeResolved` with reasons `Resolved` / `NotInCatalog`.

- [ ] **Step 1: Write the failing tests** (fake client, as the existing reconcile tests do):
  - `TestReconcile_SetsRuntimeStatus`: a cache with only `spec.image: varnish:8.0.2` and no flavor/version reconciles; persisted `status.runtime` = `{varnish, 9.1.0, varnish:8.0.2, <catalog exporter>, <catalog tracer if tracing>}`; condition `RuntimeResolved=True`. Exporter/tracer fields are empty when the sidecar is disabled.
  - `TestReconcile_VanishedEntryFreezesStatefulSet`: reconcile with a catalog containing `vinyl 9.1.0`, then swap `r.Catalog` for one without it and reconcile again. The StatefulSet's pod template is unchanged; the persisted cache has `RuntimeResolved=False`, reason `NotInCatalog`, message naming flavor and version; Reconcile returns no error (no hot error loop).
- [ ] **Step 2: Run them.** Expected: FAIL.
- [ ] **Step 3: Implement.** Resolve right before step 5 of `Reconcile`. On success: set `status.runtime` and the condition, call `reconcileStatefulSet(ctx, vc, effectiveRuntime(vc, rt))`. On `ErrNotInCatalog`: set the condition, log, skip only the StatefulSet, continue the rest of the reconcile. Make sure the condition is persisted on that path even when a later step returns early (debounce); write status there if needed. A nil `Catalog` is a programming error: return an error, add no fallback.
- [ ] **Step 4: Update every test that builds a `VinylCacheReconciler`** to set `Catalog: testCatalog(t)` (shared helper in a `_test.go` file). Run: `go test ./internal/controller/`. Then `make test-int`; the controller envtest suite needs the catalog too. Expected: PASS.
- [ ] **Step 5: Commit.** `feat(controller): resolve the runtime per cache, RuntimeResolved condition (#106)`.

### Task C5: Webhook rules

**Files:**
- Create: `internal/webhook/runtime.go`, `internal/webhook/runtime_test.go`
- Modify: `internal/webhook/v1/vinylcache_webhook.go`, `internal/webhook/v1/webhook_suite_test.go`

**Interfaces:**
- Consumes: `catalog.Catalog`.
- Produces:
  - `func ValidateRuntime(old, vc *vinylv1.VinylCache, cat *catalog.Catalog) (admission.Warnings, []string)`; `old` is nil on create.
  - `VinylCacheCustomValidator{Catalog *catalog.Catalog}`; `SetupVinylCacheWebhookWithManager(mgr ctrl.Manager, cat *catalog.Catalog) error`. Create and Update merge `ValidateRuntime` into the existing results.

- [ ] **Step 1: Write the failing tests** (`runtime_test.go`):
  - `TestValidateRuntime_UnknownVersionRejectedOnCreate`: error contains `flavor "vinyl" version "9.0.0" is not in the runtime catalog` and `9.1.0`.
  - `TestValidateRuntime_UnchangedRuntimeAllowedOnUpdate`: old and new share a vanished version, only a label differs → no error. Same with `DeletionTimestamp` set and finalizers removed (Review Focus 1).
  - `TestValidateRuntime_ChangedVersionRejectedOnUpdate`.
  - `TestValidateRuntime_ImageWithoutVersionWarns`: one warning containing `spec.version` and `9.1.0`; no warning when `version` is set; no warning for `image` without digest.
  - `TestValidateRuntime_LineageMismatchWarns`, table: flavor vinyl + `varnish:9.1.0` warns; flavor varnish + `ghcr.io/bluedynamics/vinyl-cache:9.1.0-r1` warns; flavor vinyl + `ghcr.io/bluedynamics/vinyl-cache:9.1.0-r1` does not; flavor vinyl + `varnish-mirror.example.com/team/vinyl-cache:1` does not (host and org are ignored); a digest-only reference `.../varnish@sha256:...` with flavor vinyl warns.
  - `TestValidateRuntime_LegacyCacheAdmitted`: only `spec.image`, empty flavor and version → no error, exactly the image-without-version warning.
- [ ] **Step 2: Run them.** Run: `go test ./internal/webhook/...`. Expected: FAIL.
- [ ] **Step 3: Implement.** The lineage check takes the last path segment of the repository (strip `@digest`, then `:tag`), lowercased: warn when flavor vinyl and it contains `varnish`, or flavor varnish and it contains `vinyl`. Warning texts state what happens (the pod will not start / the sidecars are built for X) and what to do.
- [ ] **Step 4: Wire it.** The webhook envtest suite passes a test catalog. Add one envtest `It`: creating a cache with an unknown version is rejected by the webhook (proves the wiring, not the rule). Run: `make test-int`. Expected: PASS.
- [ ] **Step 5: Commit.** `feat(webhook): validate flavor and version against the runtime catalog (#106)`.

### Task C6: Catalog in the chart, operator startup

**Files:**
- Modify: `cmd/operator/main.go`, `charts/cloud-vinyl/values.yaml`, `charts/cloud-vinyl/values.schema.json`, `charts/cloud-vinyl/templates/deployment.yaml`, `charts/cloud-vinyl/templates/_helpers.tpl`, `charts/cloud-vinyl/templates/NOTES.txt`, `e2e/setup/install-operator.sh`, `.github/workflows/e2e-chainsaw.yml`, `e2e/fixtures/vinylcaches/*.yaml`, `e2e/tests/vcl-validation/*.yaml`
- Create: `charts/cloud-vinyl/templates/runtime-catalog.yaml`, `charts/cloud-vinyl/tests/runtimes_test.yaml`

**Interfaces:**
- Consumes: `catalog.Load`, reconciler field `Catalog`, `SetupVinylCacheWebhookWithManager(mgr, cat)`.
- Produces:
  - Operator flag `--runtime-catalog` (default `/etc/cloud-vinyl/runtimes.yaml`). Load failure: log the error and `os.Exit(1)` before the manager starts.
  - Chart values `runtimes.<flavor>.default` and `runtimes.<flavor>.versions."<v>".{daemon,exporter,tracer}`. An empty `tracer` means "the tracer of this chart release": `ghcr.io/bluedynamics/cloud-vinyl-tracer:<appVersion>` for varnish, `...:<appVersion>-vinyl` for vinyl. Part E fills these at release.
  - Helper `cloud-vinyl.runtimes` renders the effective catalog (tracer defaults and the alias applied). ConfigMap `<fullname>-runtimes`, key `runtimes.yaml`; mounted read-only at `/etc/cloud-vinyl`; pod-template annotation `checksum/runtime-catalog: {{ include "cloud-vinyl.runtimes" . | sha256sum }}`.
  - `image.tracer`: `repository` and `tag` default to empty. When either is set, the tracer of every **varnish** entry becomes `<repository|default>:<tag|appVersion>`, and NOTES prints a deprecation note pointing at `runtimes`. The `TRACER_IMAGE` env var is removed.

- [ ] **Step 1: Write failing helm unit tests** in `runtimes_test.yaml`:
  - The ConfigMap contains both flavors; an empty tracer renders as `ghcr.io/bluedynamics/cloud-vinyl-tracer:<appVersion>` resp. `-vinyl` (set `chart.appVersion` in the test).
  - `image.tracer.tag: x` changes the varnish tracer and leaves the vinyl tracer as is; NOTES contains `deprecated`.
  - Overriding `runtimes.vinyl.versions["9.1.0"].daemon` appears in the ConfigMap.
  - The deployment has the arg `--runtime-catalog=/etc/cloud-vinyl/runtimes.yaml`, the mount, no `TRACER_IMAGE` env.
  - Checksum: rendering twice with the same values gives the same annotation; a changed daemon gives a different one (Review Focus 4).
  - Schema: an unknown flavor key (`runtimes.bogus`) and an unknown image key fail validation.
- [ ] **Step 2: Run them.** Run: `helm unittest charts/cloud-vinyl`. Expected: the new tests FAIL.
- [ ] **Step 3: Implement the chart and main.go.** Fill `values.yaml` with the catalog from the spec, still with tags only for now (C7 adds digests). Remove the `image.tracer` defaults from `deployment_test.yaml`/`values_test.yaml` expectations as needed.
- [ ] **Step 4: Run all chart and Go tests.** Run: `helm lint charts/cloud-vinyl && helm unittest charts/cloud-vinyl && go test ./... && make check-generated`. Expected: PASS.
- [ ] **Step 5: E2E plumbing.** `install-operator.sh` stops setting `image.tracer.*` and passes a values overlay that sets the tracer of `runtimes.varnish.versions."9.1.0"` to `${TRACER_IMAGE}` (a values file written to a temp path, not `--set`, because of the dots in the version key). Fixtures drop `image:` so they exercise the catalog path, except `e2e/tests/v1alpha1-compat`, which keeps its `image:` to cover the override. The workflow's pre-pull of `varnish:9.1.0` goes away: catalog images are digest references that the kind nodes pull themselves. Run locally with a scratch kubeconfig (`kind ... --kubeconfig <scratch>`), the `fast` suite: `chainsaw test --test-dir e2e/tests --selector suite=fast`. Expected: PASS.
- [ ] **Step 6: Commit.** `feat(chart): ship the runtime catalog and load it at operator startup (#106)`.

### Task C7: Real digests and the digest check

**Files:**
- Create: `hack/check-catalog-digests.sh`
- Modify: `charts/cloud-vinyl/values.yaml`, `.github/workflows/ci.yml`, `Makefile` (`check-catalog-digests`)

**Interfaces:**
- Consumes: published `varnish:9.1.0`, `varnish-exporter:1.8.3-varnish9.1.0`, `vinyl-cache:9.1.0-r1`, `varnish-exporter:1.8.3-vinyl9.1.0`.
- Produces: `hack/check-catalog-digests.sh [values.yaml]`.

- [ ] **Step 1: Write the script.** For every `daemon` and `exporter` in `runtimes` (read with `yq`): fail if it has no `@sha256:`; fail if `docker buildx imagetools inspect <repo>@<digest>` does not resolve (a typo or a digest from another repository); if `<repo>:<tag>` now resolves to a different digest, print `::warning::` naming both digests and **do not fail** (Review Focus 5; see "Calls" below). Tracer entries are skipped (empty on main).
- [ ] **Step 2: Prove the failure paths.** With a copy of values.yaml: remove a digest → red; change one hex digit → red; point the tag at an older digest that still exists → warning, exit 0. Watch out for the `pipefail` + `grep -q` trap described in CLAUDE.md.
- [ ] **Step 3: Fill in the real index digests** for the four images in `values.yaml` and run the script. Expected: exit 0, no warnings.
- [ ] **Step 4: CI step** "Check catalog digests" in `ci.yml` (job with network access, after checkout), and the Makefile target.
- [ ] **Step 5: Commit and open PR C.** `chore(chart): pin catalog images by digest and check them in CI (#106)`. PR body: upgrade impact (pods roll once; exporter 1.6.1 to 1.8.3, same metric names; `TRACER_IMAGE` gone, `image.tracer` deprecated).

---

## Part D: Vinyl E2E leg (PR D, step 6)

Branch `feat/e2e-vinyl-leg`, after PR C.

### Task D1: Flavor matrix in E2E

**Files:**
- Modify: `.github/workflows/e2e-chainsaw.yml`, `e2e/setup/install-operator.sh`, `e2e/fixtures/vinylcaches/*.yaml`, test files that inline a VinylCache, `.chainsaw.yaml` if needed
- Create: `e2e/values/varnish.yaml`, `e2e/values/vinyl.yaml` (chainsaw values: `flavor: <f>`)

**Interfaces:**
- Consumes: catalog chart values (C6); `Dockerfile.tracer` `FLAVOR` (B2).

- [ ] **Step 1: Fixtures take the flavor from chainsaw values**: `flavor: ($values.flavor)` in every fixture without `image:`. `v1alpha1-compat` keeps `image: varnish:9.1.0` and no flavor (it proves the legacy path on both legs). Check that chainsaw v0.2.12 templates the fixture files applied with `file:`; if it does not, switch those steps to inline resources or a binding. Prove it with a deliberate wrong value (`flavor: bogus` in the values file → the apply is rejected by the CRD enum).
- [ ] **Step 2: Workflow matrix `flavor: [varnish, vinyl]`**, job name includes the flavor. The vinyl leg builds the tracer with `--build-arg FLAVOR=vinyl` as `cloud-vinyl-tracer:dev-vinyl`, loads it, and the catalog overlay sets the vinyl tracer to it. When the PR changes `Dockerfile.vinyl` (detect with `git diff --name-only origin/main...HEAD`), build it as `ghcr.io/bluedynamics/vinyl-cache:ci`, load it, and overlay the vinyl daemon with that tag. `chainsaw test` gets `--values e2e/values/${FLAVOR}.yaml`. Both legs run the same suite selection as today.
- [ ] **Step 3: Run the vinyl leg locally** (kind with scratch kubeconfig, fast suite, `--values e2e/values/vinyl.yaml`). Expected: PASS, including `tracing` (spans arrive, so `vinyllog` reads the VSM) and `cache-and-invalidate` (xkey, purge).
- [ ] **Step 4: Commit and open PR D**, with the `e2e-full` label so both legs run the full suite once. `ci(e2e): run the suite against Vinyl Cache as well (#106)`.

---

## Part E: Release digests (PR E, step 5)

Branch `feat/release-catalog-digests`, after PR C (independent of D).

### Task E1: Write tracer digests at release, refuse unpinned catalogs

**Files:**
- Create: `hack/check-catalog-pinned.sh`
- Modify: `.github/workflows/release.yml` (`helm-chart` job), `RELEASING.md`

**Interfaces:**
- Consumes: the release's `cloud-vinyl-tracer:<ver>` and `:<ver>-vinyl` manifests (B2); empty-tracer semantics (C6).
- Produces: a packaged chart whose `values.yaml` has `@sha256:` on every catalog image.

- [ ] **Step 1: Write `check-catalog-pinned.sh <values.yaml>`**: exit 1 listing every `daemon`/`exporter`/`tracer` without `@sha256:` (empty counts as unpinned). Prove: on main's values.yaml it fails (tracers empty); after filling, it passes.
- [ ] **Step 2: Release job.** After the chart version sed: resolve both tracer index digests with `imagetools inspect`, write `ghcr.io/bluedynamics/cloud-vinyl-tracer:<ver>@<digest>` into every empty varnish tracer and the `-vinyl` one into every empty vinyl tracer (`yq -i`), run `hack/check-catalog-pinned.sh`, then package. The job already waits for `docker-manifest`.
- [ ] **Step 3: Dry run locally**: copy values.yaml, run the yq edits with the published 1.0.2 tracer as a stand-in, run the pinned check. Expected: pass. actionlint.
- [ ] **Step 4: RELEASING.md**: how a Vinyl rebuild (`VINYL_REVISION`) or an exporter rebuild reaches the catalog (PR updating the digest; the CI check proves it), and that the release writes tracer digests itself.
- [ ] **Step 5: Commit and open PR E.** `ci(release): pin the tracer by digest in the packaged catalog (#106)`.

---

## Part F: Docs (PR F, step 7)

Branch `docs/vinyl-flavor`, after C (can overlap D and E).

### Task F1: Documentation

**Files:**
- Create: `docs/sources/how-to/run-vinyl-cache.md`, `docs/sources/how-to/replace-an-image.md`, `docs/sources/explanation/lineages.md`
- Modify: `docs/sources/reference/vinylcache-spec.md`, `docs/sources/reference/helm-values.md`, `docs/sources/reference/varnish-versions.md`, `docs/sources/how-to/upgrade.md`, the three `index.md` toctrees, `README.md` if it names `spec.image` as required

- [ ] **Step 1: Write the pages** with the content listed in the spec's Docs section. Upgrade notes go into a `(upgrade-1-1-runtime-catalog)=` section next to the existing 1.1 metric-label section. Use explicit MyST targets and `{ref}`, never implicit heading anchors. `varnish-versions.md` loses the "not supported yet, see #106" paragraph and gains the Vinyl row.
- [ ] **Step 2: Build.** Run: `make docs`. Expected: no warnings (the build uses `-W`). Check every Mermaid block for `;`: it ends a statement and the Sphinx build does not catch it; escape it as `#59;`.
- [ ] **Step 3: Commit and open PR F.** `docs: Vinyl Cache flavor, runtime catalog, emergency image overrides (#106)`.

---

## Calls made in this plan (not in the spec)

- The exporter workflow skips an already published flavor tag with a notice instead of failing. Overwriting is still refused; a skipped flavor keeps its old, pinned build.
- The `-vinyl` tracer variant never gets a `:latest`; the catalog pins it.
- `hack/check-catalog-digests.sh` fails on a digest that does not exist in the repository, and only **warns** when the tag has moved. The spec said "fails on a mismatch with the tag"; `varnish:9.1.0` is a mutable docker-library tag that moves on every Debian rebuild, so a hard failure would turn red on unrelated PRs. The warning is the signal to bump the digest.
- The runtime webhook check rejects only on create or when `flavor`/`version` changed, so finalizer removal on a cache with a vanished entry still works.
- `status.runtime.exporter`/`tracer` are empty when the sidecar is disabled; printer columns `Flavor` and `Version`.
- The container name stays `varnish` for both flavors.
- Kustomize `config/manager` is not a documented install path and gets no catalog; `make run` needs `--runtime-catalog` pointing at a rendered catalog.
