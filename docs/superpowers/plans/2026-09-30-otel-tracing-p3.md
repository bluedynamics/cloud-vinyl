# OpenTelemetry Tracing P3 (truth under load) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The traces tell the truth under load: coalesced hits carry span
links to the originating fetch, grace hits show their background fetch,
ESI and restarted requests appear as child spans, retries emit one fetch
span per attempt, pipe and synth stop masquerading as misses, and trace
loss counters stop conflating unsampled traffic with data loss.

**Architecture:** Fixture-truth-first: Task 1 records real VSL for every
truth case with an upgraded rig (scriptable backend: delay, ESI markup,
flaky, short-TTL) and freezes the observed record shapes in a NOTES file
that later tasks reconcile against. `spans.Build` learns to walk the tx
tree recursively and returns spans plus a build outcome; a new stateful
`spans.Linker` wraps it with a bounded fetch-vxid → span-identity cache
that turns `Hit <vxid>` records into span links and detects coalescing by
time overlap. Links and timestamps flow through `roSpan`, the sink, and
new vinylprobe assertion flags; E2E proves coalescing, grace, and honest
timing in the `full` suite.

**Tech Stack:** Go 1.26, recorded varnishlog fixtures (varnish:8.0.2),
otel-go sdktrace.Link, chainsaw E2E (full suite), ealen/echo-server or a
scripted python backend for traffic shaping.

**Spec:** `docs/superpowers/specs/2026-09-21-opentelemetry-tracing-design.md`
— the Truth semantics table (lines 125-134) is the binding contract for
this plan; Task 10 syncs the table where implementation reality refines it.
P1 = PR #107, P2 = PR #108, both merged.

## Global Constraints

- Module `github.com/bluedynamics/cloud-vinyl`, `go 1.26.7`. Worktree `sources/cloud-vinyl-wt/feat/otel-tracing-p3`, branch `feat/otel-tracing-p3`. Never touch `sources/cloud-vinyl/`.
- VSL fixtures are RECORDED from real `varnish:8.0.2` (docker, local `default` context), never hand-written or hand-edited. All existing P1/P2 fixtures and tests keep passing untouched.
- **Fixture truth outranks this plan's code sketches.** Task 1's `NOTES.md` documents the observed record shapes (Link payloads, nesting depths, Timestamp labels, Hit payload fields); where a later task's sketch disagrees with NOTES.md, fix the code to match the fixtures and say so in the task report. Never "fix" a fixture.
- The recording rig's embedded VCL mirrors the generator templates' tracing blocks (ruled keep-in-sync duplication); scenario-specific VCL (restart/pipe/retry rules, do_esi) exists ONLY in the rig unless a task explicitly adds it to a template.
- cmd/vinylprobe stays k8s-free (hack/check-e2e-boundary.sh); chainsaw tests contain no `curl`/`wget` substrings anywhere; exactly one `suite:` label per test; coalescing/grace/ESI E2E go in `suite: full` (runs on push, dispatch, or the `e2e-full` PR label).
- vinylprobe exit codes: 0 pass, 1 assertion failure, 2 usage/transport. `lll` 120 chars on `cmd/*`, help strings in the const block.
- Tests: plain `testing` + testify (cmd/vinylprobe tests no-testify; webhook envtest suite untouched). TDD with RED/GREEN evidence; `make lint` (golangci-lint v2.13.1) before each commit.
- No changelog file exists; do not invent one. Commits end with `Assisted-by: Claude Fable 5` (never Co-Authored-By, never a noreply address).
- The request path must never notice the tracer; every new degradation (cache miss, unusable group) is a visible counter, never silence.

---

### Task 1: Recording rig v3 + truth fixtures + NOTES.md

**Files:**
- Create: `internal/tracer/vsl/testdata/record-fixtures-truth.sh`
- Create: `internal/tracer/vsl/testdata/truthbackend.py`
- Create: `internal/tracer/vsl/testdata/NOTES.md`
- Create (recorded): `internal/tracer/vsl/testdata/{coalesce,grace_bgfetch,esi,restart,retry,pipe,synth}.txt`
- Modify: `internal/tracer/vsl/testdata/README.md`

**Interfaces:**
- Produces: seven recorded fixtures every later task's tests parse, and
  `NOTES.md`, the authoritative catalog of observed VSL shapes. Later
  tasks reconcile their code against NOTES.md (global constraint).

