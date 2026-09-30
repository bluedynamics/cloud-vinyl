# OpenTelemetry Tracing P2 (VCL trace-context rewrite) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The backend becomes a proper child of the Varnish fetch span: a
generator-injected VCL block mints the fetch span id onto
`bereq.http.traceparent`, and the tracer adopts that id (and, for
self-rooted traffic, the trace id) from the VSL — proven end-to-end in E2E
by reading the minted header back out of the echo backend's body.

**Architecture:** A `HasTracing` template flag (the `HasXkey`/`HasESI`
pattern — the user snippet fields stay untouched) gates `import uuid;`, a
traceparent-validation block early in `vcl_recv`, and a minting block in
`vcl_backend_fetch`. `spans.Build` prefers ids parsed from `BereqHeader
traceparent` over self-minted ones, falling back to P1 behavior when the
header is absent. vinylprobe gains `-body-capture` (print a regex capture
from a response body, the `-seed` print-token precedent) and
`-trace-id`/`-span-id` filters on `-assert-spans`.

**Tech Stack:** Go 1.26, text/template VCL generation, vmod_uuid + vmod_std
(varnish:8.0.2), recorded varnishlog fixtures, chainsaw E2E.

**Spec:** `docs/superpowers/specs/2026-09-21-opentelemetry-tracing-design.md`
(build-order step 3 = P2; Task 7 here also corrects four stale statements in
the spec itself). P1 landed in PR #107.

## Global Constraints

- Module `github.com/bluedynamics/cloud-vinyl`, `go 1.26.7`. Work in worktree `sources/cloud-vinyl-wt/feat/otel-tracing-p2`, branch `feat/otel-tracing-p2`. Never touch `sources/cloud-vinyl/`.
- VSL fixtures are RECORDED from real `varnish:8.0.2` (docker, local `default` context), never hand-written or hand-edited. Existing P1 fixtures must keep passing untouched.
- `vmod_uuid` and `vmod_std` ship in the official `varnish:8.0.2` image (verified 2026-09-21 by listing `/usr/lib/varnish/vmods/`: `libvmod_uuid.so`, `libvmod_std.so`). There is no repo-side guard for vmod presence; a missing vmod fails at VCL load, which the fixture-recording task and E2E both exercise for real.
- W3C traceparent hex is lowercase; every VCL-minted hex value is wrapped in `std.tolower()` in case a vmod uuid implementation emits uppercase.
- cmd/vinylprobe must not import `k8s.io/*`/`sigs.k8s.io/*` (hack/check-e2e-boundary.sh); chainsaw tests must not contain the substrings `curl` or `wget` anywhere (`cut`/`echo`/`kubectl` are fine); exactly one `suite:` label per test.
- vinylprobe exit codes: 0 pass, 1 assertion failure, 2 usage/transport. `lll` 120-char lint applies to `cmd/*`; long flag help strings go in the existing `const` block.
- Unit tests: plain `testing` + testify, except `internal/webhook/v1alpha1` (ginkgo envtest suite) and generator tests (external `generator_test` package, inline `assert.Contains` on rendered VCL — no golden files).
- TDD with RED/GREEN evidence per task; `make lint` (golangci-lint v2.13.1) before each commit.
- No changelog file exists in this repo; do not invent one. Commits end with `Assisted-by: Claude Fable 5`.

---

### Task 1: Generator — `HasTracing` flag, `import uuid`, VCL blocks

**Files:**
- Modify: `internal/generator/generator.go` (TemplateData ~line 56-85; buildTemplateData feature flags ~line 209-228)
- Modify: `internal/generator/templates/main.vcl.tmpl` (import block, lines 8-16)
- Modify: `internal/generator/templates/vcl_recv.vcl.tmpl` (early section, before the PURGE block ~line 13)
- Modify: `internal/generator/templates/vcl_backend_fetch.vcl.tmpl` (whole file is 9 lines)
- Test: `internal/generator/generator_test.go`

**Interfaces:**
- Consumes: `Input.Spec.Tracing.Enabled` (v1alpha1.TracingSpec, exists since P1).
- Produces: rendered VCL containing `import uuid;`, a traceparent-validation
  block in `vcl_recv`, and a minting block in `vcl_backend_fetch`, all only
  when tracing is enabled. Task 2's recording script copies the two rendered
  blocks verbatim; Task 6's E2E loads them in a real varnishd.

- [ ] **Step 1: Write the failing tests** (external `generator_test` package, reuse `makeMinimalInput()`/`newGenerator(t)`):

