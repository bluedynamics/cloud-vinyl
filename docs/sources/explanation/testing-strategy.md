# Testing strategy

cloud-vinyl tests at three layers. Each answers a question the layer below it
cannot, and putting a test at the wrong layer buys slower feedback for no extra
confidence.

## Unit tests

The bulk of the coverage: 75 test functions across `internal/controller/` alone,
using fake clients. They prove the operator *builds* the right objects — the
right StatefulSet, the right NetworkPolicies, the right VCL.

What they cannot prove is that a real API server *accepts* what was built.

## Integration tests (envtest)

Run against a real `kube-apiserver` and `etcd`, no container runtime. This is
where schema, defaulting, CRD validation and webhook admission belong.

Run them with `make test-int`.

Today the suite barely occupies that space yet. The controller suite has one
`It` block, a happy-path reconcile. The webhook suite boots envtest with the
validating webhook wired into a real manager, but every `Context` under it is
scaffolding: the `It` blocks are commented-out examples, so no admission
decision is actually exercised. The harness is real; filling it in is a matter
of writing assertions, not building infrastructure. Treat "we have envtest" and
"envtest checks webhook admission" as two different claims — only the first is
true today.

## End-to-end tests (chainsaw)

Run against a real multi-node kind cluster with Calico. They cover **only what a
real cluster can prove**: real varnishd, real networking, real caching, real
policy enforcement.

E2E deliberately does not re-assert control-plane behaviour. A test that only
checks "did the operator create this object" belongs one or two layers down,
where it runs in seconds instead of minutes.

### The layer boundary

The E2E suite has two halves, and the line between them is enforced, not merely
agreed:

- **chainsaw** owns Kubernetes state: fixtures, waiting for `Ready`, namespace
  isolation, cleanup, failure diagnostics. It never speaks HTTP.
- **`cmd/vinylprobe`** owns HTTP. It never imports `k8s.io/*` and knows nothing
  about Kubernetes objects.

`hack/check-e2e-boundary.sh` fails the build if `vinylprobe` acquires a
Kubernetes dependency, or if a chainsaw test reaches for `curl` or `wget`. Both
directions have been demonstrated to actually fail on a real violation, not just
reviewed as plausible — the check itself had a bug, a `pipefail`/`grep`
interaction that let the `k8s.io` direction pass silently, which was caught and
fixed before being trusted (`CLAUDE.md` at the repo root has the general
warning this incident prompted).

The reason for the rule is that both halves are individually tempting to extend
in the wrong direction. A quick `curl` in a chainsaw step looks harmless; so does
importing a client to look up a pod name. A few of those and the suite has no
structure left.

### How cache state is checked over HTTP

The generated VCL sets no debug headers, and adding one purely for tests would be
a product change. Instead `vinylprobe` sends requests carrying a distinct
`X-Probe` token and reads the backend's echo back out of the response body: a
cached response still carries the token of the request that filled it. This
gives two ways to observe the cache without ever inspecting a Kubernetes object:

- `probe.Detect` (`-expect hit|miss`) fires two requests back-to-back and
  compares them — useful when nothing has seeded the cache yet.
- `probe.Seed` / `probe.Check` (`-seed`, `-check ... -expect-state`) split
  seeding from checking into separate single-request calls, so a chainsaw test
  can seed once, do other work, and check later without a second request
  quietly repopulating the very cache it's trying to observe.

`vinylprobe` also has a `-purge` mode (`probe.Purge`) that issues an HTTP
`PURGE`, ready for the day invalidation is testable end-to-end (see below).

### cache-per-pod: what it proves, and what it doesn't

The one chainsaw test in the suite that sends real HTTP traffic and checks what
comes back is `cache-per-pod`. It seeds each of a three-pod cluster's pods
directly, by its own StatefulSet DNS name, with its own token, then confirms
each pod is still serving the object it was seeded with.

It proves caching works, per pod, over real HTTP. It does **not** prove that
invalidation works, and deliberately doesn't try: purge is broken in every
configuration currently reachable through the API —

- PURGE is rejected with 403 under `spec.cluster.enabled: true`: the internal
  Varnish-to-Varnish hop that redirects an unshredded PURGE arrives at the
  `vinyl_purge_allowed` ACL check carrying a Varnish pod's IP instead of the
  operator's, and is rejected (#93).
- Soft purge never revalidates: the generated `vcl_hit` delivers the stale
  object for the whole grace window regardless of purge, so with the
  hardcoded 24h grace a soft purge is a no-op for a full day (#94).
- Hard purge cannot be requested at all: the defaulting webhook coerces
  `spec.invalidation.purge.soft` back to `true` on every admission, because a
  plain bool can't distinguish "unset" from "explicitly false" (#95).

No honest end-to-end test of purge broadcasting exists yet, because there is
nothing working to test. Once #93–#95 are fixed, that test belongs here,
using the `-purge` mode already built into `vinylprobe`.

### Fast and full

Every test carries `metadata.labels.suite`, either `fast` or `full`.

| Trigger | Suite |
|---|---|
| Pull request | `fast` |
| Push to main | `full` |
| PR labelled `e2e-full` | `full` |
| Manual (`workflow_dispatch`) | `full` |

Add the `e2e-full` label to a pull request to get the whole suite before
merging, without waiting for the merge to main. The check name states which
suite ran, so a green tick is never ambiguous.

`hack/check-suite-labels.sh` fails the build if a test carries no label or more
than one, because a mislabelled test would otherwise run in neither suite and
disappear without a sound.
