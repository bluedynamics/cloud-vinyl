# OpenTelemetry Tracing for VinylCache: Design

Status: **approved design, pre-implementation**.
Author: Jens W. Klein (with Claude), 2026-09-21.
Supersedes the premise of
[the idea sketch](../plans/2026-09-21-opentelemetry-tracing-idea.md); the
architecture there survives, the landscape section does not.

## Context, corrected

The request chain is `ingress → varnish → backend`. Ingress controllers and
instrumented backends emit OpenTelemetry spans; the cache tier does not, and
today it is visible only as a time gap between the ingress span and the
backend span.

The idea sketch claimed no FOSS project fills this gap. That is outdated:

- The FOSS project formerly known as Varnish Cache renamed itself
  **Vinyl Cache** in early 2026 (trademark dispute with Varnish Software) and
  is at 9.1.0 (2026-09-16). Its development moved off GitHub to a self-hosted
  Forgejo at code.vinyl-cache.org, which is **invite-only** as of 2026-09.
  The `varnishcache/*` GitHub repos are archived; do not consult them for
  current process. Varnish Software continues its own downstream "Varnish
  Cache" separately.
- **VCOT** (gitlab.com/uplex/varnish/VinylCacheOpenTelemetry, GPL-3.0+,
  UPLEX, announced on vinyl-cache.org 2025-11-20) is prior art for the core
  approach: a Go VSL reader exporting spans via OTLP, plus a VCL include that
  validates and mints traceparent headers. It validates the architecture:
  the fork's own core people chose the same out-of-process VSL design.
- VCOT's gaps, verified by code reading on 2026-09-21: no `Link` record
  handling (no coalescing, bgfetch, ESI, restarts), no VSL `Timestamp`-based
  span timing, cgo/libvarnishapi ingestion, alpine/musl image, its VCL needs
  the `blobdigest` vmod (absent from the official varnish images), dormant
  since 2025-12-02, no releases.
- The docker-library `varnish:8.0.2` image cloud-vinyl pins is the vendor
  lineage, and the 8.0 series is EOL. A lineage decision exists independent
  of tracing.

## Decisions

Settled 2026-09-21, in conversation:

1. **Scope: P1+P2+P3 complete before publication.** Sibling-span basics,
   trace-context rewrite, and truth-under-load (coalescing/grace/ESI) all
   ship before we show the world. The correctness tail is a requirement, not
   a stretch goal.
2. **Build our own `vinyl-tracer`** (Apache-2.0, this repo, our conventions),
   crediting VCOT as prior art. No GPL code is copied, VCL included; our
   snippet is written fresh against vmods our images actually ship.
   Rationale: P3 would rewrite most of VCOT anyway, and VCOT is dormant with
   unknown review latency. Known risk: second-FOSS-tracer optics in a small
   community; mitigated by explicit credit and an upstream note at
   publication.
3. **Engine decoupled: `varnish:8.0.2` baseline now**, tracer image
   version-locked via a `VARNISH_IMAGE` build-arg (exporter pattern), so a
   vinyl 9.x switch is a build-arg change. The lineage migration is its own
   follow-up project. The fork publishes no official container images; going
   to 9.x means third-party or self-built images.
4. **Ingestion: `varnishlog -g request` subprocess, not cgo.** Text interface
   is stable across the supported 7.6–9.0 window; `CGO_ENABLED=0` stays; the
   ingestion layer is swappable if parsing cost ever shows.
5. **Upstream engagement at publication, not before** (build-first, decided
   deliberately against the report-first rule for this case). Venue: a public
   issue on the VCOT GitLab repo (credit, learnings, link); the vinyl-cache
   Forgejo is not accessible to us. Announcement post plus Mastodon mention.

## Architecture

```
┌─ varnish pod ─────────────────────────────────────────────────┐
│  varnishd ── varnish-workdir emptyDir (VSL shm) ──┐           │
│    ▲ VCL snippet (operator-generated):            ▼ (ro)      │
│    │  validate traceparent, mint fetch      vinyl-tracer      │
│    │  span id on bereq                      varnishlog -g     │
│    │                                        request → spans   │
│  vinyl-agent (unchanged)                    → OTLP export ────┼─▶ collector
│  vinyl-exporter (unchanged)                 → /metrics        │
└───────────────────────────────────────────────────────────────┘
```