```go
func TestGenerate_TracingDisabled_NoTracingVCL(t *testing.T) {
	g := newGenerator(t)
	r, err := g.Generate(makeMinimalInput())
	require.NoError(t, err)
	assert.NotContains(t, r.VCL, "import uuid")
	assert.NotContains(t, r.VCL, "traceparent")
}

func TestGenerate_TracingEnabled_ImportsUUID(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Spec.Tracing = vinylv1alpha1.TracingSpec{Enabled: true}
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "import uuid;",
		"tracing must import the uuid vmod for span-id minting")
}

func TestGenerate_TracingEnabled_RecvValidatesTraceparent(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Spec.Tracing = vinylv1alpha1.TracingSpec{Enabled: true}
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL,
		`req.http.traceparent !~ "^[0-9a-f]{2}-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$"`)
	assert.Contains(t, r.VCL, "unset req.http.traceparent")
	// The validation must run before cluster routing: relayed requests
	// return(pass) inside the routing block and would skip anything later.
	validation := strings.Index(r.VCL, "unset req.http.traceparent")
	purge := strings.Index(r.VCL, "PURGE")
	require.Greater(t, purge, 0)
	assert.Less(t, validation, purge,
		"traceparent validation must precede the PURGE/routing blocks in vcl_recv")
}

func TestGenerate_TracingEnabled_BackendFetchMintsSpanID(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Spec.Tracing = vinylv1alpha1.TracingSpec{Enabled: true}
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "set bereq.http.traceparent")
	assert.Contains(t, r.VCL, "uuid.uuid_v4()")
	assert.Contains(t, r.VCL, "std.tolower")
}

func TestGenerate_TracingWithUserSnippets_BothPresent(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Spec.Tracing = vinylv1alpha1.TracingSpec{Enabled: true}
	input.Spec.VCL.Snippets.VCLRecv = "# user recv snippet"
	input.Spec.VCL.Snippets.VCLBackendFetch = "# user backend_fetch snippet"
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "# user recv snippet")
	assert.Contains(t, r.VCL, "# user backend_fetch snippet")
	assert.Contains(t, r.VCL, "set bereq.http.traceparent",
		"operator tracing block must not displace user snippets")
}
```

(add `"strings"` and the `vinylv1alpha1` import if the file lacks them; it
already imports the API package for other tests — check first).

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/generator/ -run TestGenerate_Tracing -v`
Expected: FAIL (no tracing content rendered).

- [ ] **Step 3: Implement**

`generator.go` — add to `TemplateData` (with the other feature flags):

```go
	// HasTracing gates the tracing VCL: uuid import, traceparent
	// validation in vcl_recv, span-id minting in vcl_backend_fetch.
	HasTracing bool
```

and in `buildTemplateData`, next to the other feature flags:

```go
	data.HasTracing = input.Spec.Tracing.Enabled
```

`main.vcl.tmpl` — after the `HasESI` import gate:

```
{{- if .HasTracing }}
import uuid;
{{- end }}
```

`vcl_recv.vcl.tmpl` — insert directly after the Host-normalize/querysort
lines and BEFORE the PURGE block (placement is load-bearing: the cluster
routing block `return(pass)`es relayed requests, and validation must have
happened by then):

```
{{- if .HasTracing }}
    # Tracing: the incoming traceparent is untrusted client input. Drop
    # anything not matching the W3C trace-context shape, and all-zero ids,
    # so the tracer and the backend only ever see a well-formed header.
    if (req.http.traceparent &&
        req.http.traceparent !~ "^[0-9a-f]{2}-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$") {
        unset req.http.traceparent;
    }
    if (req.http.traceparent ~ "^[0-9a-f]{2}-0{32}-" ||
        req.http.traceparent ~ "-0{16}-[0-9a-f]{2}$") {
        unset req.http.traceparent;
    }
{{- end }}
```

`vcl_backend_fetch.vcl.tmpl` — full new file content (tracing block after
the user snippet, before `return(fetch)`):

```
sub vcl_backend_fetch {
{{ if .Spec.VCL.Snippets.VCLBackendFetch }}
    # --- custom vcl_backend_fetch snippet ---
    {{ .Spec.VCL.Snippets.VCLBackendFetch }}
    # --- end custom vcl_backend_fetch snippet ---
{{ end }}
{{- if .HasTracing }}
    # Tracing: the span id minted here becomes the "varnish fetch" span's
    # id — the tracer adopts it from the VSL BereqHeader record — so the
    # backend's own spans parent under the fetch span. Runs for bgfetch
    # too (each backend fetch gets its own id). std.tolower guards against
    # a vmod uuid implementation emitting uppercase hex (W3C: lowercase).
    if (bereq.http.traceparent) {
        set bereq.http.traceparent = regsub(bereq.http.traceparent,
            "^([0-9a-f]{2}-[0-9a-f]{32}-)[0-9a-f]{16}(-[0-9a-f]{2})$",
            "\1" + std.tolower(regsub(uuid.uuid_v4(),
                "^(........)-(....)-(....).*$", "\1\2\3")) + "\2");
    } else {
        set bereq.http.traceparent = "00-" +
            std.tolower(regsuball(uuid.uuid_v4(), "-", "")) + "-" +
            std.tolower(regsub(uuid.uuid_v4(),
                "^(........)-(....)-(....).*$", "\1\2\3")) + "-01";
    }
{{- end }}
    return(fetch);
}
```

(The first 16 hex chars of a dashless UUIDv4 are the 8-4-4 groups, hence
`\1\2\3`. The rewrite keeps version, trace id, and flags; the self-root
mints version 00 with the sampled flag set, matching the spec's trace
model. Leading blank line of the original file is dropped deliberately —
keep it if `TestGenerate_Determinism` or byte-level tests complain.)

- [ ] **Step 4: Run the full generator suite**

Run: `go test ./internal/generator/ -v`
Expected: PASS, including the pre-existing `TestGenerate_Determinism` and
`TestGenerate_AllSubroutinesHaveReturnStatement`.

- [ ] **Step 5: Lint and commit**

```bash
make lint
git add internal/generator
git commit -m "feat(generator): tracing VCL — validate traceparent, mint fetch span id"
```

---

### Task 2: Record P2 fixtures (rewrite + self-root)

**Files:**
- Create: `internal/tracer/vsl/testdata/record-fixtures-tracing.sh`
- Create: `internal/tracer/vsl/testdata/{rewrite_miss,rewrite_selfroot}.txt` (recorded)
- Modify: `internal/tracer/vsl/testdata/README.md`

The script's `default.vcl` embeds the SAME validation/minting logic Task 1
put in the templates, rendered by hand into plain VCL (the generator needs
a k8s backend list; the recording rig needs one static backend). A loud
comment marks the duplication; template correctness itself is proven by
Task 1's tests and Task 6's E2E, while these fixtures exist to give the
parser/builder real `BereqHeader traceparent` records.

- [ ] **Step 1: Write the script**

```bash
#!/usr/bin/env bash
# Records P2 VSL fixtures: varnish 8.0.2 WITH the tracing VCL active, so
# BereqHeader carries a VCL-minted traceparent. The vcl below mirrors the
# tracing blocks of internal/generator/templates/{vcl_recv,vcl_backend_fetch}
# .vcl.tmpl — keep them in sync when the templates change.
set -euo pipefail
cd "$(dirname "$0")"

