# Releasing cloud-vinyl

Cutting a release means pushing a tag. Everything else is automated by
[`.github/workflows/release.yml`](.github/workflows/release.yml), which triggers on any
tag matching `v*`.

Nothing needs to be edited or committed beforehand. In particular, do **not** bump the
version in `charts/cloud-vinyl/Chart.yaml` by hand: the committed `0.1.0` is a
placeholder that the workflow overwrites from the tag.

## Choose the version

The project follows semantic versioning, derived from the conventional commit types
since the previous tag:

```bash
git log --format="%s" $(git describe --tags --abbrev=0 --exclude '*-*')..main --no-merges \
  | grep -oE "^[a-z]+" | sort | uniq -c | sort -rn
```

`--exclude '*-*'` skips release candidates, so the comparison is against the last final
release.

A `feat` means a minor bump, `fix` alone means a patch bump. Breaking changes are a
major bump, but note that goreleaser's changelog does not detect `!` or
`BREAKING CHANGE:` markers for you — decide deliberately.

## Release

Make sure CI **and** the E2E suite are green on `main` first. The release workflow reuses
`ci.yml` and will stop if it fails, but it does not run the chainsaw E2E suite, so a
broken cluster path would not be caught during the release itself.

```bash
git checkout main
git pull
gh run list --branch main --limit 4   # CI and E2E both success?

git tag v0.6.0
git push origin v0.6.0
```

## Release candidates

A release candidate is a tag with a SemVer pre-release suffix: `v1.0.0-rc.1`, then
`v1.0.0-rc.2` and so on. Use exactly that form. `v1.0.0rc1` is not SemVer, and the Helm
chart could not be packaged with it.

The workflow treats such a tag differently in three places:

- The images get `:1.0.0-rc.1`, but `:latest` stays on the last final release.
- goreleaser publishes a GitHub *pre-release*, so the rc does not become the latest
  release.
- The chart is pushed as `1.0.0-rc.1`. Helm ignores pre-release versions unless asked, so
  install the rc with `--version 1.0.0-rc.1` (or `--devel`).

The final `v1.0.0` then gets release notes against the previous final release, not
against the last rc. The workflow sets `GORELEASER_PREVIOUS_TAG` for that.

Tag the final release on the commit of the last rc when nothing changed since. That
commit then carries two tags, so the workflow also sets `GORELEASER_CURRENT_TAG` to the
pushed tag; otherwise goreleaser may pick the rc tag and fail on its existing assets, as
it did for v1.0.0.

```bash
git tag v1.0.0-rc.1
git push origin v1.0.0-rc.1

helm install cloud-vinyl oci://ghcr.io/bluedynamics/charts/cloud-vinyl --version 1.0.0-rc.1
```

## What the workflow does

Five jobs, in this order:

1. **`ci`** reuses `.github/workflows/ci.yml` in full. Everything below waits on it.
2. **`docker-build`** builds `cloud-vinyl-operator` and `cloud-vinyl-agent` for
   `linux/amd64` and `linux/arm64`, four jobs in a matrix, pushing per-arch tags like
   `:0.6.0-linux-amd64`. The arm64 jobs run on `ubuntu-24.04-arm`, not under emulation.
3. **`docker-manifest`** joins the per-arch tags into multi-arch manifests and publishes
   both `:0.6.0` and `:latest`, or only the version for a release candidate.
4. **`release`** runs goreleaser, which builds the binaries, generates the changelog and
   creates the GitHub release.
5. **`helm-chart`** rewrites `version` and `appVersion` in `Chart.yaml` from the tag, pins the tracer digests in the catalog (see below),
   packages the chart and pushes it to `oci://ghcr.io/bluedynamics/charts`.

Note that `helm-chart` depends on `docker-manifest`, so the chart is never published
pointing at images that do not exist yet.

## The runtime catalog and its digests

`charts/cloud-vinyl/values.yaml` holds the runtime catalog (`runtimes.<flavor>.versions`).
Every `daemon`, `exporter` and `tracer` image in the packaged chart is pinned by a
multi-arch index digest.

- **Tracer:** the catalog commits an empty `tracer`. The `helm-chart` job resolves the
  index digests of `cloud-vinyl-tracer:<version>` (varnish) and
  `cloud-vinyl-tracer:<version>-vinyl` (vinyl) and writes them into every empty entry
  before packaging. You do nothing for it.
- **Daemon and exporter:** their digests are committed. After a Vinyl rebuild
  (a new `VINYL_REVISION`, i.e. a new `vinyl-cache:<ver>-rN`) or an exporter rebuild,
  open a PR that updates the image tag and digest in `values.yaml`. Get the index
  digest with `docker buildx imagetools inspect <image>:<tag>`, never a per-arch one.
  `hack/check-catalog-digests.sh` runs in CI and fails if the digest is missing or does
  not exist in the repository; it only warns when an upstream tag has moved on.
- **Safety net:** before packaging, the job runs `hack/check-catalog-pinned.sh`. It
  fails the release if any catalog image, including a non-empty tracer of your own
  build, has no `@sha256:` digest.

## Verify afterwards

```bash
gh run list --workflow Release --limit 1
gh release view v0.6.0

# Images resolve and are multi-arch
docker manifest inspect ghcr.io/bluedynamics/cloud-vinyl-operator:0.6.0 \
  | grep -c architecture     # expect 2

# Chart is pullable at the new version
helm show chart oci://ghcr.io/bluedynamics/charts/cloud-vinyl --version 0.6.0
```

## If a release fails midway

The jobs are not transactional. A failure in `helm-chart` still leaves the images and the
GitHub release published.

Do not delete and re-push the same tag: the container tags are already public and
consumers may have pulled them. Fix the problem and cut the next patch version instead.

## Known wrinkles

**Toolchain work is invisible in the notes.** `.goreleaser.yaml` excludes `docs`, `ci`
and `chore` commits, along with merge commits. That is usually right, but it means a
release consisting mostly of dependency and toolchain updates will produce nearly empty
notes. v0.6.0 is the cautionary example in the other direction: those filters were
written without allowing for conventional-commit scopes, so `chore(ci):` never matched
`^chore:` and its notes carried 44 entries, 32 of them noise.

**`fix` and `test` are never filtered, whatever their scope.** Internal commits like
`fix(ci): ...` or `test(e2e): ...` do reach the user-facing notes by design, since the
filters key on the type rather than the scope. Worth a skim after a release.

**The exporter image is released separately.** `ghcr.io/bluedynamics/varnish-exporter`
has its own workflow, [`release-exporter.yml`](.github/workflows/release-exporter.yml),
triggered manually or by changes to `Dockerfile.exporter`. It is not versioned by the
repo tag, because the image is essentially static. Its tag is the upstream exporter
version, the flavor and the bundled daemon version, one image per flavor:
`<exporter-version>-<flavor><daemon-version>`, e.g. `1.8.3-varnish9.1.0` and
`1.8.3-vinyl9.1.0`. `EXPORTER_VERSION` comes from the workflow, the daemon version from
`VARNISH_IMAGE` / `VINYL_IMAGE` in `Dockerfile.exporter` (`-r<N>` image revisions are
stripped). A daemon bump therefore publishes a new tag, and the operator's default
exporter image has to follow it. Tags are immutable: a `Dockerfile.exporter` change
without a version bump is a green run that skips existing tags with a notice. To
republish one flavor, dispatch the workflow with `overwrite_flavor` set to it; the other
flavor is left alone.
