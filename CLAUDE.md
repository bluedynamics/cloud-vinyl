# Working in this repository

## Testing layers

Three layers, described in full in
[docs/sources/explanation/testing-strategy.md](docs/sources/explanation/testing-strategy.md).
Read it before adding a test.

- **Unit tests** with fake clients: does the operator build the right objects?
- **envtest** (`make test-int`): does a real API server accept them? Today this
  layer is close to a stub — one real `It` block in the controller suite, and a
  webhook suite that boots envtest but exercises no admission decision yet.
  Say what a layer is *for*, not what it currently covers; the two are not the
  same claim.
- **E2E** (chainsaw + kind + Calico): only what a real cluster can prove — real
  varnishd, real networking, real caching, real policy enforcement.

Do not add control-plane assertions to E2E. If a test only checks that an object
was created, it belongs in a lower layer where it runs in seconds.

## The E2E layer boundary

This one is enforced by `hack/check-e2e-boundary.sh` and will fail the build:

- `cmd/vinylprobe` speaks HTTP only. It must not import `k8s.io/*`, directly or
  transitively.
- chainsaw tests own Kubernetes state only. They must not invoke `curl` or
  `wget`; drive HTTP through `vinylprobe`.

Both halves are tempting to extend in the wrong direction. Resist it.

## Chainsaw suite labels

Every test in `e2e/tests/` needs exactly one `metadata.labels.suite`, `fast` or
`full`. `hack/check-suite-labels.sh` enforces this, because a mislabelled test
runs in neither suite and vanishes silently. Add the `e2e-full` label to a pull
request to run the `full` suite before merging, instead of waiting for a push
to main.

## Shell checks: prove the failure path, not just the pass path

A check that has only ever been run against clean input is unverified. Before
trusting a new `hack/*.sh` check (or believing an existing one), introduce the
violation it's supposed to catch and confirm it actually goes red — then remove
the violation.

This is not hypothetical for this repo. Both `hack/check-suite-labels.sh` and
`hack/check-e2e-boundary.sh` were written, looked correct, and silently passed
on the exact input they exist to reject — twice, independently, both from the
same root cause: `set -euo pipefail` plus a `grep` used as a filter. When
`grep -q` sits at the end of a pipeline and finds its match early, it exits and
closes its stdin pipe; the process feeding it (e.g. `go list`) then gets
SIGPIPE, and under `pipefail` the pipeline's exit status comes from the killed
producer, not from grep's successful match. The `if` sees a nonzero status and
skips the failure branch — the exact opposite of what the script was checking
for. Similarly, `grep -c ... || true` after `set -e` is required, or a file
with zero matches (grep's normal exit-1-on-no-match) aborts the whole script
instead of being reported and continuing to the next file.

Both scripts carry comments at the point of the fix explaining the specific
failure mode. Read them before writing another shell check in this repo, and
budget time to break your own check on purpose before relying on it.

## Version pins that must agree

- kind CLI and `kindest/node` image come from the same kind release. Bump both
  together, or `kind load` fails with an unreadable containerd config error.
- golangci-lint is pinned in both `.github/workflows/ci.yml` and the `Makefile`.
  They must match, or local and CI lint with different rule sets.
- Raising the `go` directive in `go.mod` forces a golangci-lint bump: the linter
  refuses to run when it was built with an older Go than the module targets.

See #87 for the general problem.
