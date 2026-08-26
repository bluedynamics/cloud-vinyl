# E2E test rewrite: data-path coverage

Design for issue #26. Supersedes that issue's proposals, several of which are
obsolete.

## Why the issue needed rethinking

#26 was filed on 2026-04-09 and three of its premises no longer hold:

| Claim in #26 | Reality on 2026-08-26 |
|---|---|
| Depends on a custom varnish image carrying `vmod_xkey` | Obsolete. Stock `varnish:8.0.2` ships `libvmod_xkey.so`; fixtures already use it |
| "Consider adding a `chainsaw-config.yaml`" | Done. `.chainsaw.yaml` exists and carries the failure-diagnostics `catch` blocks |
| Add a readiness probe to the varnish container | Still open. The probe is on `vinyl-agent`, not on `varnish` |

Its central criticism holds, and more sharply than written: **none of the ten
E2E tests sends a single HTTP request through the cache.** `purge-broadcast`
asserts that a Service object exists. `xkey-invalidation` asserts `phase: Ready`
and three replicas. Both are named after data-path behaviour they never
exercise, so a green run implies far more than it proves.

## What E2E is for

E2E covers only what a real cluster can prove and cheaper layers cannot: real
varnishd, real networking, real caching, real policy enforcement.

One correction to the reasoning that led here. It is tempting to say
"control-plane behaviour is already covered by envtest" — that is wrong. The
coverage lives in **76 unit tests across nine files using fake clients**. The
envtest suite is effectively a stub: a single `It` block that reconciles once.

Fake clients prove the operator *builds* the right objects. They do not prove a
real API server *accepts* them — schema, defaulting, CRD validation, webhook
interaction. That layer is currently held up by the E2E tests, accidentally.
Deleting them wholesale would lose real coverage, so this design keeps one
acceptance test and proposes deepening envtest as separate work.

## Architecture

Two layers with a boundary that is mechanically checkable.

**chainsaw** owns Kubernetes state: applying fixtures, waiting for `Ready`,
namespace isolation, cleanup, and the diagnostics `catch` blocks. It never
touches HTTP.

**`cmd/vinylprobe`** is a new Go binary. It takes targets as flags, speaks only
HTTP, and answers with an exit code plus one plain-text line. It never imports
`k8s.io/*` and knows nothing about Kubernetes objects.

chainsaw invokes it as a `script:` step running inside a pod in the test
namespace. Running it in-cluster is required, not incidental: `kubectl
port-forward` would bypass NetworkPolicies and defeat the point of enforcing
them.

### Detecting a cache hit

The generated VCL sets no debug headers, so a test cannot tell HIT from MISS by
reading a response header, and adding one would be a product change made for
tests.

Instead: `ealen/echo-server` echoes request headers back in its body. Two
requests to the same URL carrying different `X-Probe` values reveal the answer —
a cached response still shows the *first* request's value.

Validated before adopting it:

```
Request 1 (X-Probe: FIRST)  -> body contains "x-probe":"FIRST"
Request 2 (X-Probe: SECOND) -> body contains "x-probe":"FIRST"   => served from cache
```

No product change, no fixture change. `vinylprobe` encapsulates the trick so
tests are written as "expect HIT" rather than as header comparisons; if the
mechanism is ever replaced, one place changes.

## Test inventory

### Fast core — every pull request, target ~5 minutes

| Test | Proves |
|---|---|
| `acceptance` | A real cluster accepts all five fixtures |
| `cache-and-invalidate` | Objects are cached; one PURGE clears them on **every** pod |
| `netpol-enforcement` | The operator reaches the agent; an unrelated pod does **not** |

`acceptance` replaces `basic-lifecycle` and `volumes-and-pvc` in the role they
actually served — catching CRD and schema regressions against a real API server —
rather than the role their names claimed.

It does **not** replace `vcl-validation`, which asserts that *invalid* resources
are rejected; applying valid fixtures proves nothing about that. See below.