NET=vsl-fixtures-p2
TP='00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01'

cleanup() {
  docker rm -f vslp2-varnish vslp2-backend >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
  rm -f default.vcl
}
trap cleanup EXIT
cleanup

cat > default.vcl <<'EOF'
vcl 4.1;
import std;
import uuid;
backend default { .host = "vslp2-backend"; .port = "8000"; }
sub vcl_recv {
    if (req.http.traceparent &&
        req.http.traceparent !~ "^[0-9a-f]{2}-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$") {
        unset req.http.traceparent;
    }
    if (req.http.traceparent ~ "^[0-9a-f]{2}-0{32}-" ||
        req.http.traceparent ~ "-0{16}-[0-9a-f]{2}$") {
        unset req.http.traceparent;
    }
}
sub vcl_backend_fetch {
    if (bereq.http.traceparent) {
        set bereq.http.traceparent = regsub(bereq.http.traceparent,
            "^([0-9a-f]{2}-[0-9a-f]{32}-)[0-9a-f]{16}(-[0-9a-f]{2})$",
            "\1" + std.tolower(regsub(uuid.uuid_v4(),
                "^(........)-(....)-(....).*$", "\1\2\3")) + "\2");
    } else {
        set bereq.http.traceparent = "00-" +
            std.tolower(regsuball(uuid.uuid_v4(), "-", "")) + "-" +
            std.tolower(regsub(uuid.uuid_v4(),
                "^(........)-(....)-(....).*$", "\1\2\3")) + "-01";
    }
    return(fetch);
}
EOF

docker network create "$NET" >/dev/null
docker run -d --name vslp2-backend --network "$NET" \
    python:3.12-slim python -m http.server 8000 >/dev/null
docker run -d --name vslp2-varnish --network "$NET" \
    -v "$PWD/default.vcl:/etc/varnish/default.vcl:ro" \
    varnish:8.0.2 >/dev/null
sleep 2

