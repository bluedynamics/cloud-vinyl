# Testing strategy

cloud-vinyl tests at three layers.
Each answers a question the layer below it cannot.
Putting a test at the wrong layer buys slower feedback for no extra confidence.

(testing-strategy-unit-tests)=

## Unit tests

The bulk of the coverage: 75 test functions across `internal/controller/` alone, using fake clients.
They prove the operator *builds* the right objects: the right StatefulSet, the right NetworkPolicies, the right VCL.

They cannot prove that a real API server *accepts* what was built.

(testing-strategy-integration-tests)=

## Integration tests (envtest)

These run against a real `kube-apiserver` and `etcd`, with no container runtime.
This is where schema, defaulting, CRD validation and webhook admission belong.

Run them with `make test-int`.

Today the suite barely occupies that space yet.
The controller suite has one `It` block, a happy-path reconcile.
The webhook suite boots envtest with the validating webhook wired into a real manager.
Every `Context` under it is scaffolding.
The `It` blocks are commented-out examples, and no admission decision is actually exercised.
The harness is real.
Filling it in is a matter of writing assertions, and it needs no new infrastructure.
Treat "we have envtest" and "envtest checks webhook admission" as two different claims.
Only the first is true today.

(testing-strategy-end-to-end-tests)=

## End-to-end tests (chainsaw)

These run against a real multi-node kind cluster with Calico.
They cover **only what a real cluster can prove**: real varnishd, real networking, real caching, real policy enforcement.

E2E deliberately does not re-assert control-plane behaviour.
A test that only checks "did the operator create this object" belongs one or two layers down.
There it runs in seconds instead of minutes.

### The layer boundary

The E2E suite has two halves, and the build enforces the line between them:

- **chainsaw** owns Kubernetes state: fixtures, waiting for `Ready`, namespace isolation, cleanup, failure diagnostics.
  It never speaks HTTP.
- **`cmd/vinylprobe`** owns HTTP.
  It never imports `k8s.io/*` and knows nothing about Kubernetes objects.

`hack/check-e2e-boundary.sh` fails the build if `vinylprobe` acquires a Kubernetes dependency.
It also fails the build if a chainsaw test reaches for `curl` or `wget`.
Both directions have been demonstrated to fail on a real violation.
The check itself once had a bug, a `pipefail`/`grep` interaction that let the `k8s.io` direction pass silently.
That bug was caught and fixed before the check was trusted.
`CLAUDE.md` at the repo root has the general warning this incident prompted.

Both halves are individually tempting to extend in the wrong direction.
A quick `curl` in a chainsaw step looks harmless.
So does importing a client to look up a pod name.
A few of those and the suite has no structure left.

### How cache state is checked over HTTP

The generated VCL sets no debug headers, and adding one purely for tests would be a product change.
Instead `vinylprobe` sends requests carrying a distinct `X-Probe` token and reads the backend's echo back out of the response body.
A cached response still carries the token of the request that filled it.
This gives two ways to observe the cache without ever inspecting a Kubernetes object:

- `probe.Detect` (`-expect hit|miss`) fires two requests back-to-back and compares them.
  It is useful when nothing has seeded the cache yet.
- `probe.Seed` / `probe.Check` (`-seed`, `-check ... -expect-state`) split seeding from checking into separate single-request calls.
  A chainsaw test can then seed once, do other work, and check later.
  No second request quietly repopulates the very cache it is trying to observe.

`vinylprobe` also has a `-purge` mode (`probe.Purge`) that issues an HTTP `PURGE`.
It reports the operator's own count of objects removed (`objectsPurged`, #103).
The value `nil` (unknown) is kept strictly distinct from a confirmed `0`.
A sharded broadcast purge legitimately removes 0 objects on every pod except the URL's owner.
`-expect-purged N` asserts against that count directly.
A missing count is always a failure and is never read as `0`.