### Full suite — pushes to main, and on demand per PR

Adds:

- `xkey-selective` — only tagged objects are invalidated
- `shard-routing` — the same key hits the same cached object regardless of entry pod
- `scale-out-participation` — a new pod joins sharding after scale-up
- `ha-invalidation-after-failover` — invalidation survives a leader change
- `exporter-scrape` — the metrics endpoint is reachable and reports cache hits
- `per-backend-routing` — requests reach the correct backend

Two of these test something currently impossible: the negative assertion in
`netpol-enforcement`, and `exporter-scrape`, for which a fixture must enable
monitoring for the first time.

### Dropped, with a precondition

`vcl-validation` asserts that an invalid CIDR and a forbidden parameter are
rejected, and that defaults are applied. The webhook envtest suite
(`internal/webhook/v1alpha1/vinylcache_webhook_test.go`) already covers
admission, rejection of missing required fields, defaulting and update
validation — against a real API server with the real webhook, in seconds.

It does not yet cover those two specific rejection cases. So the order matters:
**move the invalid-CIDR and forbidden-parameter cases into the webhook envtest
suite first, then delete the E2E test.** Deleting first would lose coverage that
nothing else holds.

### Dropped outright

`drift-detection`. That the operator reverts manual edits is already covered by
`statefulset_test.go` at unit level, and a real cluster adds nothing to it.

### Coverage gap found while designing

**No fixture enables monitoring.** The exporter sidecar, the ServiceMonitor, the
exporter NetworkPolicy and the whole metrics path are unexercised — although
issue #56 was a failure in exactly that area. `exporter-scrape` closes this.

## Suite split

Each test carries `metadata.labels.suite: fast` or `full`; the workflow selects
with chainsaw's `--selector`.

This replaces today's `--include-test-regex` selection, which needs a paragraph
of comment in the workflow because chainsaw maps it onto Go's `-test.run`: a
pattern missing the `chainsaw/` prefix silently matches nothing, and per that
comment a step once ran zero tests while exiting 0.

Labels move the failure mode but do not remove it — a typo means a test runs
*nowhere*. A CI check asserting that every test carries exactly one `suite`
label closes it mechanically.

### Triggers

- **Pull request** — fast core.
- **Push to main** — full suite.
- **PR labelled `e2e-full`** — full suite, and on every subsequent push while
  the label remains. Intended for use just before merge.
- **`workflow_dispatch`** — kept unchanged; its purpose from #63 (re-running
  unmodified main on demand) is untouched.

Three details decide whether the label mechanism helps or annoys:

1. `pull_request: types: [labeled]` fires for *every* label. Without a
   `github.event.label.name == 'e2e-full'` guard, adding `documentation` would
   start a 13-minute suite.
2. The job name must carry the variant — "E2E (fast)" or "E2E (full)". Under one
   shared name a green check is ambiguous about which suite passed, which is the
   same class of ambiguity this rewrite exists to remove.
3. Permission falls out for free: only users with write access can label. A
   `/e2e-full` comment command would need its own permission checks and would run
   in the base-branch context for fork PRs.

Note for later: `main` currently has no branch protection, so a job name that
varies is safe. If required status checks are introduced, a varying check name
needs care.

### Path filter

`e2e-chainsaw.yml` has no path filter, so every merge to main — including
documentation-only ones — runs the full multi-node suite. It gets one covering
`e2e/`, `charts/`, `config/`, Go sources and the workflow file itself. Drawn too
narrowly it would be worse than none.

## Calico

The whole suite runs on a policy-enforcing CNI. The operator ships four
NetworkPolicies as a security feature, and `kindnet` ignores them: a policy that
blocked all traffic would pass today's suite.

Spike results, measured rather than assumed:

| | Time to genuinely operational |
|---|---|
| kindnet (today) | 96s |
| Calico | 113s |

About **17 seconds**, including a 33s cold image pull that CI removes by
pre-pulling and `kind load`, the same pattern already used for the varnish image.