req() { # req <path> [traceparent]
  if [ $# -gt 1 ]; then
    docker run --rm --network "$NET" python:3.12-slim python -c "
import urllib.request, urllib.error
r = urllib.request.Request('http://vslp2-varnish$1', headers={'traceparent': '$2'})
try:
    urllib.request.urlopen(r).read()
except urllib.error.HTTPError:
    pass"
  else
    docker run --rm --network "$NET" python:3.12-slim python -c "
import urllib.request, urllib.error
try:
    urllib.request.urlopen('http://vslp2-varnish$1').read()
except urllib.error.HTTPError:
    pass"
  fi
}

record() { docker exec vslp2-varnish varnishlog -g request -d > "$1"; }

req /rewrite "$TP";                 record rewrite_miss.txt
docker restart vslp2-varnish >/dev/null; sleep 2
req /selfroot;                      record rewrite_selfroot.txt

echo "Recorded: rewrite_miss.txt rewrite_selfroot.txt"
```

- [ ] **Step 2: Run it and verify the minted header by eye**

Run: `bash internal/tracer/vsl/testdata/record-fixtures-tracing.sh`
Expected: `rewrite_miss.txt` shows `ReqHeader Traceparent:` with the fixed
`$TP` value AND a `BereqHeader Traceparent: 00-4bf92f...-<16 new hex>-01`
whose trace id equals `$TP`'s but whose span id differs;
`rewrite_selfroot.txt` shows no client traceparent and a full VCL-minted
`BereqHeader Traceparent: 00-<32 hex>-<16 hex>-01`, all lowercase. If the
VCL fails to load (missing vmod, syntax), varnishd exits — STOP and report
with the container logs; do not edit fixtures.

- [ ] **Step 3: Extend the README**

Append:

```markdown

`record-fixtures-tracing.sh` records the P2 variants with the tracing VCL
active (VCL-minted `BereqHeader traceparent`); its embedded VCL mirrors the
generator templates' tracing blocks — keep them in sync.
```

- [ ] **Step 4: Commit**

```bash
git add internal/tracer/vsl/testdata
git commit -m "test(tracer): record P2 fixtures with VCL-minted traceparent"
```

---

### Task 3: Tracer adopts the VCL-minted ids

**Files:**
- Modify: `internal/tracer/spans/spans.go` (`Build`, lines ~79-139)
- Test: `internal/tracer/spans/spans_test.go`

**Interfaces:**
- Consumes: `vsl.Tx.Header("BereqHeader", "traceparent")`, existing
  `parseTraceparent`.
- Produces: unchanged signature `Build(tx *vsl.Tx, ids IDSource) []Span`;
  new semantics: fetch span id = VCL-minted id when present; trace id
  adopted from the minted header when the client sent none. P1 fixtures
  (no BereqHeader minting) must keep passing byte-identically.

- [ ] **Step 1: Write the failing tests.** The minted ids are random per
recording, so expectations are read FROM the fixture, not hardcoded:

```go
// mintedTraceparent extracts the VCL-minted BereqHeader traceparent of the
// first BeReq child, parsed with the same production parser.
func mintedTraceparent(t *testing.T, tx *vsl.Tx) (trace.TraceID, trace.SpanID) {
	t.Helper()
	for _, child := range tx.Children {
		if child.Type != "BeReq" {
			continue
		}
		raw, ok := child.Header("BereqHeader", "traceparent")
		require.True(t, ok, "fixture must carry a minted BereqHeader traceparent")
		tid, sid, _, valid := parseTraceparent(raw)
		require.True(t, valid, "minted traceparent must be W3C-valid: %q", raw)
		return tid, sid
	}
	t.Fatal("fixture has no BeReq child")
	return trace.TraceID{}, trace.SpanID{}
}

func TestBuild_AdoptsVCLMintedFetchSpanID(t *testing.T) {
	txs := fixtureTxs(t, "rewrite_miss.txt")
	require.NotEmpty(t, txs)
	wantTID, wantSID := mintedTraceparent(t, txs[0])

	got := Build(txs[0], &seqIDs{})
	require.Len(t, got, 2)
	req, fetch := got[0], got[1]

	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", req.TraceID.String(),
		"client trace id inherited")
	assert.Equal(t, wantTID, fetch.TraceID, "VCL preserved the trace id")
	assert.Equal(t, wantSID, fetch.SpanID,
		"fetch span must carry the VCL-minted id the backend parented to")
	assert.Equal(t, req.SpanID, fetch.ParentID)
	assert.NotEqual(t, "00f067aa0ba902b7", fetch.SpanID.String(),
		"minted id must differ from the client's span id")
}

func TestBuild_SelfRootAdoptsVCLMintedTraceID(t *testing.T) {
	txs := fixtureTxs(t, "rewrite_selfroot.txt")
	require.NotEmpty(t, txs)
	wantTID, wantSID := mintedTraceparent(t, txs[0])

	got := Build(txs[0], &seqIDs{})
	require.Len(t, got, 2)
	req, fetch := got[0], got[1]

	assert.Equal(t, wantTID, req.TraceID,
		"with no client traceparent the request span must join the VCL-minted trace, or the backend's spans land in a different trace")
	assert.Equal(t, trace.SpanID{}, req.ParentID, "still a root span")
	assert.Equal(t, wantSID, fetch.SpanID)
}

