# OpenTelemetry Tracing for VinylCache — Idea / Design Sketch

Status: **idea, uncommitted draft** — no implementation planned or promised.
Author: Jens W. Klein (with Claude), 2026-09-21.

## Problem

In a typical VinylCache deployment the request chain is
`ingress → varnish → backend`. Ingress controllers (Traefik, ingress-nginx)
and instrumented backends (e.g. Plone with plone.observability) emit
OpenTelemetry spans natively. Varnish does not: open-source Varnish has **no
way to emit trace spans**. The official
[varnish-otel](https://docs.varnish-software.com/varnish-otel/) exporter
covers metrics and logs for OSS Varnish, but its trace support requires
`vmod-otel` — a **Varnish Enterprise license add-on**. The only community
attempt, [varnish-opentracing](https://github.com/rnburn/varnish-opentracing),
targeted the OpenTracing era and is dead.

Today's best workaround (deployed for aaf on kup6s, 2026-09): Varnish
forwards the W3C `traceparent` request header untouched, so ingress and
backend share one trace, and the ingress span carries the VCL-set `X-Cache`
response header as an attribute (Traefik `--tracing.capturedResponseHeaders`).
The Varnish hop itself is only visible as the *time gap* between the ingress
`ReverseProxy` span and the backend span, and the backend span is parented to
the **ingress** span instead of a Varnish span. Good enough for hit/miss
forensics; not a real trace of the cache tier.

There is no FOSS project filling this gap. Reasons, as far as researchable:
the vendor monetizes exactly this feature; a correct implementation needs an
awkward two-part architecture (see below); out-of-tree vmods suffer VRT ABI
churn every Varnish release; and the pre-existing excellence of `varnishlog`
kept local pain low. That leaves room for cloud-vinyl to provide the first
operator-integrated FOSS solution.

## Why neither VCL nor the log alone can do it

The two capabilities a tracer needs live in different places:

1. **Rewriting trace context** (making the backend a *child of Varnish*
   instead of a child of the ingress) means changing the `traceparent` header
   on `bereq` — possible **only in VCL/vmod**, inside the request path.
2. **Accurate timing and cache semantics** (when did the fetch start, was it
   a hit/miss/grace, how many bytes) live in the **VSL** (Varnish Shared
   memory Log: `Timestamp`, `Hit`, `Link`, `ReqAcct`, … records) — visible
   only *outside* VCL, and after the fact.

Enterprise `vmod-otel` solves this in-process. The FOSS-viable alternative is
a **hybrid**: a small VCL snippet handles (1), an out-of-process VSL reader
handles (2). Crucially, the two need **no explicit side channel**: whatever
`traceparent` VCL puts on `bereq` shows up in the VSL as a `BereqHeader`
record, and the incoming one as `ReqHeader` — the reader learns both the
parent span id and Varnish's own span id for free.

## Proposed architecture

A VCL snippet plus a new, optional **`vinyl-tracer` sidecar**, enabled via a
new CRD field:

```
┌─ varnish pod ───────────────────────────────────────────────┐
│  varnishd ──── shared workdir emptyDir (VSL shm) ────┐      │
│    ▲ VCL snippet (operator-generated):               │      │
│    │  parse traceparent, mint span id,               ▼      │
│    │  rewrite bereq.traceparent                vinyl-tracer │
│    │                                           VSL reader + │
│  vinyl-agent (unchanged, distroless)           OTLP export ─┼─▶ collector
└─────────────────────────────────────────────────────────────┘
```

### Why a separate sidecar and not the vinyl-agent?

The agent is deliberately a **distroless static nonroot** image
(`Dockerfile.agent`: `CGO_ENABLED=0`, `gcr.io/distroless/static`) — it has no
`varnishlog`, no `libvarnishapi`, and should stay that way. More importantly,
the VSL shared-memory layout is **not a stable ABI**: the reader must match
the running `varnishd` version. Building the tracer image `FROM` the same
Varnish base image that `spec.image` uses gives that version lock for free
(varnishlog/libvarnishapi always match varnishd exactly). Tracing also has a
different resource/lifecycle profile than the admin agent (it burns CPU
proportional to traffic; it may be killed/restarted without affecting
invalidation). Hence: `Dockerfile.tracer` = varnish base image + a small Go
binary (`cmd/tracer`).

### Part 1: VCL snippet (operator template)

Injected into the generated VCL (same mechanism as user snippets,
`internal/generator/templates/`) when tracing is enabled:

- `vcl_recv`: validate the incoming `traceparent` (regex per W3C
  trace-context; drop garbage — inbound values are untrusted client input),
  honor its sampled flag.
- `vcl_backend_fetch`: set `bereq.http.traceparent` to
  `00-<trace-id>-<varnish-span-id>-<flags>`, keeping the original trace id.
- Keep the existing practice of setting an `X-Cache`/hit-miss response header
  so upstream proxies retain their attribute.

**Open question — minting the span id.** VCL has no native random-hex.
Options, in order of preference:

a. **Deterministic derivation**: `span-id = first 16 hex chars of
   hash(instance-secret, vxid)`. Needs a hash in VCL — `vmod_digest` from
   [varnish-modules](https://github.com/varnish/varnish-modules) (must verify
   availability in the images we support). Elegant because the tracer could
   *recompute* it from the vxid alone.
b. **A tiny purpose-built vmod** (~100 lines C: `otelhdr.spanid()`), shipped
   in cloud-vinyl-built varnish images. Clean, but buys the VRT-ABI rebuild
   treadmill per Varnish release — acceptable only if we build/pin images
   anyway.
c. `std.random()` assembly hacks — last resort, ugly, slow.

### Part 2: VSL reader + exporter (`vinyl-tracer`)

- Pod change: share the varnishd working directory (`varnishd -n`) between
  the varnish and tracer containers via an `emptyDir` (`Memory` medium) so
  the tracer can attach to the VSL shm; mind file permissions (varnishd
  creates `_.vsm*` for the varnish user/group — run the tracer in the
  varnish group rather than as root).
- A reader consuming transactions in request grouping: spawn and parse
  `varnishlog -g request` (version-stable text interface, subprocess), or
  link `libvarnishapi` via cgo (cheaper per record, needs matching glibc in
  the image — fine, the base is the varnish image). **Open question** which
  one; subprocess first, cgo if parsing cost shows.
- Build spans per transaction and export via OTLP (otel-go SDK, batching,
  bounded queue, drop-on-overflow — the request path must never notice).

**Span construction from VSL records:**

| Span | Start / End | Source records |
|---|---|---|
| `varnish request` (SERVER) | `Timestamp Start` → `Timestamp Resp` | client tx |
| `varnish fetch` (CLIENT) | `Timestamp Bereq` → `Timestamp BerespBody` | backend tx via `Link bereq <vxid> fetch` |

Attributes on the request span: hit/miss/pass/pipe/synth (from `VCL_call`/
`Hit`/`HitPass` records), `obj.hits`, Age/TTL/grace (from `Hit` parameters),
response status, `ReqAcct` byte counts, restarts (`Link req <vxid> restart`),
ESI (`Link req <vxid> esi` → child spans). On the fetch span: backend name
(`BackendOpen` — shows the shard director's pick), status, streaming,
304-revalidation vs full fetch, retries.

### The hard part: correctness under coalescing, grace and ESI

Distributed tracing assumes an RPC tree; Varnish breaks that assumption.
These cases decide whether the traces *tell the truth*:

- **Request coalescing**: N client requests wait on one fetch. The fetch
  span belongs to the trace of the *initiating* request (it carries that
  request's rewritten `traceparent`). The N−1 waiting-list requests get an
  OTel **span link** to the fetch span (the `Hit`/waitinglist records carry
  the fetch vxid), plus a `varnish.coalesced=true` attribute.
- **Grace / background fetch** (`Link bereq <vxid> bgfetch`): the client got
  a stale object; the bgfetch has *no* waiting request. It becomes its own
  root (or child of the triggering request — to decide), linked back.
- **ESI subrequests**: proper child spans of the parent request span.
- **Streaming**: fetch span may end after the request span; readers must not
  assume containment.

A naive implementation that parents every fetch under every client request
would produce actively misleading traces — this correctness work is the real
project, not the OTLP plumbing.

## CRD API sketch

```yaml
spec:
  tracing:
    enabled: true
    otlp:
      endpoint: alloy-traces.monitoring.svc.cluster.local:4317
      protocol: grpc          # grpc | http/protobuf
      insecure: true
    serviceName: vinylcache   # default: VinylCache name
    # tracer sidecar resources analogous to spec.monitoring.exporter
    # sampling: parent-based; no local ratio — leave head/tail
    # sampling to the ingress and the collector pipeline
```

Operator wiring: template the VCL snippet, add the shared workdir volume and
the tracer sidecar (image via `TRACER_IMAGE` env from the Helm chart,
analogous to `AGENT_IMAGE`), pass OTLP config via env. Webhook validates
endpoint/protocol. Follows the established sidecar pattern of
`spec.monitoring.exporter`.

## Phasing

1. **P1 — sibling spans, no VCL rewrite**: tracer-only. Parent the Varnish
   span on the *incoming* `traceparent`; the backend span stays a sibling
   (both children of the ingress span). Hierarchy is slightly flat but
   timing and cache attributes are honest. Zero request-path changes, no
   span-id minting problem. Immediately useful.
2. **P2 — context rewrite**: add the VCL snippet + span-id minting; backend
   becomes a proper child of the Varnish span.
3. **P3 — truth under load**: coalescing links, bgfetch, ESI, restarts,
   overrun/backpressure hardening (VSL overruns must surface as a counter,
   not as silent trace loss).

## Non-goals

- Metrics and logs: covered by the existing prometheus exporter sidecar
  (`spec.monitoring`) and, if ever needed, FOSS varnish-otel.
- Enterprise `vmod-otel` feature parity; in-VCL custom spans.
- Tail-sampling decisions — that stays in the collector (alloy).
- Touching the vinyl-agent: it stays distroless and tracing-free.

## Open questions

1. Span-id minting: vmod_digest availability in supported images vs tiny
   own vmod (option a vs b above).
2. varnishlog subprocess vs libvarnishapi/cgo: parsing stability vs cost;
   what does `-g request` guarantee about ordering/completeness?
3. VSL overrun behavior under high traffic with a slow reader — measure.
4. VSM/VSL access as non-root across the container boundary: exact
   permissions varnishd applies to the workdir, and whether `emptyDir`
   `Memory` medium suffices or the workdir must be `-n`-relocated.
5. `tracestate` passthrough and sampled-flag semantics when the incoming
   request is unsampled but local policy wants the span anyway.
6. Effort: P1 is days; P2 depends on the minting decision; P3 is the
   open-ended correctness tail. An upstream `varnishcache/varnish-cache`
   discussion (native OTel hooks) should be opened before serious work, per
   the no-workaround-without-upstream-report rule.

## References

- varnish-otel (traces = Enterprise vmod-otel): <https://docs.varnish-software.com/varnish-otel/>
- Dead OpenTracing-era attempt: <https://github.com/rnburn/varnish-opentracing>
- W3C Trace Context: <https://www.w3.org/TR/trace-context/>
- OTel span links: <https://opentelemetry.io/docs/concepts/signals/traces/#span-links>
- VSL records: `man vsl` (Timestamp, Link, Hit, ReqAcct, BackendOpen)
- In-repo anchors: `api/v1alpha1/vinylcache_types.go` (`VCLSnippets`,
  `MonitoringSpec`), `internal/generator/templates/`,
  `internal/controller/statefulset.go` (agent sidecar wiring, `AGENT_IMAGE`),
  `Dockerfile.agent` (distroless — why the tracer is a separate image).