Enforcement was verified explicitly — reachable without a policy, blocked under
`deny-all` — because the change would be pointless otherwise.

**The operator's policies survive enforcement.** Installed via Helm as E2E does,
a three-replica `VinylCache` reached `Ready` in ~25s with all pods `2/2`,
including the delicate part where the operator authorises itself in the agent
policy by its own pod IP (#58).

That reframes the justification: the gap is **latent, not active**. Switching to
Calico would not uncover a bug today. Its value is regression protection and
making negative assertions possible at all — that an unrelated pod *cannot* reach
the agent cannot be shown under kindnet.

### Wiring

`disableDefaultCNI: true` in the kind config, apply the Calico manifest, set
`CALICO_IPV4POOL_CIDR` to `10.244.0.0/16` to match the repo's `podSubnet`.

One detail is mandatory: wait on `rollout status daemonset/calico-node`, **not**
`wait --for=condition=Ready nodes`. Nodes report `Ready` as soon as the CNI
binary is on disk while calico-node is still starting. During the spike this
made Calico look *faster* than kindnet (67s) and would have produced a suite that
intermittently runs against a half-ready cluster.

## Determinism

The repo has a flake history (#63, #67, #72) and data-path tests are more timing
sensitive than object assertions. Three rules:

1. TTLs are stated explicitly in fixtures, never inherited from defaults.
2. Every test uses cache keys containing its own name, so tests sharing a cluster
   cannot disturb each other through the cache.
3. `vinylprobe` always waits on a condition with a deadline, never a fixed
   `sleep`.

## Keeping the boundary

A boundary that exists only in someone's head does not survive three sessions.
Three layers:

1. **Enforced** — a CI check that `cmd/vinylprobe` imports no `k8s.io/*` and that
   no chainsaw test invokes `curl`.
2. **For humans** — `docs/sources/explanation/testing-strategy.md`: which layer
   covers what, and why E2E is data-path only.
3. **For agents** — a new `CLAUDE.md` carrying the rule and pointing at both.

Only the first holds without discipline; the others explain why it exists.

## Phasing

The work is staged so each phase stands on its own and value lands early rather
than at the end.

1. **Infrastructure.** Calico wiring, path filter, `suite` labels with
   `--selector`, the `e2e-full` label trigger, and the CI check that every test
   carries exactly one label. No new tests: the existing ten simply get labelled
   and keep running. Delivers faster pull requests and a cluster that enforces
   policy, without touching test content.
2. **The probe and the first real test.** `cmd/vinylprobe`, the boundary CI
   check, `docs/sources/explanation/testing-strategy.md` and `CLAUDE.md`, plus
   `cache-and-invalidate`. The documentation belongs here, with the boundary it
   describes, not bolted on later. After this phase the product's central promise
   has genuine coverage for the first time.
3. **The rest of the data path.** `netpol-enforcement`, `xkey-selective`,
   `shard-routing`, `scale-out-participation`, `ha-invalidation-after-failover`,
   `per-backend-routing`, and `exporter-scrape` with the monitoring fixture it
   needs.
4. **Retire the old tests.** Move the invalid-CIDR and forbidden-parameter cases
   into the webhook envtest suite, then delete `vcl-validation`. Add
   `acceptance`, then delete `basic-lifecycle`, `volumes-and-pvc` and
   `drift-detection`. Deletions come last on purpose, so coverage is never
   reduced before its replacement is proven.

## Out of scope, proposed as follow-ups

- **Deepen the envtest suite generally.** Distinct from phase 4, which moves two
  specific cases as a precondition for one deletion. The broader problem is that
  a single `It` block is why E2E had to carry real-API-server coverage at all.
- **A varnish readiness probe** (#26 item 5), still unaddressed.
- Adding an `X-Cache` header to generated VCL. Not needed for testing; if it is
  ever wanted, it should be justified as a product feature.