Every seed, check and purge call also accepts `-host`.
It overrides the HTTP `Host` header independent of the address `-url` dials.
This exists because Vinyl Cache hashes `req.http.host` into the cache key (`vcl_hash.vcl.tmpl`).
Addressing a pod directly by its own StatefulSet DNS name is the only way to pin which pod handles a request.
That gives the pod a *different* Host, and therefore a different cache key, than the same path reached any other way.
Seed three pods that way with no `-host` override and each pod caches a genuinely different object.
A single broadcast `PURGE` can then match at most one of them.
`-host` pins seeding, checking and purging to one shared value.
"Seeded directly" and "invalidated through the service" then agree on the same key.
See the doc comment of `fetch` in `internal/probe/cache.go` for the full reasoning.
The descriptions of `cache-and-invalidate` and `shard-routing` show where it is used.

### cache-and-invalidate and shard-routing: what they prove

Two chainsaw tests send real HTTP traffic through the cache and check what comes back.
They then invalidate it and check again.

**`cache-and-invalidate`** (fast suite) runs without `spec.cluster.enabled`.
Each of its three pods caches independently.
It seeds a distinct token into each pod directly, using the pod's own StatefulSet DNS name.
It pins `-host` to the invalidation service's DNS name.
All three pods then land on the same key, as described above.
It confirms all three are cached and issues one `PURGE` through the invalidation service.
It then confirms all three are gone, asserting `-expect-purged 3` along the way.
This test replaces `cache-per-pod`, which proved a weaker claim than its name suggested.
Seeded with no `-host` override, the three pods of `cache-per-pod` each cached a *different* key.
Nothing there proved three pods holding the same object, or could have been purged with one request the way this test is.

**`shard-routing`** (full suite) runs with clustering and shard-by-URL enabled.
It seeds one object through one entry pod.
It confirms every pod resolves the same URL to the same cached object, which proves one owner and not three independent copies.
It then purges once and confirms the object is gone.
It asserts `-expect-purged 1`.
Under sharding, only the owner pod ever held a copy.
Asserting `3` would be wrong.
The check for "not cached" after the purge goes through exactly one entry pod.
Every entry funnels to the same single cache entry under clustering.
A second check would observe the first check's own cache-repopulating miss and not the purge's aftermath.

Together these are the end-to-end proof for defects that were fixed without any checked-in evidence:

- PURGE rejected with 403 under `spec.cluster.enabled: true` (#93).
  The internal cache-to-cache hop that redirects an unsharded PURGE reached the `vinyl_purge_allowed` ACL check carrying a peer pod's IP instead of the operator's real one.
  The ACL rejected it.
  The successful purge of `shard-routing` under clustering exercises exactly what this defect broke.
- Soft purge never revalidating (#94).
  The generated `vcl_hit` delivered the stale object for the whole grace window regardless of purge.
  Both tests use hard purge deliberately (see `fixtures/vinylcaches/no-cluster.yaml` and `sharded.yaml`).
  The grace and revalidate window of soft purge is real, correct product behaviour.
  An immediately-following request can legitimately still see the pre-purge object for a short, unbounded window.
  That is exactly the kind of nondeterminism this suite exists not to reintroduce.
  The two tests therefore do not assert against it.
- Hard purge unrequestable at all (#95).
  The defaulting webhook coerced `spec.invalidation.purge.soft` back to `true` on every admission.
  Both fixtures set an explicit `false`, and it is honored.
- Shard-by-URL hashing nothing at request time (#92).
  Before this fix, "which pod, if any, holds this object" was not something a test could assert.
  The `-expect-purged 1` of `shard-routing` is only meaningful because routing is now deterministic.
- Silent, unverifiable purge outcomes (#103).
  Before the operator surfaced `objectsPurged`, "did the purge actually remove anything" was not observable at all from outside a debugger.
  Both tests assert the exact count and not just a 200 status.

### Fast and full

Every test carries `metadata.labels.suite`, either `fast` or `full`.

| Trigger | Suite |
|---|---|
| Pull request | `fast` |
| Push to main | `full` |
| PR labelled `e2e-full` | `full` |
| Manual (`workflow_dispatch`) | `full` |

Add the `e2e-full` label to a pull request to get the whole suite before merging, without waiting for the merge to main.
The check name states which suite ran.
A green tick is never ambiguous.

`hack/check-suite-labels.sh` fails the build if a test carries no label or more than one.
A mislabelled test would otherwise run in neither suite and disappear without a sound.