### vinyl-tracer (`cmd/tracer`)

- `internal/tracer/vsl`: parser, `io.Reader` → stream of transaction groups
  (type, vxid, parent link, records). Pure; tested against golden fixtures
  recorded from real varnishd output.
- `internal/tracer/spans`: transaction group → spans. All truth semantics
  live here. Request span (SERVER) from `Timestamp Start → Resp`; fetch span
  (CLIENT) from `Timestamp Bereq → BerespBody`. Attributes: hit/miss/pass/
  pipe/synth, `obj.hits`, Age/TTL/grace, status, `ReqAcct` bytes, backend
  name from `BackendOpen`, streaming, 304 revalidation, retries.
- `internal/tracer/export`: spans carry foreign ids and explicit timestamps,
  so the OTel SDK tracer (which mints its own ids) is bypassed; we feed the
  `otlptrace` gRPC/HTTP exporter directly through our own bounded batcher.
  Fixed-size queue, drop on overflow. Drops, VSL overruns, and reattach
  counts are exposed on a small `/metrics` endpoint. The request path must
  never notice the tracer; trace loss must be visible, never silent.
- Supervision: varnishlog exits when varnishd restarts (VSM abandoned);
  reattach with backoff, counted, never crash-looping the pod.
- Config via env/flags set by the operator: OTLP endpoint, protocol,
  insecure, service name.

### VCL snippet (operator template)

Injected via the existing snippet mechanism when tracing is enabled:

- `vcl_recv`: validate incoming `traceparent` against the W3C regex, unset
  garbage (untrusted client input). No rewrite here; the original stays
  visible to the tracer for parenting.
- `vcl_backend_fetch`: mint a span id with `vmod_uuid` (present in
  `varnish:8.0.2`): `uuid_v4()`, dashes stripped, first 16 hex chars. Set
  `bereq.http.traceparent` keeping trace id and flags. No incoming
  traceparent → mint a full trace id too and self-root.
- The existing `X-Cache` response-header practice stays, so ingress
  attributes keep working.

### Trace model

The minted bereq span id **is the fetch span's id**; that is what the
backend parents to. The tracer reads `ReqHeader traceparent` (parent span +
sampled flag) and `BereqHeader traceparent` (fetch span id) from the VSL and
mints the request span's id itself.

Hierarchy: ingress span → varnish request span (SERVER) → varnish fetch
span (CLIENT) → backend span.

Truth semantics:

| Case | Treatment |
|---|---|
| Coalescing | Fetch span belongs to the initiating request's trace. Waiting requests get a span link to it (fetch vxid from `Hit`/waitinglist records), resolved via a bounded TTL cache of fetch vxid → (trace id, span id). Cache miss degrades to `varnish.coalesced=true` without a link, counted. |
| Grace / bgfetch | Child of the triggering request span, `varnish.bgfetch=true`. May outlive its parent; that is truthful. |
| ESI | Child spans per `Link req <vxid> esi`, recursively. |
| Restarts | Child spans under the original request span, `varnish.restarts` counted. |
| Pipe / synth | Request span with `varnish.pipe` / `varnish.synthetic`; no fetch span pretensions. |
| Streaming | Fetch span may end after the request span; no containment assumption. |

Sampling: parent-based only. Unsampled incoming → parse, count, export
nothing. Self-rooted → sampled. Ratio and tail sampling stay in the
collector.

## CRD API and operator wiring

```yaml
spec:
  tracing:
    enabled: true
    otlp:
      endpoint: alloy-traces.monitoring.svc:4317
      protocol: grpc            # grpc | http/protobuf, default grpc
      insecure: true
    serviceName: vinylcache     # default: the VinylCache name
    resources: {}               # like spec.monitoring.exporter
```