- [ ] **Step 1: Write the backend.** `truthbackend.py` — stdlib-only HTTP
server the rig runs inside a `python:3.12-slim` container:

```python
"""Traffic-shaping backend for VSL truth-fixture recording. Endpoints:
/plain            -> 200 "hello"
/slow             -> 200 after a 2s sleep (coalescing window)
/shortttl         -> 200 with Cache-Control: max-age=1 (grace/bgfetch)
/esi              -> 200 ESI markup + Surrogate-Control: content="ESI/1.0"
/frag             -> 200 "FRAGMENT"
/flaky            -> 500 on the first call, 200 afterwards (retry)
/teapot           -> 418 (restart trigger for the rig VCL)
anything else     -> 200 with the path echoed
"""
import http.server
import threading
import time

hits = {}
lock = threading.Lock()


class H(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_GET(self):  # noqa: N802 - stdlib API name
        with lock:
            n = hits.get(self.path, 0) + 1
            hits[self.path] = n
        body = b"hello"
        status, headers = 200, {}
        if self.path.startswith("/slow"):
            time.sleep(2)
        elif self.path.startswith("/shortttl"):
            headers["Cache-Control"] = "max-age=1"
        elif self.path.startswith("/esi"):
            body = b'before <esi:include src="/frag"/> after'
            headers["Surrogate-Control"] = 'content="ESI/1.0"'
        elif self.path.startswith("/frag"):
            body = b"FRAGMENT"
        elif self.path.startswith("/flaky"):
            if n == 1:
                status, body = 500, b"boom"
        elif self.path.startswith("/teapot"):
            status, body = 418, b"teapot"
        else:
            body = self.path.encode()
        self.send_response(status)
        for k, v in headers.items():
            self.send_header(k, v)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *a):  # keep container logs quiet
        pass


http.server.ThreadingHTTPServer(("", 8000), H).serve_forever()
```

- [ ] **Step 2: Write the rig.** `record-fixtures-truth.sh`, modeled on
`record-fixtures-tracing.sh` (same cleanup/trap/record patterns, network
`vsl-fixtures-p3`, containers `vslp3-varnish`/`vslp3-backend`). The
embedded `default.vcl` = the P2 script's VCL (verbatim tracing blocks,
keep-in-sync comment) PLUS scenario rules:

```
sub vcl_recv {
    # ... P2 traceparent validation block verbatim ...
    if (req.url ~ "^/pipe") { return (pipe); }
    if (req.url ~ "^/synth") { return (synth(410, "gone")); }
}
sub vcl_deliver {
    # restart once when the backend serves a teapot
    if (resp.status == 418 && req.restarts == 0) { return (restart); }
}
sub vcl_backend_fetch {
    # ... P2 minting block verbatim ...
}
sub vcl_backend_response {
    if (beresp.http.Surrogate-Control ~ "ESI/1.0") {
        set beresp.do_esi = true;
    }
    if (beresp.status == 500 && bereq.retries == 0) { return (retry); }
    if (beresp.ttl <= 0s) { set beresp.ttl = 60s; }
    set beresp.grace = 1h;
}
```

Backend container: `docker run -d --name vslp3-backend --network "$NET" -v "$PWD/truthbackend.py:/b.py:ro" python:3.12-slim python /b.py`.
The `req` helper is the P2 script's, plus a parallel variant:

```bash
req2parallel() { # two concurrent requests to the same path
  req "$1" & req "$1" & wait
}
```

Recording sequence (each scenario preceded by `docker restart vslp3-varnish; sleep 2`):

```bash
req2parallel /slow;                       record coalesce.txt
# grace: prime, let TTL lapse, hit stale (triggers bgfetch), settle
req /shortttl; sleep 2; req /shortttl; sleep 1;   record grace_bgfetch.txt
req /esi;                                 record esi.txt
req /teapot;                              record restart.txt
req /flaky;                               record retry.txt
req /pipe;                                record pipe.txt
req /synth;                               record synth.txt
```