func TestBuild_UnrewrittenBereqHeaderIsNotAdopted(t *testing.T) {
	txs := fixtureTxs(t, "miss_then_hit.txt")
	got := Build(txs[0], &seqIDs{})
	require.Len(t, got, 2)
	req, fetch := got[0], got[1]
	// P1 fixture: bereq forwarded the client's traceparent untouched, so
	// its span id equals the request's ParentID. Adopting it would give
	// the fetch span the ingress's own id.
	assert.NotEqual(t, req.ParentID, fetch.SpanID,
		"an unrewritten forwarded span id must not be adopted")
	assert.Equal(t, req.SpanID, fetch.ParentID)
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/tracer/spans/ -run 'TestBuild_Adopts|TestBuild_SelfRoot|TestBuild_Unrewritten' -v`
Expected: the two adoption tests FAIL (ids are self-minted today); the
unrewritten test passes already (it pins existing behavior against
regression by the fix).

- [ ] **Step 3: Implement.** In `Build`, after the incoming-traceparent
handling and before minting `traceID` (currently `spans.go:91-101`),
gather the minted ids:

```go
	// P2: the VCL snippet mints the fetch span id onto bereq's traceparent
	// (and the whole trace id when the client sent none). Adopt those ids
	// so the backend's spans, parented on the minted value, land in the
	// same trace under the fetch span. A BereqHeader whose span id merely
	// equals the incoming parent id is an unrewritten P1-style forward —
	// not a minted id — and is ignored.
	mintedSpanIDs := make(map[*vsl.Tx]trace.SpanID)
	var mintedTraceID trace.TraceID
	for _, child := range tx.Children {
		if child.Type != "BeReq" {
			continue
		}
		raw, ok := child.Header("BereqHeader", "traceparent")
		if !ok {
			continue
		}
		tid, sid, _, valid := parseTraceparent(raw)
		if !valid || sid == parentID {
			continue
		}
		mintedSpanIDs[child] = sid
		if !mintedTraceID.IsValid() {
			mintedTraceID = tid
		}
	}
```

Then change the self-root minting (currently `traceID = ids.TraceID()`):

```go
	if !traceID.IsValid() {
		if mintedTraceID.IsValid() {
			traceID = mintedTraceID // join the VCL-minted trace
		} else {
			traceID = ids.TraceID() // self-rooted: Varnish is the edge
		}
	}
```

And in the fetch-span loop, replace `SpanID: ids.SpanID(),`:

```go
		spanID, minted := mintedSpanIDs[child]
		if !minted {
			spanID = ids.SpanID()
		}
```

(using `SpanID: spanID,` in the struct literal). Note `parentID` is the
variable already holding the incoming parent span id; it is the zero
`trace.SpanID{}` when the client sent nothing, and a minted sid is never
zero (parseTraceparent rejects all-zero), so the `sid == parentID` guard
is correct in both modes.

- [ ] **Step 4: Run the full tracer suite**

Run: `go test ./internal/tracer/... -v`
Expected: PASS — new adoption tests AND every P1 test (hit, miss,
no-traceparent, invalid, pass, unsampled) unchanged.

- [ ] **Step 5: Lint and commit**

```bash
make lint
git add internal/tracer/spans
git commit -m "feat(tracer): adopt VCL-minted trace context from BereqHeader"
```

---

### Task 4: vinylprobe — `-body-capture`, span-id filters, error-carrying FAIL

**Files:**
- Modify: `internal/probe/cache.go` (new exported func next to `Detect`)
- Test: `internal/probe/cache_test.go`
- Modify: `cmd/vinylprobe/main.go` (flags :81-144, validateSpanModes :153, dispatch :225-250, decideSpans :386, runAssertSpans :424)
- Test: `cmd/vinylprobe/main_test.go`

**Interfaces:**
- Produces (Task 6 consumes):
  - `vinylprobe -url U [-host H] -body-capture '<regex with 1 capture group>'` — GET U once, print the first capture group of the first match against the body to stdout (the `-seed` print-token precedent), exit 0; no match → FAIL + exit 1; bad regex/transport → exit 2.
  - `vinylprobe -assert-spans ... [-trace-id <32hex>] [-span-id <16hex>]` — additional equality filters against `SpanSummary.TraceID`/`SpanID`.
  - `-assert-spans` FAIL line now appends `; last sink error: <err>` when the final poll iteration had a transport error.
- New probe func: `func BodyCapture(ctx context.Context, c *http.Client, url, host, pattern string) (string, bool, error)` — (capture, matched, err); err only for transport/regex problems.

- [ ] **Step 1: Failing test for `BodyCapture`** (internal/probe, testify;
httptest server as counterpart — it reflects a header into a JSON body the
way the real echo server does, which is the documented `"x-probe":"..."`
shape from the E2E design spec):

```go
func TestBodyCapture_ExtractsGroupFromEchoedBody(t *testing.T) {
	// The handler reflects a request header into a JSON body the way the
	// real echo backend does (lowercased names, documented in the E2E
	// design spec as `"x-probe":"<value>"`).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"headers":{"traceparent":%q}}`, r.Header.Get("traceparent"))
	}))
	defer srv.Close()

	c := &http.Client{Transport: roundTripperWithHeader{
		key: "traceparent", val: "00-abc-def-01"}}
	got, matched, err := BodyCapture(context.Background(), c, srv.URL, "",
		`"traceparent":"(00-[0-9a-f-]+-01)"`)
	require.NoError(t, err)
	require.True(t, matched)
	assert.Equal(t, "00-abc-def-01", got)
}

