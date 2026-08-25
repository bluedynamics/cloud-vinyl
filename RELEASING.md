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
git log --format="%s" $(git describe --tags --abbrev=0)..main --no-merges \
  | grep -oE "^[a-z]+" | sort | uniq -c | sort -rn
```

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

## What the workflow does

Five jobs, in this order:

1. **`ci`** reuses `.github/workflows/ci.yml` in full. Everything below waits on it.
2. **`docker-build`** builds `cloud-vinyl-operator` and `cloud-vinyl-agent` for
   `linux/amd64` and `linux/arm64`, four jobs in a matrix, pushing per-arch tags like
   `:0.6.0-linux-amd64`. The arm64 jobs run on `ubuntu-24.04-arm`, not under emulation.
3. **`docker-manifest`** joins the per-arch tags into multi-arch manifests and publishes
   both `:0.6.0` and `:latest`.
4. **`release`** runs goreleaser, which builds the binaries, generates the changelog and
   creates the GitHub release.
5. **`helm-chart`** rewrites `version` and `appVersion` in `Chart.yaml` from the tag,
   packages the chart and pushes it to `oci://ghcr.io/bluedynamics/charts`.

Note that `helm-chart` depends on `docker-manifest`, so the chart is never published
pointing at images that do not exist yet.

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

**The changelog filters are prefix-based and let internal work through.**
`.goreleaser.yaml` excludes `^docs:`, `^ci:` and `^chore:`. Because the match is on the
raw prefix, a scoped commit like `fix(ci): ...` or `fix(e2e): ...` does *not* match
`^ci:` and lands in the user-facing release notes. Worth skimming the generated notes
after a release and editing them if internal churn slipped in.

**Toolchain work is invisible in the notes.** Conversely, everything committed as
`chore:` is filtered out entirely. That is usually right, but it means a release that
consisted mostly of dependency and toolchain updates will produce nearly empty notes.

**The exporter image is released separately.** `ghcr.io/bluedynamics/varnish-exporter`
has its own workflow, [`release-exporter.yml`](.github/workflows/release-exporter.yml),
triggered manually or by changes to `Dockerfile.exporter`. It is versioned by
`EXPORTER_VERSION` in that workflow, not by the repo tag, because the image is
essentially static.
