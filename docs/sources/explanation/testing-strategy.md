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
`PURGE` and reports the operator's own count of objects removed (`objectsPurged`,
#103) — `nil` (unknown) kept strictly distinct from a confirmed `0`, since a
sharded broadcast purge legitimately removes 0 objects on every pod but the
URL's owner. `-expect-purged N` asserts against that count directly; a missing
count is always a failure, never silently read as `0`.

Every seed/check/purge call also accepts `-host`, overriding the HTTP `Host`
header independent of the address `-url` dials. This exists because Varnish
hashes `req.http.host` into the cache key (`vcl_hash.vcl.tmpl`): addressing a
pod directly by its own StatefulSet DNS name — the only way to pin which pod
handles a request — gives that pod a *different* Host, and therefore a
different cache key, than the same path reached any other way. Seed three pods
that way with no `-host` override and each pod caches a genuinely different
object; a single broadcast `PURGE` can then match at most one of them. `-host`
pins seeding, checking and purging to one shared value so "seeded directly" and
"invalidated through the service" agree on the same key. See `fetch`'s doc
comment in `internal/probe/cache.go` for the full reasoning, and
`cache-and-invalidate`'s and `shard-routing`'s own descriptions for where it is
used.

### cache-and-invalidate and shard-routing: what they prove

Two chainsaw tests send real HTTP traffic through the cache and check what
comes back, then invalidate it and check again.

**`cache-and-invalidate`** (fast suite) runs without `spec.cluster.enabled`, so
each of three pods caches independently. It seeds a distinct token into each
pod directly (own StatefulSet DNS name, `-host` pinned to the invalidation
service's DNS name so all three land on the same key — see above), confirms
all three are cached, issues one `PURGE` through the invalidation service, and
confirms all three are gone — asserting `-expect-purged 3` along the way. This
test replaces `cache-per-pod`, which proved a weaker claim than its name
suggested: seeded with no `-host` override, its three pods each cached a
*different* key, so nothing there proved three pods holding the same object,
or could have been purged with one request the way this test is.

**`shard-routing`** (full suite) runs with clustering and shard-by-URL
enabled. It seeds one object through one entry pod, confirms every pod
resolves the same URL to the same cached object (proving one owner, not three
independent copies), purges once, and confirms the object is gone — asserting
`-expect-purged 1`, not `3`: under sharding, only the owner pod ever held a
copy. Checking "not cached" after the purge is done through exactly one entry
pod, not all three: every entry funnels to the same single cache entry under
clustering, so a second check would observe the first check's own
cache-repopulating miss, not the purge's aftermath.

Together these are the end-to-end proof for defects that were, in turn, fixed
without any checked-in evidence:

- PURGE rejected with 403 under `spec.cluster.enabled: true` (#93): the
  internal Varnish-to-Varnish hop that redirects an unshredded PURGE reached
  the `vinyl_purge_allowed` ACL check carrying a peer pod's IP instead of the
  operator's real one, and was rejected. `shard-routing`'s successful purge
  under clustering is the exercise this defect specifically broke.
- Soft purge never revalidating (#94): the generated `vcl_hit` delivered the
  stale object for the whole grace window regardless of purge. Both tests use
  hard purge deliberately (see `fixtures/vinylcaches/no-cluster.yaml` and
  `sharded.yaml`) — soft purge's grace/revalidate window is real, correct
  product behaviour (an immediately-following request can legitimately still
  see the pre-purge object for a short, unbounded window), but it is exactly
  the kind of nondeterminism this suite exists not to reintroduce, so it is
  deliberately not what these two assert against.
- Hard purge unrequestable at all (#95): the defaulting webhook coerced
  `spec.invalidation.purge.soft` back to `true` on every admission. Both
  fixtures set an explicit `false`, and it is honored.
- Shard-by-URL hashing nothing at request time (#92): before this fix,
  "which pod, if any, holds this object" was not something a test could
  assert. `shard-routing`'s `-expect-purged 1` is only meaningful because
  routing is now deterministic.
- Silent, unverifiable purge outcomes (#103): before the operator surfaced
  `objectsPurged`, "did the purge actually remove anything" was not
  observable at all from outside a debugger. Both tests assert the exact
  count, not just a 200 status.

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