func TestBodyCapture_NoMatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"headers":{}}`)
	}))
	defer srv.Close()
	_, matched, err := BodyCapture(context.Background(), srv.Client(), srv.URL, "",
		`"traceparent":"(00-[0-9a-f-]+)"`)
	require.NoError(t, err)
	assert.False(t, matched)
}

func TestBodyCapture_RegexNeedsCaptureGroup(t *testing.T) {
	_, _, err := BodyCapture(context.Background(), http.DefaultClient,
		"http://unused.invalid", "", `no-group-here`)
	require.Error(t, err)
}
```

with the tiny test helper:

```go
type roundTripperWithHeader struct {
	base http.RoundTripper
	key, val string
}

func (rt roundTripperWithHeader) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set(rt.key, rt.val)
	base := rt.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(r)
}
```

- [ ] **Step 2: Verify failure, implement `BodyCapture` in `internal/probe/cache.go`:**

```go
// BodyCapture GETs url once and applies pattern (which must contain at
// least one capture group) to the response body, returning the first
// group of the first match. It exists for E2E assertions against backends
// that reflect request state into their response body (the echo server's
// lowercased-header JSON), e.g. reading back a VCL-minted traceparent.
// matched=false with a nil error means the body simply didn't match.
func BodyCapture(ctx context.Context, c *http.Client, url, host, pattern string) (string, bool, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", false, fmt.Errorf("compiling -body-capture pattern: %w", err)
	}
	if re.NumSubexp() < 1 {
		return "", false, fmt.Errorf("-body-capture pattern %q has no capture group", pattern)
	}
	tok, err := token()
	if err != nil {
		return "", false, err
	}
	body, err := fetch(ctx, c, url, tok, host)
	if err != nil {
		return "", false, err
	}
	m := re.FindStringSubmatch(body)
	if m == nil {
		return "", false, nil
	}
	return m[1], true, nil
}
```

(add `"regexp"` to the imports). Run: `go test ./internal/probe/ -v` → PASS.

- [ ] **Step 3: Failing tests for the cmd layer** (no-testify style):

```go
func TestDecideSpans_TraceIDFilter(t *testing.T) {
	spans := []probe.SpanSummary{
		{Name: "varnish fetch", TraceID: "aa11", SpanID: "bb22", Attrs: map[string]string{}},
		{Name: "varnish fetch", TraceID: "cc33", SpanID: "dd44", Attrs: map[string]string{}},
	}
	v := decideSpans(spans, "varnish fetch", nil, "aa11", "", 1)
	if !v.satisfied || v.matched != 1 {
		t.Fatalf("trace-id filter: want 1 match, got %+v", v)
	}
}

func TestDecideSpans_SpanIDFilter(t *testing.T) {
	spans := []probe.SpanSummary{
		{Name: "varnish fetch", TraceID: "aa11", SpanID: "bb22", Attrs: map[string]string{}},
	}
	if v := decideSpans(spans, "varnish fetch", nil, "", "zz99", 1); v.satisfied {
		t.Fatal("span-id mismatch must not satisfy")
	}
	if v := decideSpans(spans, "varnish fetch", nil, "aa11", "bb22", 1); !v.satisfied {
		t.Fatal("both filters matching must satisfy")
	}
}
```

(existing decideSpans tests gain the two extra `"" , ""` arguments.)

- [ ] **Step 4: Implement the cmd changes.**

- `probeFlags`: add `bodyCapture string`, `traceID string`, `spanID string`.
- `parseFlags`: `bodyCapture := flag.String("body-capture", "", bodyCaptureHelp)`, `traceID := flag.String("trace-id", "", traceIDHelp)`, `spanID := flag.String("span-id", "", spanIDHelp)`; help consts into the `:24-51` block:

```go
	bodyCaptureHelp = "regex with one capture group applied to the -url response body; " +
		"prints the first group (exit 0), FAIL exit 1 on no match"
	traceIDHelp = "assert-spans: only count spans with this exact hex trace id"
	spanIDHelp  = "assert-spans: only count spans with this exact hex span id"
```

- `validateSpanModes`: `-trace-id`/`-span-id` without `-assert-spans` is an error; `-body-capture` conflicts with `-assert-spans`/`-otlp-sink` (it belongs to the `-url` family):

```go
	if (f.traceID != "" || f.spanID != "") && !f.assertSpans {
		return true, errors.New("-trace-id/-span-id require -assert-spans")
	}
```

and in `validate()`'s `-url` family checks: `-body-capture` requires `-url` and excludes `-purge`, `-seed`, `-check` (mirror the existing mutual-exclusion phrasing; `-expect`'s default is fine — dispatch order decides).

- `decideSpans` — new signature `decideSpans(spans []probe.SpanSummary, name string, attrs map[string]string, traceID, spanID string, minCount int) spanVerdict`; inside the loop, after the name check:

```go
		if traceID != "" && s.TraceID != traceID {
			continue
		}
		if spanID != "" && s.SpanID != spanID {
			continue
		}
```

- `runAssertSpans`: pass `f.traceID, f.spanID`; carry the last error:

```go
	var lastErr error
	for time.Now().Before(deadline) {
		spans, err := fetchSpans(ctx, f.sink)
		lastErr = err
		...
	}
	msg := fmt.Sprintf("FAIL: only %d span(s) named %q matched within %s",
		last.matched, f.spanName, f.within)
	if lastErr != nil {
		msg += fmt.Sprintf("; last sink error: %v", lastErr)
	}
	fmt.Println(msg)
	os.Exit(1)
```

- New `runBodyCapture` + dispatch case BEFORE the default detect case:

```go
	case f.bodyCapture != "":
		runBodyCapture(ctx, client, f)
```

```go
func runBodyCapture(ctx context.Context, client *http.Client, f probeFlags) {
	got, matched, err := probe.BodyCapture(ctx, client, f.url, f.host, f.bodyCapture)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	if !matched {
		fmt.Printf("FAIL: %s body did not match %q\n", f.url, f.bodyCapture)
		os.Exit(1)
	}
	// Bare capture on stdout: chainsaw script steps shell-capture it,
	// the same contract as -seed's token.
	fmt.Println(got)
}
```

- [ ] **Step 5: Run everything + boundary**

Run: `go test ./cmd/vinylprobe/ ./internal/probe/ -v && bash hack/check-e2e-boundary.sh`
Expected: PASS + `OK: E2E layer boundary intact`.

- [ ] **Step 6: Lint and commit**

```bash
make lint
git add internal/probe cmd/vinylprobe
git commit -m "feat(probe): body-capture mode and span id filters"
```

---

### Task 5: envtest admission tests for spec.tracing

**Files:**
- Modify: `internal/webhook/v1alpha1/vinylcache_webhook_test.go` (currently zero `It` blocks — only commented scaffold)

The suite already boots envtest with the real CRDs and webhook server
(`webhook_suite_test.go`), and `k8sClient` is created but unused. These are
the first real admission assertions: they prove the API-server round trip
(webhook wired, failurePolicy effective), not the pure function (that has
unit tests in `internal/webhook/vinylcache_validator_test.go:561-590`).

- [ ] **Step 1: Write the failing tests.** Inside the existing
`Context("When creating or updating VinylCache under Validating Webhook", ...)`,
replacing the commented examples:

```go
		It("denies tracing enabled without an OTLP endpoint", func() {
			vc := minimalAdmissibleVC("traced-invalid")
			vc.Spec.Tracing = vinylv1alpha1.TracingSpec{Enabled: true}
			err := k8sClient.Create(ctx, vc)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("tracing.otlp.endpoint is required"))
		})

		It("admits tracing with a host:port endpoint", func() {
			vc := minimalAdmissibleVC("traced-valid")
			vc.Spec.Tracing = vinylv1alpha1.TracingSpec{
				Enabled: true,
				OTLP:    vinylv1alpha1.OTLPSpec{Endpoint: "collector:4317"},
			}
			Expect(k8sClient.Create(ctx, vc)).To(Succeed())
			DeferCleanup(func() {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, vc))).To(Succeed())
			})
		})
```

plus a package-level helper in the same file:

```go
// minimalAdmissibleVC returns a VinylCache that passes CRD schema and
// webhook validation with tracing left off. Mirror the shape of
// internal/webhook's minimalValidVC unit fixture (test helpers cannot be
// imported across packages) — copy its required spec fields exactly; if
// the two drift, the admit test fails loudly, which is the point.
func minimalAdmissibleVC(name string) *vinylv1alpha1.VinylCache {
	...
}
```

Fill `...` by reading `internal/webhook/vinylcache_validator_test.go`'s
`minimalValidVC` and reproducing its `Spec` verbatim, with
`ObjectMeta{Name: name, Namespace: "default"}`. Do not invent fields.

- [ ] **Step 2: Run to verify failure**

Run: `KUBEBUILDER_ASSETS="$(bin/setup-envtest use -p path 2>/dev/null || true)" go test ./internal/webhook/v1alpha1/ -v` — simpler: `make test-int` (targets set the assets). First run: compile error until the helper exists, then genuine admission results.
Expected once compiling: the deny test FAILS only if the webhook is not
actually rejecting (it should pass immediately if wiring is correct — that
is fine: these tests pin the round trip; RED here is the compile stage).

- [ ] **Step 3: Make it green, run the suite twice** (ordering flakes in
envtest show on the second run): `make test-int` → all PASS, both runs.

- [ ] **Step 4: Lint and commit**

```bash
make lint
git add internal/webhook/v1alpha1
git commit -m "test(webhook): first real admission assertions — spec.tracing"
```

---

### Task 6: E2E rewrite proof