- Defaulter fills protocol and serviceName; validator requires a non-empty
  endpoint when enabled and checks host:port shape.
- `buildTracerContainer` alongside `buildExporterContainer`: image from
  `TRACER_IMAGE` env (Helm chart, like `AGENT_IMAGE`), `varnish-workdir`
  mounted read-only, OTLP config via env, nonroot / read-only-rootfs /
  no-privilege-escalation security context. No new RBAC; the tracer never
  touches the Kubernetes API.
- VCL generator input grows a tracing flag that injects the snippet.
- NetworkPolicy: the tracer's OTLP egress (4317/4318, user-chosen endpoint)
  needs an explicit egress rule in the generated policies. This is the known
  blind-spot area (#28, #56, #58); it gets dedicated E2E coverage.

## Images

`Dockerfile.tracer` follows `Dockerfile.exporter` exactly: static Go build
stage; a `FROM ${VARNISH_IMAGE}` stage as the source of a matching
`varnishlog` plus `libvarnishapi.so.3*` and its libraries, with the same
soname build guard (#91); runtime `gcr.io/distroless/base-debian13:nonroot`
(base, not static: varnishlog needs glibc). Published as
`ghcr.io/bluedynamics/vinyl-tracer:<version>`.

## Testing

- **Unit** (the bulk): parser and span builder on golden fixtures recorded
  from real `varnishlog -g request` output, never hand-written. One fixture
  per truth case: hit, miss, coalescing, bgfetch, ESI, restart, pipe, synth,
  unsampled, garbage traceparent, truncated/overrun output, streaming.
  Operator side: fake-client tests for container/volume/env wiring and VCL
  snippet rendering.
- **envtest**: webhook defaulting and validation of `spec.tracing`; adds
  real admission assertions to a currently near-stub layer.
- **E2E**: `vinylprobe` grows two HTTP-only modes, preserving the enforced
  boundary (no `k8s.io/*` imports in vinylprobe, no curl in chainsaw):
  1. `otlp-sink`: an OTLP/http-protobuf receiver holding recent spans in
     memory;
  2. `assert-spans`: queries the sink over HTTP and exits 0/1, run by
     chainsaw as a Job like existing probes.
  E2E uses `http/protobuf`; the gRPC exporter path is unit-covered. Cluster
  tests: honest timing against a delayed backend, one fetch + N links under
  concurrent cold-cache load, grace/bgfetch, ESI children, NetworkPolicy
  egress. Coalescing and grace go in the `full` suite. Assertions poll the
  sink with timeouts; no sleeps.

## Build order

All before publication:

1. VSL parser + span builder against golden fixtures (pure core).
2. Tracer binary, `Dockerfile.tracer`, operator + chart wiring, basic E2E
   (P1 semantics: sibling spans, honest timing).
3. VCL snippet + webhook validation (P2: proper parent-child).
4. Truth cases + full E2E matrix (P3).
5. Docs, demo, publication.

## Publication deliverables

- Docs: how-to (enable tracing) and explanation (the trace model, telling
  the coalescing/grace story honestly), per ecosystem docs conventions.
- A real trace from the aaf/kup6s deployment: ingress → varnish → Plone in
  one trace, as the demo.
- Announcement post; venue and timing open (blog, Plone Conf window).
- Upstream note: public issue on the VCOT GitLab repo with credit and
  learnings. The vinyl-cache Forgejo is invite-only; no issue there.

## Non-goals

- Metrics and logs (existing exporter sidecar covers metrics).
- Enterprise `vmod-otel` feature parity; in-VCL custom spans.
- Local sampling ratios or tail sampling (collector's job).
- Touching vinyl-agent (stays distroless-static and tracing-free).
- The vinyl 9.x lineage migration (separate project).

## Open items

- Exact varnishlog text-format details (indent markers, notice records) to
  be pinned down in step 1 with the recorded fixtures.
- Shape of the NetworkPolicy egress rule (endpoint-scoped vs. port-scoped).
- Whether `assert-spans` needs a small query language or a fixed flag set
  suffices; decide in step 2 with the first real assertions.
- Announcement venue and timing.