- [ ] **Step 3: Run it; verify each fixture's shape by eye and write NOTES.md.**
This is the STOP-gate. For each fixture record in NOTES.md, with quoted
lines: the group/nesting structure (`*`/`**`/`***` depths and Begin
payloads), every `Link` record verbatim (esi/restart/retry/bgfetch/fetch
reasons and their vxids), the `Hit` record payload fields in the
coalesced waiter and the grace hit, which `Timestamp` labels exist for
pipe (`Resp` absent? `PipeSess`?), whether ESI subrequests nest as child
Request groups or surface as separate top-level groups under `-g
request`, where the retried bereq sits (sibling child vs nested under the
first bereq), and each bereq's minted `BereqHeader traceparent`.
Expectations that MUST hold or you STOP and report (DONE_WITH_CONCERNS /
BLOCKED with the actual output): coalesce.txt has two Request groups and
exactly ONE fetch between them; esi.txt shows a subrequest for /frag;
retry.txt shows two backend attempts; pipe.txt's client group lacks a
plain `Resp` timestamp. If ESI produces no subrequest, retry with
varnishd feature flag `+esi_disable_xml_check` noted, or investigate the
vmod-esi route (`import esi`) — document whichever incantation worked,
because Task 7 mirrors it into the generator.

- [ ] **Step 4: Extend README.md** (one paragraph: truth fixtures, the
rig, NOTES.md's role as the shape catalog).

- [ ] **Step 5: Commit**

```bash
git add internal/tracer/vsl/testdata
git commit -m "test(tracer): record truth fixtures — coalesce, grace, esi, restart, retry, pipe, synth"
```

---

### Task 2: `vsl.Last` helper

**Files:**
- Modify: `internal/tracer/vsl/vsl.go` (next to `First`, line ~119)
- Test: `internal/tracer/vsl/vsl_test.go`

**Interfaces:**
- Produces: `func (t *Tx) Last(tag string) (string, bool)` — payload of
  the LAST record with that tag (status codes are rewritten by restarts/
  revalidation; the delivered value is the last one logged).

- [ ] **Step 1: Failing test** against a real fixture whose status is
rewritten — restart.txt (418 then the restarted delivery) per NOTES.md;
if no recorded fixture has two same-tag records with different payloads,
use the in-memory-mutation pattern from the garbage-lines test (inject a
duplicated record into parsed content, never the file):

```go
func TestLast_ReturnsFinalRecord(t *testing.T) {
	txs := parseAll(t, "restart.txt")
	require.NotEmpty(t, txs)
	// NOTES.md: the client group logs RespStatus twice on restart
	// (pre-restart 418, delivered 200). Reconcile with the fixture.
	first, ok := txs[0].First("RespStatus")
	require.True(t, ok)
	last, ok := txs[0].Last("RespStatus")
	require.True(t, ok)
	assert.NotEqual(t, first, last, "restart must rewrite the delivered status")
	assert.Equal(t, "200", last)
}
```

- [ ] **Step 2: RED, then implement:**

```go
// Last returns the payload of the last record with the given tag. Where a
// value is rewritten during processing (a restarted request's RespStatus,
// a revalidated BerespStatus), the last record is the delivered truth.
func (t *Tx) Last(tag string) (string, bool) {
	for i := len(t.Records) - 1; i >= 0; i-- {
		if t.Records[i].Tag == tag {
			return t.Records[i].Payload, true
		}
	}
	return "", false
}
```

- [ ] **Step 3: GREEN (full vsl package), lint, commit** (`feat(tracer): vsl.Last for rewritten-record truth`).

---

### Task 3: Recursive span building — retries, bgfetch, ESI, restarts

**Files:**
- Modify: `internal/tracer/spans/spans.go` (Build ~82-181, fetchAttrs ~232)
- Test: `internal/tracer/spans/spans_test.go`

**Interfaces:**
- Consumes: Task 1 fixtures + NOTES.md; `vsl.Last` (Task 2).
- Produces (Tasks 5/6 rely on):
  - `Build(tx, ids)` unchanged signature; now emits spans for the WHOLE
    tx tree: every descendant BeReq (retries: one fetch span per attempt,
    `varnish.retry=<n>` for n≥1, parent = the span owning that bereq),
    bgfetch bereqs (`varnish.bgfetch=true`, child of the triggering
    request span), ESI subrequest Request groups (`varnish.esi=true`,
    SpanKindServer name "varnish request", child of the parent request
    span, recursively), restarted Request children (child span of the
    original request span; the TOP request span gains
    `varnish.restarts=<count>`).
  - Fetch-span identity rule unchanged: adopt the bereq's own minted
    `BereqHeader traceparent` span id (each attempt/bgfetch minted its
    own in vcl_backend_fetch).
  - Multi-minted-trace-id note (spans.go:107-112) resolved: the top
    request's trace id still comes from incoming-or-first-minted; every
    descendant span uses THE TOP span's trace id (one group, one trace) —
    a descendant bereq whose minted trace id differs (self-root bgfetch
    minting fresh) still emits under the group's trace id, with
    `varnish.minted_trace_mismatch=true` when they differ, so the
    backend-side orphan is at least visible. Update the comment.

- [ ] **Step 1: Failing tests, one per fixture** (all reading expectations
from the fixture via the existing `mintedTraceparent`-style helpers,
reconciled with NOTES.md — the assertions below state the CONTRACT; exact
child counts come from the fixtures):

```go
func TestBuild_RetryEmitsOneFetchSpanPerAttempt(t *testing.T) {
	txs := fixtureTxs(t, "retry.txt")
	got := Build(txs[0], &seqIDs{})
	fetches := spansNamed(got, "varnish fetch")
	require.Len(t, fetches, 2, "retry: two backend attempts, two fetch spans")
	assert.Equal(t, "500", attrString(t, fetches[0].Attrs, "http.response.status_code"))
	assert.Equal(t, "200", attrString(t, fetches[1].Attrs, "http.response.status_code"))
	assert.Equal(t, "1", attrString(t, fetches[1].Attrs, "varnish.retry"))
	req := spansNamed(got, "varnish request")[0]
	for _, f := range fetches {
		assert.Equal(t, req.TraceID, f.TraceID)
	}
}

func TestBuild_ESISubrequestBecomesChildSpan(t *testing.T) {
	txs := fixtureTxs(t, "esi.txt")
	got := Build(txs[0], &seqIDs{})
	reqs := spansNamed(got, "varnish request")
	require.GreaterOrEqual(t, len(reqs), 2, "parent + at least one ESI child")
	child := reqs[1]
	assert.Equal(t, "true", attrString(t, child.Attrs, "varnish.esi"))
	assert.Equal(t, reqs[0].SpanID, child.ParentID)
	assert.Equal(t, reqs[0].TraceID, child.TraceID)
}

func TestBuild_RestartChildAndCount(t *testing.T) {
	txs := fixtureTxs(t, "restart.txt")
	got := Build(txs[0], &seqIDs{})
	top := got[0]
	assert.Equal(t, "1", attrString(t, top.Attrs, "varnish.restarts"))
	assert.Equal(t, "200", attrString(t, top.Attrs, "http.response.status_code"),
		"delivered status is the post-restart one (vsl.Last)")
}

func TestBuild_GraceHitEmitsBgfetchSpan(t *testing.T) {
	txs := fixtureTxs(t, "grace_bgfetch.txt")
	// NOTES.md names which group is the stale hit; find it by the
	// bgfetch child rather than position.
	var hitSpans []Span
	for _, tx := range txs {
		s := Build(tx, &seqIDs{})
		hitSpans = append(hitSpans, s...)
	}
	fetches := spansNamed(hitSpans, "varnish fetch")
	var bg *Span
	for i := range fetches {
		if attrHas(fetches[i].Attrs, "varnish.bgfetch") {
			bg = &fetches[i]
		}
	}
	require.NotNil(t, bg, "the stale hit must carry a bgfetch fetch span")
}
```

Helpers `spansNamed`/`attrHas` are small test-local additions. Write them
plus RED evidence, then implement.

- [ ] **Step 2: Implement.** Restructure Build's tail into a recursive
walk (`buildChildren(parent *Span, tx *vsl.Tx, ...)`) emitting: BeReq →
fetch span (existing logic + `varnish.retry` when NOTES.md's retry shape
identifies attempt ordinals — `Begin` payload or nesting; +
`varnish.bgfetch` existing check; status via `vsl.Last`), nested BeReq
under a BeReq (retry nesting, if that is the recorded shape) → fetch span
parented to the outer FETCH span, child Request groups (Begin "… esi") →
ESI request spans recursively, child Request groups (Begin "… restart" or
the restart Link shape per NOTES.md) → restart child spans, counting into
the top span's `varnish.restarts`. Request-span statuses switch to
`vsl.Last("RespStatus")`; fetch spans to `Last("BerespStatus")`. Keep the
unsampled early-return and the P1/P2 fixtures byte-identical in behavior
(run the full suite).

- [ ] **Step 3: GREEN (whole ./internal/tracer/...), lint, commit** (`feat(tracer): recursive truth spans — retry, bgfetch, esi, restart`).

---

### Task 4: Pipe and synth truth + build outcomes

**Files:**
- Modify: `internal/tracer/spans/spans.go` (handling ~210, Build entry)
- Modify: `cmd/tracer/main.go` (handle closure ~76-94), `cmd/tracer/supervise.go` (metrics ~17-24)
- Test: `internal/tracer/spans/spans_test.go`, `cmd/tracer/supervise_test.go` if touched

**Interfaces:**
- Produces:
  - `type Outcome int` with `OutcomeSpans`, `OutcomeUnsampled`,
    `OutcomeUnusable`; `func BuildWithOutcome(tx *vsl.Tx, ids IDSource) ([]Span, Outcome)`;
    `Build` kept as a thin wrapper returning only spans (existing callers/tests untouched).
  - Pipe: request span from `Timestamp Start` → the label NOTES.md shows
    for pipe teardown (expected `PipeSess`; reconcile), `varnish.handling="pipe"`
    (new `case "PIPE"` — or the VCL_return/BereqBegin evidence per
    NOTES.md), `varnish.pipe=true`, no fetch span. Pipe is OutcomeSpans,
    not unusable.
  - Synth: existing "synth" handling + `varnish.synthetic=true` attr.
  - Metrics: `vinyl_tracer_groups_unsampled_total` (new, Help "Request
    groups skipped because the incoming trace context is unsampled.");
    `vinyl_tracer_groups_unusable_total` Help reworded to truly mean
    truncated/unusable; handle closure switches on Outcome.

- [ ] **Step 1: Failing tests** — pipe fixture yields exactly one span
with `varnish.pipe=true` and handling "pipe" and a nonzero duration;
synth fixture yields `varnish.synthetic=true` + handling "synth";
`BuildWithOutcome` returns OutcomeUnsampled for the in-memory
flags-00-mutated fixture and OutcomeUnusable for a Start-less mutation.

- [ ] **Step 2: Implement; wire the handle closure:**

```go
		handle: func(tx *vsl.Tx) {
			built, outcome := spans.BuildWithOutcome(tx, ids)
			switch outcome {
			case spans.OutcomeUnsampled:
				groupsUnsampled.Inc()
			case spans.OutcomeUnusable:
				groupsUnusable.Inc()
			}
			for _, sp := range built {
				batcher.Enqueue(sp)
			}
		},
```

- [ ] **Step 3: GREEN (tracer packages + cmd/tracer), lint, commit** (`feat(tracer): pipe/synth truth and split skip counters`).

---

### Task 5: Links — `spans.Linker` with the fetch-identity cache

**Files:**
- Modify: `internal/tracer/spans/spans.go` (Span struct)
- Create: `internal/tracer/spans/linker.go`
- Test: `internal/tracer/spans/linker_test.go`
- Modify: `cmd/tracer/main.go` (handle closure), `cmd/tracer/supervise.go` (one new counter)

**Interfaces:**
- Produces (Task 6 relies on):
  - `Span.Links []Link` with `type Link struct { TraceID trace.TraceID; SpanID trace.SpanID; Attrs []attribute.KeyValue }`.
  - `func NewLinker(capacity int, ttl time.Duration, ids IDSource) *Linker`
    and `func (l *Linker) Build(tx *vsl.Tx) ([]Span, Outcome)` — wraps
    BuildWithOutcome; after building, records every fetch span's
    (fetchVXID → traceID, spanID, endTime) in a bounded LRU with TTL
    (fetch vxid = the BeReq child's `Tx.VXID`); for every HIT request
    span whose `Hit <vxid> …` record resolves in the cache, appends a
    Link to the originating fetch (`varnish.link="origin-fetch"` attr on
    the link) — every hit links to the fetch that created its object.
    Coalescing detection: when the hit request's Start precedes the
    cached fetch's endTime, the request overlapped the in-flight fetch →
    add `varnish.coalesced=true` to the hit request span. Cache miss →
    `varnish.coalesced_unlinked=true` when the Hit vxid is absent AND the
    overlap cannot be established, plus `vinyl_tracer_link_cache_misses_total`.
  - Single-goroutine use (the supervisor calls handle serially) — no
    mutex, documented.
- Consumes: `Hit` record payload field layout from NOTES.md (first field
  = originating fetch vxid; reconcile).

- [ ] **Step 1: Failing tests** against coalesce.txt and grace fixtures:

```go
func TestLinker_CoalescedHitLinksToOriginFetch(t *testing.T) {
	txs := fixtureTxs(t, "coalesce.txt")
	require.Len(t, txs, 2)
	l := NewLinker(1024, time.Minute, &seqIDs{})
	var all []Span
	for _, tx := range txs {
		s, _ := l.Build(tx)
		all = append(all, s...)
	}
	fetches := spansNamed(all, "varnish fetch")
	require.Len(t, fetches, 1, "coalescing: one fetch for two requests")
	reqs := spansNamed(all, "varnish request")
	require.Len(t, reqs, 2)
	var waiter *Span
	for i := range reqs {
		if attrHas(reqs[i].Attrs, "varnish.coalesced") {
			waiter = &reqs[i]
		}
	}
	require.NotNil(t, waiter, "one request waited on the in-flight fetch")
	require.Len(t, waiter.Links, 1)
	assert.Equal(t, fetches[0].SpanID, waiter.Links[0].SpanID)
	assert.Equal(t, fetches[0].TraceID, waiter.Links[0].TraceID)
}

func TestLinker_PlainLaterHitLinksWithoutCoalescedFlag(t *testing.T) {
	txs := fixtureTxs(t, "miss_then_hit.txt") // P1 fixture: sequential
	l := NewLinker(1024, time.Minute, &seqIDs{})
	var all []Span
	for _, tx := range txs {
		s, _ := l.Build(tx)
		all = append(all, s...)
	}
	reqs := spansNamed(all, "varnish request")
	require.Len(t, reqs, 2)
	hit := reqs[1]
	require.Len(t, hit.Links, 1, "every hit links to its origin fetch")
	assert.False(t, attrHas(hit.Attrs, "varnish.coalesced"),
		"a hit after the fetch completed did not coalesce")
}
```

(The ordering assumption — waiter processed after the fetch's group — is
what `-g request` guarantees by completion order; reconcile with the
recorded coalesce.txt group order per NOTES.md, and if the waiter's group
completes FIRST, the cache will miss for it: then the test asserts the
degradation attrs instead and the linker also handles the
fetch-arrives-later case by keeping a small pending-hits buffer ONLY if
NOTES.md proves that ordering actually occurs — do not build the buffer
speculatively.)

- [ ] **Step 2: Implement linker.go** (bounded LRU: a map + ring of vxids,
evict oldest beyond capacity, drop entries older than ttl on insert;
~80 lines, no external deps). Wire `cmd/tracer/main.go`:

```go
	linker := spans.NewLinker(4096, 5*time.Minute, spans.NewRandomIDs())
	s := &supervisor{
		...
		handle: func(tx *vsl.Tx) {
			built, outcome := linker.Build(tx)
			...
		},
	}
```

- [ ] **Step 3: GREEN (tracer + cmd/tracer), lint, commit** (`feat(tracer): hit-to-fetch span links with coalescing detection`).

---

### Task 6: Links and timestamps on the wire — export, sink, probe

**Files:**
- Modify: `internal/tracer/export/readonly.go` (Links() line ~52)
- Test: `internal/tracer/export/batcher_test.go`
- Modify: `internal/probe/otlp.go` (SpanSummary, receive)
- Test: `internal/probe/otlp_test.go`
- Modify: `cmd/vinylprobe/main.go` (flags, decideSpans, help consts)
- Test: `cmd/vinylprobe/main_test.go`

**Interfaces:**
- Produces (Tasks 8/9 rely on):
  - `roSpan.Links()` maps `spans.Span.Links` → `[]sdktrace.Link` (SpanContext from TraceID/SpanID + FlagsSampled, Attributes passed through).
  - `SpanSummary` gains `Links []LinkSummary` (`type LinkSummary struct { TraceID string `json:"traceID"`; SpanID string `json:"spanID"` }`) and `StartUnixNano, EndUnixNano int64` (json `startUnixNano`/`endUnixNano`), filled from `sp.GetLinks()`, `sp.GetStartTimeUnixNano()`, `sp.GetEndTimeUnixNano()`.
  - vinylprobe `-assert-spans` gains `-link-span-id <hex>` (a matching span must carry a link whose SpanID equals it) and `-min-duration <duration>` (EndUnixNano-StartUnixNano ≥ it); both filter inside `decideSpans` (signature grows: `..., linkSpanID string, minDuration time.Duration, ...` — update all call sites/tests), both require `-assert-spans` in validateSpanModes, help strings in the const block.

- [ ] **Step 1: TDD each layer in order** (export: batcher test asserting
`mem.GetSpans()[0].Links` round-trips a linked span; sink: post a proto
span with one link + timestamps, assert the summary; cmd: decideSpans
filter tests in no-testify style — link match, link mismatch, duration
threshold pass/fail). The sink test builds the link with the real
`tracepb.Span_Link` type (counterpart-true).

- [ ] **Step 2: Implement all three layers; run the three packages + whole repo; boundary check** (`bash hack/check-e2e-boundary.sh` → OK, evidence).

- [ ] **Step 3: Lint, commit** (`feat(tracer): span links and timings through export, sink, and assertions`).

---

### Task 7: Generator — ESI processing gate

**Files:**
- Modify: `internal/generator/templates/vcl_backend_response.vcl.tmpl`
- Test: `internal/generator/generator_test.go`

**Interfaces:**
- Produces: when `.HasESI`, vcl_backend_response gains (before the
  snippet block, matching Task 1's rig incantation — reconcile with what
  NOTES.md proved actually produces subrequests):

```
{{- if .HasESI }}

    # ESI processing for backends that declare it (Surrogate-Control per
    # W3C ESI 1.0). Only meaningful with the esi feature flag enabled.
    if (beresp.http.Surrogate-Control ~ "ESI/1.0") {
        set beresp.do_esi = true;
    }
{{- end }}
```

- [ ] **Step 1: Failing tests**: `TestGenerate_ESI_EnablesDoESIOnSurrogateControl`
(HasESI via `VarnishParams["feature +esi"]`, assert both `do_esi` and the
Surrogate-Control guard render) and `TestGenerate_NoESI_NoDoESI` (absent otherwise).
- [ ] **Step 2: Implement; full generator suite green (Determinism/AllSubroutines included); lint, commit** (`feat(generator): gate ESI processing on Surrogate-Control when esi is enabled`).

---

### Task 8: E2E — coalescing, grace, honest timing (full suite)

**Files:**
- Create: `e2e/tests/tracing-truth/chainsaw-test.yaml`
- Modify (only if the echo backend cannot shape traffic, see Step 1): `e2e/fixtures/backends/…`

- [ ] **Step 1: Establish traffic-shaping facts.** Read the ealen
echo-server docs/source for delay and response-header controls
(`ECHO_SERVER` supports per-request behavior via query/headers — verify
the exact parameter names, e.g. delay in ms and custom response headers).
If the deployed 0.9.2 image supports them, use query-driven shaping
against the existing `echo-service.yaml`; otherwise add a
`truth-backend` fixture (configmap + python:3.12-slim running the same
truthbackend.py pattern as Task 1) and document the choice in the test
description. No curl/wget anywhere.

- [ ] **Step 2: Write the test.** `suite: full` (runs on push/dispatch/
`e2e-full` PR label — note that in the description). Reuse the tracing
test's fixture set (otlp-sink, probe pod, tracing.yaml VinylCache with a
distinct CR name, e.g. `truth-cache`, to avoid cross-test collisions —
copy tracing.yaml to a `truth.yaml` fixture with the new name). Steps:
  1. deploy backend + sink + probe + cache (as in the tracing test, with
     the transport-retry loop on first traffic).
  2. **coalescing**: two backgrounded `kubectl exec … /vinylprobe -url
     "<slow cold URL>" -expect hit` (`&` + `wait` — each Detect's first
     request is the cold miss; with a ≥2s backend delay the two probes
     overlap on one fetch), then assert `-assert-spans -span-name
     "varnish fetch" -min-count 1 -within 90s` (sanity: the slow fetch
     reached the sink) and `-assert-spans -span-name "varnish request"
     -attr varnish.coalesced=true -min-count 1 -within 90s`. The
     `varnish.coalesced` attribute is only ever set when the linker
     resolved the origin fetch from its cache and established the time
     overlap, so its presence implies the link; exact link-id equality is
     already proven at the unit layer (Task 5) and is not re-derived
     here. State that reasoning in the step description.
  3. **grace/bgfetch**: request a short-TTL URL (shaped per Step 1),
     sleep is banned in scripts EXCEPT bounded retry loops — instead poll:
     re-request the URL via a small retry loop until `-assert-spans
     -span-name "varnish fetch" -attr varnish.bgfetch=true -min-count 1
     -within 120s` passes (the second+ request after TTL lapse triggers
     the bgfetch; drive requests inside the poll loop, 3s apart, max 20
     iterations — this is traffic generation, not a bare sleep; say so).
  4. **honest timing**: one cold request to a ~2s-delayed URL, then
     `-assert-spans -span-name "varnish fetch" -min-duration 1500ms
     -min-count 1 -within 60s` — the spec's honest-timing E2E, finally
     expressible.
  5. cleanup mirroring the tracing test.
  Budget every script step with the documented arithmetic (the P2
  lesson: worst case + explicit margin).

- [ ] **Step 3: Validate** (yaml parse, both hack checks — evidence), no
local cluster run; state CI-pending (and that `full` only runs with the
`e2e-full` label on the PR — recommend the controller add that label).

- [ ] **Step 4: Commit** (`test(e2e): truth-under-load suite — coalescing, grace, honest timing`).

---

### Task 9: E2E — ESI children (full suite; report BLOCKED if the backend route fails)

**Files:**
- Create: `e2e/fixtures/backends/esi-backend.yaml` (ConfigMap with an ESI
  page + fragment served by `python:3.12-slim` running an inline
  `http.server` variant that adds `Surrogate-Control: content="ESI/1.0"`,
  or nginx-alpine with `add_header` — implementer's choice, justified)
- Modify: `e2e/fixtures/vinylcaches/` (a `truth-esi.yaml` CR with
  `varnishParameters: {"feature +esi": "on"}` + tracing enabled — check
  how VarnishParams spell in the CRD yaml against an existing fixture)
- Modify: `e2e/tests/tracing-truth/chainsaw-test.yaml` (an esi step, or a
  second test file if CR-per-test isolation demands it — follow how other
  multi-CR tests in e2e/tests are organized)

- [ ] **Step 1:** Serve `before <esi:include src="/frag"/> after` +
Surrogate-Control; request it through a tracing-enabled, ESI-enabled
cache; assert: `-assert-spans -span-name "varnish request" -attr
varnish.esi=true -min-count 1 -within 90s` and the response body (via
`-body-capture 'FRAGMENT'`-style regex with a capture group, e.g.
`(FRAGMENT)`) proves the include actually assembled.
- [ ] **Step 2:** Validations (yaml, hack checks); CI-pending statement.
If ESI cannot be provoked end-to-end (feature flag, vmod route,
assembly), report BLOCKED with the varnishd evidence rather than shipping
a non-asserting test.
- [ ] **Step 3: Commit** (`test(e2e): esi child spans end to end`).

---

### Task 10: Docs sync + stale trailer

**Files:**
- Modify: `docs/superpowers/specs/2026-09-21-opentelemetry-tracing-design.md` (Truth semantics table, lines ~125-134)
- Modify: `docs/superpowers/plans/2026-09-22-otel-tracing-p1.md` (line 37)

- [ ] **Step 1:** Update the spec table rows to the implemented reality:
Coalescing row — every hit links to its origin fetch; `varnish.coalesced`
set on time-overlap; cache miss degrades to `varnish.coalesced_unlinked`
+ counter. Restarts/ESI/pipe rows — the attribute names as shipped
(`varnish.restarts`, `varnish.esi`, `varnish.pipe`, `varnish.synthetic`,
`varnish.retry`). Add the `varnish.minted_trace_mismatch` note to the
trace-model section. Keep prose tight.
- [ ] **Step 2:** P1 plan line 37: replace the stale trailer text with
`Assisted-by: Claude Fable 5` (same wording fix as the P2 doc got).
- [ ] **Step 3: Commit** (`docs: sync truth-semantics spec with P3 reality; fix stale trailer`).

---

### Task 11: Full verification pass

- [ ] **Step 1:** The battery, exit codes captured without pipes:
`make manifests generate && git diff --exit-code && git status --porcelain`
(no CRD changes expected), `make lint`, `make test`, `make test-int`,
`helm unittest charts/cloud-vinyl/`, `bash hack/check-suite-labels.sh`,
`bash hack/check-e2e-boundary.sh`.
- [ ] **Step 2:** Fix anything red minimally, commit, re-run from the top.
- [ ] **Step 3:** Report with real output tails; state that
`tracing-truth` (and the esi test) are pending a `full`-suite CI run —
the PR needs the `e2e-full` label.