**Files:**
- Modify: `e2e/tests/tracing/chainsaw-test.yaml` (insert a step between `assert-spans` (L92) and `cleanup` (L116); update the spec.description's NOT COVERED line)

- [ ] **Step 1: Add the step.**

```yaml
    - name: rewrite-proof
      description: |
        P2: the VCL snippet mints the fetch span id onto bereq's
        traceparent, and the echo backend reflects request headers into
        its JSON body (lowercased names), so the minted value is readable
        from the response of the very fetch it labeled. The sink must then
        hold a "varnish fetch" span with exactly that trace and span id —
        the backend's parent IS the fetch span. A fresh path guarantees
        the reflecting response comes from a cold-cache fetch, and sending
        no client traceparent also proves self-root trace adoption: the
        "varnish request" span must be in the SAME trace the VCL minted.
      try:
        - script:
            timeout: 120s
            content: |
              set -eu
              TP=$(kubectl exec -n $NAMESPACE probe -- \
                /vinylprobe -url "http://traced-cache-traffic:8080/traced/rewrite" \
                  -body-capture '"traceparent":"([0-9a-f]{2}-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2})"')
              echo "backend saw traceparent ${TP}"
              TID=$(echo "${TP}" | cut -d- -f2)
              SID=$(echo "${TP}" | cut -d- -f3)
              kubectl exec -n $NAMESPACE probe -- \
                /vinylprobe -assert-spans -sink "http://otlp-sink:4318" \
                  -span-name "varnish fetch" -trace-id "${TID}" -span-id "${SID}" \
                  -min-count 1 -within 60s
              kubectl exec -n $NAMESPACE probe -- \
                /vinylprobe -assert-spans -sink "http://otlp-sink:4318" \
                  -span-name "varnish request" -trace-id "${TID}" \
                  -min-count 1 -within 30s
```

- [ ] **Step 2: Update the header description.** Change the
`NOT COVERED (later plans)` lines to:

```
    NOT COVERED (later plans): coalescing/grace/ESI truth (P3).
```

- [ ] **Step 3: Validate**

Run:
```bash
python3 -c "import yaml; yaml.safe_load(open('e2e/tests/tracing/chainsaw-test.yaml')); print('yaml ok')"
bash hack/check-suite-labels.sh
bash hack/check-e2e-boundary.sh
```
Expected: all OK (`cut` is fine; only `curl`/`wget` substrings are banned —
double-check the step prose too).

- [ ] **Step 4: Commit**

```bash
git add e2e/tests/tracing/chainsaw-test.yaml
git commit -m "test(e2e): prove the VCL trace-context rewrite end to end"
```

No local cluster run expected; the PR's CI fast suite is the execution.

---

### Task 7: Spec corrections (docs only)

**Files:**
- Modify: `docs/superpowers/specs/2026-09-21-opentelemetry-tracing-design.md`

Four stale statements, found while implementing P1/P2 — the spec is the
binding doc and must match reality:

- [ ] **Step 1: Apply the four edits.**

1. Line 174: `ghcr.io/bluedynamics/vinyl-tracer:<version>` → `ghcr.io/bluedynamics/cloud-vinyl-tracer:<version>` (shipped name, consistent with operator/agent).
2. Line 104: `Injected via the existing snippet mechanism when tracing is enabled:` → `Injected by the generator itself when tracing is enabled — a HasTracing template flag, the same pattern as the xkey/esi imports; the user snippet fields stay untouched:`
3. Lines 112-114 (the X-Cache bullet) → `- Ingress-side capture of cache-status response headers (e.g. Traefik's capturedResponseHeaders) is a deployment concern; this design changes no response headers.`
4. In the "CRD API and operator wiring" section, the sentence beginning `Defaulter fills protocol and serviceName` → `Protocol and serviceName defaults are applied controller-side in the sidecar builder (the exporter convention); the webhook only validates.`

- [ ] **Step 2: Re-read the spec's P2-relevant sections** (VCL snippet,
Trace model, Testing) once against this plan; if another statement
contradicts shipped reality, fix it in the same commit and say so in the
task report.

- [ ] **Step 3: Commit**

```bash
git add docs/superpowers/specs/2026-09-21-opentelemetry-tracing-design.md
git commit -m "docs(spec): align tracing spec with shipped P1 reality"
```

---

### Task 8: Full verification pass

- [ ] **Step 1: The battery**

```bash
make manifests generate && git diff --exit-code && git status --porcelain
make lint
make test
make test-int
helm unittest charts/cloud-vinyl/
bash hack/check-suite-labels.sh
bash hack/check-e2e-boundary.sh
```
Expected: every command exits 0 (no CRD changes in P2, so the first line
must show zero drift). Capture real exit codes — do not read them through
a pipe to `tail` (that reports tail's status; use `{ cmd; echo EXIT=$?; }`).

- [ ] **Step 2: Fix anything red minimally, commit, re-run from the top.**

- [ ] **Step 3: Report** with actual command tails; state explicitly that
the extended tracing chainsaw test is pending its PR CI run.
