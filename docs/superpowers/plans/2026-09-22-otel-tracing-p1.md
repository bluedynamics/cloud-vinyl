# OpenTelemetry Tracing P1 (vinyl-tracer sidecar) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A deployable `vinyl-tracer` sidecar that reads the VSL via a
version-matched `varnishlog` subprocess, builds honest request/fetch spans
with real VSL timestamps, exports them via OTLP, and is wired through CRD,
operator, chart, and a fast-suite E2E test.

**Architecture:** Pure parsing/building core (`internal/tracer/vsl`,
`internal/tracer/spans`) tested against golden fixtures recorded from real
varnishd; a thin exporter layer feeding `otlptrace` directly with a bounded
drop-on-overflow batcher; `cmd/tracer` supervises the `varnishlog -g request`
subprocess. Operator wiring follows the exporter sidecar patterns exactly.

**Tech Stack:** Go 1.26, otel-go SDK (`otel` + `sdk` + `otlptrace` aligned to
one version), prometheus/client_golang (already a direct dep), distroless
base-debian13 runtime image, chainsaw E2E, helm-unittest chart tests.

**Spec:** `docs/superpowers/specs/2026-09-21-opentelemetry-tracing-design.md`
(this plan implements build-order steps 1–2 = P1; VCL rewrite, truth
semantics, docs/publication are later plans).

## Global Constraints

- Module `github.com/bluedynamics/cloud-vinyl`, `go 1.26.7`.
- Work happens in worktree `sources/cloud-vinyl-wt/feat/otel-tracing`, branch `feat/otel-tracing`. Never touch `sources/cloud-vinyl/`.
- otel deps are currently all `// indirect` and version-skewed (api 1.41.0 vs sdk/otlptrace 1.40.0). When promoting to direct, `go get` api+sdk+exporters at ONE version (Task 4 pins the exact commands).
- Every container gets the repo's security context: `RunAsNonRoot: new(true), ReadOnlyRootFilesystem: new(true), AllowPrivilegeEscalation: new(false)` (Go 1.26 `new(expr)`).
- Sidecar image/port defaults live in the controller builder function, NOT in the webhook defaulter (exporter convention). Plain `bool` CRD fields stay plain (zero value = disabled); never default booleans in the webhook.
- `cmd/vinylprobe` must not import `k8s.io/*` or `sigs.k8s.io/*`, directly or transitively; chainsaw tests must not contain the substrings `curl` or `wget` anywhere, including comments (`hack/check-e2e-boundary.sh` greps literally).
- Every chainsaw test has exactly one `metadata.labels.suite: fast|full` label (`hack/check-suite-labels.sh`).
- vinylprobe exit codes: 0 = pass, 1 = assertion failure, 2 = usage/transport error.
- Test fixtures for the VSL parser are RECORDED from real varnishd (`varnish:8.0.2`), never hand-written. Docker: local `default` context only.
- Unit tests: plain `testing` + testify (`assert`/`require`) + controller-runtime fake client. Ginkgo only in existing envtest suites.
- No changelog file exists in this repo; do not invent one.
- Commit messages end with `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`.
- Run `make lint` before each commit that touches Go code (golangci-lint v2.13.1; `lll` 120-char limit applies to `cmd/*`).

---

### Task 1: Record golden VSL fixtures

**Files:**
- Create: `internal/tracer/vsl/testdata/README.md`
- Create: `internal/tracer/vsl/testdata/record-fixtures.sh`
- Create: `internal/tracer/vsl/testdata/{miss_then_hit,no_traceparent,invalid_traceparent,pass}.txt` (recorded output)

This repo has no golden-fixture convention yet; this task establishes it:
fixtures are raw `varnishlog -g request` text recorded from a real
`varnish:8.0.2`, and the script to re-record them is committed next to them.

- [ ] **Step 1: Write the recording script**

```bash
#!/usr/bin/env bash
# Records golden VSL fixtures for the vinyl-tracer parser tests from a real
# varnish:8.0.2. Re-run when bumping the varnish baseline; commit the diff.
set -euo pipefail
cd "$(dirname "$0")"

NET=vsl-fixtures
TP='00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01'

cleanup() {
  docker rm -f vsl-varnish vsl-backend >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
  rm -f default.vcl
}
trap cleanup EXIT
cleanup

cat > default.vcl <<'EOF'
vcl 4.1;
backend default { .host = "vsl-backend"; .port = "8000"; }
sub vcl_recv {
    if (req.url ~ "^/pass") { return (pass); }
}
EOF

docker network create "$NET" >/dev/null
docker run -d --name vsl-backend --network "$NET" \
    python:3.12-slim python -m http.server 8000 >/dev/null
docker run -d --name vsl-varnish --network "$NET" \
    -v "$PWD/default.vcl:/etc/varnish/default.vcl:ro" \
    varnish:8.0.2 >/dev/null
sleep 2

req() { # req <path> [extra header]
  if [ $# -gt 1 ]; then
    docker run --rm --network "$NET" python:3.12-slim python -c "
import urllib.request
r = urllib.request.Request('http://vsl-varnish$1', headers={'traceparent': '$2'})
urllib.request.urlopen(r).read()"
  else
    docker run --rm --network "$NET" python:3.12-slim python -c "
import urllib.request
urllib.request.urlopen('http://vsl-varnish$1').read()"
  fi
}

record() { # record <file>: drain processed log records, then truncate live log
  docker exec vsl-varnish varnishlog -g request -d > "$1"
}

req /miss-hit "$TP";  req /miss-hit "$TP";  record miss_then_hit.txt
docker restart vsl-varnish >/dev/null; sleep 2
req /plain;                                 record no_traceparent.txt
docker restart vsl-varnish >/dev/null; sleep 2
req /garbage "not-a-traceparent";           record invalid_traceparent.txt
docker restart vsl-varnish >/dev/null; sleep 2
req /pass "$TP";                            record pass.txt

echo "Recorded: miss_then_hit.txt no_traceparent.txt invalid_traceparent.txt pass.txt"
```

(The `docker restart` between scenarios keeps each fixture file down to its
own transactions; `varnishlog -d` dumps already-processed records and exits.)

- [ ] **Step 2: Run it and eyeball the output**

Run: `bash internal/tracer/vsl/testdata/record-fixtures.sh`
Expected: four `.txt` files; `miss_then_hit.txt` contains two `* << Request >>`
groups, the first with a nested `** << BeReq >>` group, `ReqHeader`
lines including `traceparent: 00-4bf92...`, `Timestamp Start:`/`Resp:` records,
and the second group a `Hit` record. If varnishd logs differ structurally from
this description, STOP and reconcile the parser design in Task 2 with reality
before writing any parser code.

- [ ] **Step 3: Write `testdata/README.md`**

```markdown
# VSL golden fixtures

Raw `varnishlog -g request` output recorded from varnish:8.0.2 by
`record-fixtures.sh`. Parser and span-builder tests assert against these
files. Never hand-edit a fixture; re-run the script (requires Docker) and
commit the diff together with whatever parser change made it necessary.
```

- [ ] **Step 4: Commit**

```bash
git add internal/tracer/vsl/testdata
git commit -m "test(tracer): record golden VSL fixtures from varnish 8.0.2"
```

---

### Task 2: VSL parser (`internal/tracer/vsl`)

**Files:**
- Create: `internal/tracer/vsl/vsl.go`
- Test: `internal/tracer/vsl/vsl_test.go`

**Interfaces:**
- Produces (used by Task 3 and 5):

```go
type Record struct{ Tag, Payload string }
type Tx struct {
    Type     string // "Request", "BeReq", "Session"
    VXID     uint64
    Records  []Record
    Children []*Tx
}
func NewParser(r io.Reader) *Parser
func (p *Parser) Next() (*Tx, error)        // next top-level group; io.EOF at end
func (t *Tx) Timestamp(label string) (time.Time, bool) // "Start", "Resp", "Bereq", ...
func (t *Tx) Header(tag, name string) (string, bool)   // tag "ReqHeader"/"BereqHeader", name case-insensitive
func (t *Tx) Has(tag string) bool
func (t *Tx) First(tag string) (string, bool)          // payload of first record with tag
```

- [ ] **Step 1: Write failing tests against the fixtures**

```go
package vsl

import (
    "os"
    "testing"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func parseAll(t *testing.T, fixture string) []*Tx {
    t.Helper()
    f, err := os.Open("testdata/" + fixture)
    require.NoError(t, err)
    defer f.Close()
    var out []*Tx
    p := NewParser(f)
    for {
        tx, err := p.Next()
        if err != nil {
            break
        }
        out = append(out, tx)
    }
    return out
}

func TestParser_MissThenHit_GroupsAndNesting(t *testing.T) {
    txs := parseAll(t, "miss_then_hit.txt")
    require.Len(t, txs, 2)
    require.Equal(t, "Request", txs[0].Type)
    require.Len(t, txs[0].Children, 1, "miss must nest one BeReq")
    assert.Equal(t, "BeReq", txs[0].Children[0].Type)
    assert.Empty(t, txs[1].Children, "hit fetches nothing")
    assert.True(t, txs[1].Has("Hit"))
    assert.NotZero(t, txs[0].VXID)
}

func TestParser_TimestampsParse(t *testing.T) {
    txs := parseAll(t, "miss_then_hit.txt")
    start, ok := txs[0].Timestamp("Start")
    require.True(t, ok)
    resp, ok := txs[0].Timestamp("Resp")
    require.True(t, ok)
    assert.True(t, resp.After(start))
    _, ok = txs[0].Children[0].Timestamp("Bereq")
    assert.True(t, ok)
}

func TestParser_HeaderLookupCaseInsensitive(t *testing.T) {
    txs := parseAll(t, "miss_then_hit.txt")
    v, ok := txs[0].Header("ReqHeader", "TraceParent")
    require.True(t, ok)
    assert.Equal(t, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", v)
}

func TestParser_GarbageLinesAreSkippedNotFatal(t *testing.T) {
    txs := parseAll(t, "invalid_traceparent.txt")
    require.NotEmpty(t, txs)
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/tracer/vsl/ -run TestParser -v`
Expected: FAIL (package has no non-test Go files yet / undefined symbols).

- [ ] **Step 3: Implement the parser**

```go
// Package vsl parses varnishlog -g request text output into transaction
// groups. It is deliberately independent of the OTel model: it knows tags,
// payloads and nesting, nothing about spans.
package vsl

import (
    "bufio"
    "io"
    "math"
    "regexp"
    "strconv"
    "strings"
    "time"
)

type Record struct{ Tag, Payload string }

type Tx struct {
    Type     string
    VXID     uint64
    Records  []Record
    Children []*Tx
}

var (
    groupRe  = regexp.MustCompile(`^(\*+)\s+<<\s+(\w+)\s+>>\s+(\d+)\s*$`)
    recordRe = regexp.MustCompile(`^(-+)\s+(\S+)\s*(.*)$`)
)

type Parser struct {
    s *bufio.Scanner
    // byLevel[i] is the most recent Tx opened at star-depth i+1.
    byLevel []*Tx
    pending *Tx
    eof     bool
}

func NewParser(r io.Reader) *Parser {
    s := bufio.NewScanner(r)
    s.Buffer(make([]byte, 0, 64*1024), 1024*1024)
    return &Parser{s: s}
}

// Next returns the next completed top-level transaction group. A group is
// complete at the blank line that varnishlog prints after it, or at EOF.
func (p *Parser) Next() (*Tx, error) {
    if p.eof {
        return nil, io.EOF
    }
    for p.s.Scan() {
        line := p.s.Text()
        if strings.TrimSpace(line) == "" {
            if p.pending != nil {
                tx := p.pending
                p.pending = nil
                p.byLevel = nil
                return tx, nil
            }
            continue
        }
        if m := groupRe.FindStringSubmatch(line); m != nil {
            level := len(m[1])
            vxid, _ := strconv.ParseUint(m[3], 10, 64)
            tx := &Tx{Type: m[2], VXID: vxid}
            if level == 1 {
                // A new top-level group before a blank line: flush the old one.
                if p.pending != nil {
                    old := p.pending
                    p.pending = tx
                    p.byLevel = []*Tx{tx}
                    return old, nil
                }
                p.pending = tx
                p.byLevel = []*Tx{tx}
            } else if level-2 < len(p.byLevel) {
                parent := p.byLevel[level-2]
                parent.Children = append(parent.Children, tx)
                p.byLevel = append(p.byLevel[:level-1], tx)
            }
            continue
        }
        if m := recordRe.FindStringSubmatch(line); m != nil {
            level := len(m[1])
            if level-1 < len(p.byLevel) {
                t := p.byLevel[level-1]
                t.Records = append(t.Records, Record{Tag: m[2], Payload: m[3]})
            }
            continue
        }
        // Anything else (notices, truncation markers) is skipped, never fatal.
    }
    p.eof = true
    if p.pending != nil {
        tx := p.pending
        p.pending = nil
        return tx, nil
    }
    if err := p.s.Err(); err != nil {
        return nil, err
    }
    return nil, io.EOF
}

func (t *Tx) Has(tag string) bool {
    _, ok := t.First(tag)
    return ok
}

func (t *Tx) First(tag string) (string, bool) {
    for _, r := range t.Records {
        if r.Tag == tag {
            return r.Payload, true
        }
    }
    return "", false
}

// Timestamp finds a "Timestamp <label>: <epoch> ..." record and returns the
// absolute time. VSL epochs are fractional seconds.
func (t *Tx) Timestamp(label string) (time.Time, bool) {
    prefix := label + ": "
    for _, r := range t.Records {
        if r.Tag != "Timestamp" || !strings.HasPrefix(r.Payload, prefix) {
            continue
        }
        fields := strings.Fields(strings.TrimPrefix(r.Payload, prefix))
        if len(fields) == 0 {
            return time.Time{}, false
        }
        epoch, err := strconv.ParseFloat(fields[0], 64)
        if err != nil {
            return time.Time{}, false
        }
        sec, frac := math.Modf(epoch)
        return time.Unix(int64(sec), int64(frac*1e9)).UTC(), true
    }
    return time.Time{}, false
}

// Header returns the value of "<tag> <name>: <value>", name compared
// case-insensitively (HTTP header names are).
func (t *Tx) Header(tag, name string) (string, bool) {
    for _, r := range t.Records {
        if r.Tag != tag {
            continue
        }
        k, v, ok := strings.Cut(r.Payload, ":")
        if ok && strings.EqualFold(strings.TrimSpace(k), name) {
            return strings.TrimSpace(v), true
        }
    }
    return "", false
}
```

- [ ] **Step 4: Run tests until green — fixing the PARSER against the
  fixture, never the fixture against the parser**

Run: `go test ./internal/tracer/vsl/ -v`
Expected: PASS. If the recorded format differs (marker chars, spacing),
adjust the regexes and keep a comment quoting one real line.

- [ ] **Step 5: Lint and commit**

```bash
make lint
git add internal/tracer/vsl
git commit -m "feat(tracer): VSL request-grouped text parser"
```

---

### Task 3: Span builder (`internal/tracer/spans`)

**Files:**
- Create: `internal/tracer/spans/spans.go`
- Test: `internal/tracer/spans/spans_test.go`

**Interfaces:**
- Consumes: `vsl.Tx`, `vsl.Record` from Task 2 (exact signatures above).
- Produces (used by Tasks 4, 5):

```go
type Span struct {
    TraceID  trace.TraceID
    SpanID   trace.SpanID
    ParentID trace.SpanID // zero = root
    Name     string       // "varnish request" | "varnish fetch"
    Kind     trace.SpanKind
    Start    time.Time
    End      time.Time
    Attrs    []attribute.KeyValue
}
type IDSource interface {
    TraceID() trace.TraceID
    SpanID() trace.SpanID
}
func NewRandomIDs() IDSource
func Build(tx *vsl.Tx, ids IDSource) []Span // nil for unsampled or non-Request tx
```

Note: importing `go.opentelemetry.io/otel/trace` and `otel/attribute` here is
what promotes otel api to a direct dependency; `go mod tidy` in Step 3.

- [ ] **Step 1: Write failing tests against the fixtures**

```go
package spans

import (
    "os"
    "testing"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
    "go.opentelemetry.io/otel/trace"

    "github.com/bluedynamics/cloud-vinyl/internal/tracer/vsl"
)

// seqIDs is a deterministic IDSource for tests. It fakes only OUR interface,
// which we fully control; nothing external is imitated here.
type seqIDs struct{ n byte }

func (s *seqIDs) TraceID() trace.TraceID { s.n++; return trace.TraceID{0xaa, s.n} }
func (s *seqIDs) SpanID() trace.SpanID   { s.n++; return trace.SpanID{s.n} }

func fixtureTxs(t *testing.T, name string) []*vsl.Tx {
    t.Helper()
    f, err := os.Open("../vsl/testdata/" + name)
    require.NoError(t, err)
    defer f.Close()
    var out []*vsl.Tx
    p := vsl.NewParser(f)
    for {
        tx, err := p.Next()
        if err != nil {
            break
        }
        out = append(out, tx)
    }
    return out
}

func TestBuild_MissProducesRequestAndFetchSpans(t *testing.T) {
    txs := fixtureTxs(t, "miss_then_hit.txt")
    got := Build(txs[0], &seqIDs{})
    require.Len(t, got, 2)
    req, fetch := got[0], got[1]

    // Trace context inherited from the recorded traceparent.
    assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", req.TraceID.String())
    assert.Equal(t, "00f067aa0ba902b7", req.ParentID.String())

    assert.Equal(t, "varnish request", req.Name)
    assert.Equal(t, trace.SpanKindServer, req.Kind)
    assert.Equal(t, "varnish fetch", fetch.Name)
    assert.Equal(t, trace.SpanKindClient, fetch.Kind)
    assert.Equal(t, req.SpanID, fetch.ParentID, "fetch is child of request")
    assert.Equal(t, req.TraceID, fetch.TraceID)
    assert.False(t, req.Start.IsZero())
    assert.True(t, req.End.After(req.Start))
    assert.True(t, fetch.Start.After(req.Start) || fetch.Start.Equal(req.Start))
    assert.Equal(t, "miss", attrString(t, req.Attrs, "varnish.handling"))
}

func TestBuild_HitHasNoFetchSpan(t *testing.T) {
    txs := fixtureTxs(t, "miss_then_hit.txt")
    got := Build(txs[1], &seqIDs{})
    require.Len(t, got, 1)
    assert.Equal(t, "hit", attrString(t, got[0].Attrs, "varnish.handling"))
}

func TestBuild_NoTraceparentSelfRoots(t *testing.T) {
    txs := fixtureTxs(t, "no_traceparent.txt")
    got := Build(txs[0], &seqIDs{})
    require.NotEmpty(t, got)
    assert.False(t, got[0].TraceID.IsValid() == false, "must mint a trace id")
    assert.Equal(t, trace.SpanID{}, got[0].ParentID, "root has zero parent")
}

func TestBuild_InvalidTraceparentSelfRoots(t *testing.T) {
    txs := fixtureTxs(t, "invalid_traceparent.txt")
    got := Build(txs[0], &seqIDs{})
    require.NotEmpty(t, got)
    assert.Equal(t, trace.SpanID{}, got[0].ParentID)
}

func TestBuild_PassIsPass(t *testing.T) {
    txs := fixtureTxs(t, "pass.txt")
    got := Build(txs[0], &seqIDs{})
    require.NotEmpty(t, got)
    assert.Equal(t, "pass", attrString(t, got[0].Attrs, "varnish.handling"))
}

func TestParseTraceparent_UnsampledMeansNoSpans(t *testing.T) {
    txs := fixtureTxs(t, "miss_then_hit.txt")
    // Rewrite the recorded header's flags to 00 in-memory.
    for i, r := range txs[0].Records {
        if r.Tag == "ReqHeader" && len(r.Payload) > 2 {
            txs[0].Records[i].Payload =
                r.Payload[:len(r.Payload)-2] + "00"
        }
    }
    assert.Nil(t, Build(txs[0], &seqIDs{}))
}
```

Also write the small helper used above:

```go
func attrString(t *testing.T, attrs []attribute.KeyValue, key string) string {
    t.Helper()
    for _, a := range attrs {
        if string(a.Key) == key {
            return a.Value.Emit()
        }
    }
    t.Fatalf("attribute %q not found", key)
    return ""
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/tracer/spans/ -v`
Expected: FAIL (undefined `Build` etc.). `go mod tidy` first if the otel
import breaks compilation of the test file itself.

- [ ] **Step 3: Implement**

```go
// Package spans turns parsed VSL transaction groups into OTel-shaped spans
// with explicit ids and real VSL timestamps. P1 scope: request + fetch spans,
// parenting from the incoming traceparent (the backend stays a sibling until
// the P2 VCL rewrite lands).
package spans

import (
    "crypto/rand"
    "encoding/hex"
    "regexp"
    "strconv"
    "strings"
    "time"

    "go.opentelemetry.io/otel/attribute"
    "go.opentelemetry.io/otel/trace"

    "github.com/bluedynamics/cloud-vinyl/internal/tracer/vsl"
)

type Span struct {
    TraceID  trace.TraceID
    SpanID   trace.SpanID
    ParentID trace.SpanID
    Name     string
    Kind     trace.SpanKind
    Start    time.Time
    End      time.Time
    Attrs    []attribute.KeyValue
}

type IDSource interface {
    TraceID() trace.TraceID
    SpanID() trace.SpanID
}

type randomIDs struct{}

func NewRandomIDs() IDSource { return randomIDs{} }

func (randomIDs) TraceID() trace.TraceID {
    var id trace.TraceID
    _, _ = rand.Read(id[:])
    return id
}

func (randomIDs) SpanID() trace.SpanID {
    var id trace.SpanID
    _, _ = rand.Read(id[:])
    return id
}

var traceparentRe = regexp.MustCompile(
    `^([0-9a-f]{2})-([0-9a-f]{32})-([0-9a-f]{16})-([0-9a-f]{2})$`)

// parseTraceparent returns (traceID, parentSpanID, sampled, ok).
func parseTraceparent(v string) (trace.TraceID, trace.SpanID, bool, bool) {
    m := traceparentRe.FindStringSubmatch(strings.TrimSpace(v))
    if m == nil || m[1] == "ff" ||
        m[2] == strings.Repeat("0", 32) || m[3] == strings.Repeat("0", 16) {
        return trace.TraceID{}, trace.SpanID{}, false, false
    }
    var tid trace.TraceID
    var sid trace.SpanID
    _, _ = hex.Decode(tid[:], []byte(m[2]))
    _, _ = hex.Decode(sid[:], []byte(m[3]))
    flags, _ := strconv.ParseUint(m[4], 16, 8)
    return tid, sid, flags&0x01 == 0x01, true
}

// Build returns the spans for one top-level Request group, or nil when the
// transaction is unsampled or not a client request.
func Build(tx *vsl.Tx, ids IDSource) []Span {
    if tx.Type != "Request" {
        return nil
    }
    start, okStart := tx.Timestamp("Start")
    end, okEnd := tx.Timestamp("Resp")
    if !okStart || !okEnd {
        return nil // incomplete group (e.g. truncated log); counted by caller
    }

    var traceID trace.TraceID
    var parentID trace.SpanID
    if raw, ok := tx.Header("ReqHeader", "traceparent"); ok {
        if tid, sid, sampled, valid := parseTraceparent(raw); valid {
            if !sampled {
                return nil
            }
            traceID, parentID = tid, sid
        }
    }
    if !traceID.IsValid() {
        traceID = ids.TraceID() // self-rooted: Varnish is the edge
    }

    req := Span{
        TraceID:  traceID,
        SpanID:   ids.SpanID(),
        ParentID: parentID,
        Name:     "varnish request",
        Kind:     trace.SpanKindServer,
        Start:    start,
        End:      end,
        Attrs:    requestAttrs(tx),
    }
    out := []Span{req}

    for _, child := range tx.Children {
        if child.Type != "BeReq" {
            continue
        }
        fs, okF := child.Timestamp("Bereq")
        fe, okE := child.Timestamp("BerespBody")
        if !okE {
            fe, okE = child.Timestamp("Beresp")
        }
        if !okF || !okE {
            continue
        }
        out = append(out, Span{
            TraceID:  traceID,
            SpanID:   ids.SpanID(),
            ParentID: req.SpanID,
            Name:     "varnish fetch",
            Kind:     trace.SpanKindClient,
            Start:    fs,
            End:      fe,
            Attrs:    fetchAttrs(child),
        })
    }
    return out
}

func requestAttrs(tx *vsl.Tx) []attribute.KeyValue {
    var attrs []attribute.KeyValue
    if m, ok := tx.First("ReqMethod"); ok {
        attrs = append(attrs, attribute.String("http.request.method", m))
    }
    if u, ok := tx.First("ReqURL"); ok {
        attrs = append(attrs, attribute.String("url.path", u))
    }
    if s, ok := tx.First("RespStatus"); ok {
        if n, err := strconv.Atoi(s); err == nil {
            attrs = append(attrs, attribute.Int("http.response.status_code", n))
        }
    }
    attrs = append(attrs, attribute.String("varnish.handling", handling(tx)))
    // ReqAcct: "reqhdr reqbody reqtotal resphdr respbody resptotal"
    if a, ok := tx.First("ReqAcct"); ok {
        if f := strings.Fields(a); len(f) == 6 {
            if n, err := strconv.Atoi(f[5]); err == nil {
                attrs = append(attrs, attribute.Int("varnish.resp_bytes", n))
            }
        }
    }
    return attrs
}

// handling derives hit/miss/pass/synth/pipe from the VCL_call sequence,
// with a Hit record as corroboration for hits.
func handling(tx *vsl.Tx) string {
    for _, r := range tx.Records {
        if r.Tag != "VCL_call" {
            continue
        }
        switch r.Payload {
        case "HIT":
            return "hit"
        case "PASS":
            return "pass"
        case "SYNTH":
            return "synth"
        case "MISS":
            return "miss"
        }
    }
    if tx.Has("Hit") {
        return "hit"
    }
    return "miss"
}

func fetchAttrs(tx *vsl.Tx) []attribute.KeyValue {
    var attrs []attribute.KeyValue
    // BackendOpen: "<fd> <name> <ip> <port> ..." — name is field 2.
    if b, ok := tx.First("BackendOpen"); ok {
        if f := strings.Fields(b); len(f) >= 2 {
            attrs = append(attrs, attribute.String("varnish.backend", f[1]))
        }
    }
    if s, ok := tx.First("BerespStatus"); ok {
        if n, err := strconv.Atoi(s); err == nil {
            attrs = append(attrs, attribute.Int("http.response.status_code", n))
        }
    }
    if b, ok := tx.First("Begin"); ok && strings.Contains(b, "bgfetch") {
        attrs = append(attrs, attribute.Bool("varnish.bgfetch", true))
    }
    return attrs
}
```

- [ ] **Step 4: `go mod tidy`, run tests until green**

Run: `go mod tidy && go test ./internal/tracer/... -v`
Expected: PASS. Check `go.mod`: `go.opentelemetry.io/otel` and `otel/trace`
moved to the direct require block. If a fixture assertion fails on
`varnish.handling`, read the fixture's `VCL_call` records and fix `handling`,
not the test.

- [ ] **Step 5: Lint and commit**

```bash
make lint
git add internal/tracer/spans go.mod go.sum
git commit -m "feat(tracer): span builder with real VSL timestamps"
```

---

### Task 4: Exporter + bounded batcher + metrics (`internal/tracer/export`)

**Files:**
- Create: `internal/tracer/export/readonly.go`
- Create: `internal/tracer/export/batcher.go`
- Test: `internal/tracer/export/batcher_test.go`

**Interfaces:**
- Consumes: `spans.Span` (Task 3).
- Produces (used by Task 5):

```go
func NewExporter(ctx context.Context, endpoint, protocol string, insecure bool) (sdktrace.SpanExporter, error)
func NewBatcher(exp sdktrace.SpanExporter, serviceName string, queueLen int) *Batcher
func (b *Batcher) Enqueue(s spans.Span) // never blocks; drops + counts when full
func (b *Batcher) Run(ctx context.Context) // flush loop; returns on ctx cancel after final flush
```
- Prometheus counters registered on the default registry:
  `vinyl_tracer_spans_exported_total`, `vinyl_tracer_spans_dropped_total`,
  `vinyl_tracer_export_errors_total`.

- [ ] **Step 1: Align and promote the otel dependencies**

```bash
go get go.opentelemetry.io/otel@v1.41.0 \
       go.opentelemetry.io/otel/sdk@v1.41.0 \
       go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc@v1.41.0 \
       go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp@v1.41.0
go mod tidy
```

If v1.41.0 does not exist for sdk/exporters, use the highest common version
`go list -m -versions go.opentelemetry.io/otel/sdk` shows for ALL four — the
skew (api 1.41.0 vs sdk 1.40.0) must end here, in whichever direction.

- [ ] **Step 2: Write the failing batcher test using otel's in-memory exporter**

`tracetest.NewInMemoryExporter()` (from
`go.opentelemetry.io/otel/sdk/trace/tracetest`) consumes the same
`ReadOnlySpan` interface the real OTLP exporter does, so it is a
counterpart-true double.

```go
package export

import (
    "context"
    "testing"
    "time"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
    "go.opentelemetry.io/otel/sdk/trace/tracetest"
    "go.opentelemetry.io/otel/trace"

    "github.com/bluedynamics/cloud-vinyl/internal/tracer/spans"
)

func testSpan(n byte) spans.Span {
    return spans.Span{
        TraceID: trace.TraceID{0xaa, n}, SpanID: trace.SpanID{n},
        Name: "varnish request", Kind: trace.SpanKindServer,
        Start: time.Unix(100, 0), End: time.Unix(101, 0),
    }
}

func TestBatcher_ExportsEnqueuedSpans(t *testing.T) {
    mem := tracetest.NewInMemoryExporter()
    b := NewBatcher(mem, "test-svc", 16)
    ctx, cancel := context.WithCancel(context.Background())
    done := make(chan struct{})
    go func() { b.Run(ctx); close(done) }()

    b.Enqueue(testSpan(1))
    b.Enqueue(testSpan(2))
    cancel() // Run flushes on shutdown
    <-done

    got := mem.GetSpans()
    require.Len(t, got, 2)
    assert.Equal(t, "varnish request", got[0].Name)
    assert.Equal(t, trace.SpanID{1}, got[0].SpanContext.SpanID())
    assert.False(t, got[0].StartTime.IsZero())
    assert.Equal(t, "test-svc", svcName(t, got[0]))
}

func TestBatcher_DropsWhenFullWithoutBlocking(t *testing.T) {
    mem := tracetest.NewInMemoryExporter()
    b := NewBatcher(mem, "test-svc", 1) // queue of one
    // No Run() consuming: the second Enqueue must return immediately.
    okCh := make(chan struct{})
    go func() {
        b.Enqueue(testSpan(1))
        b.Enqueue(testSpan(2))
        close(okCh)
    }()
    select {
    case <-okCh:
    case <-time.After(time.Second):
        t.Fatal("Enqueue blocked on a full queue")
    }
}
```

`svcName` walks `got[0].Resource.Attributes()` for `service.name`; write it
as a t.Helper in the test file.

- [ ] **Step 3: Run to verify failure**

Run: `go test ./internal/tracer/export/ -v`
Expected: FAIL (undefined NewBatcher).

- [ ] **Step 4: Implement `readonly.go` (the spans.Span → ReadOnlySpan adapter)**

```go
package export

import (
    "time"

    "go.opentelemetry.io/otel/attribute"
    "go.opentelemetry.io/otel/sdk/instrumentation"
    "go.opentelemetry.io/otel/sdk/resource"
    sdktrace "go.opentelemetry.io/otel/sdk/trace"
    "go.opentelemetry.io/otel/trace"

    "github.com/bluedynamics/cloud-vinyl/internal/tracer/spans"
)

// roSpan adapts spans.Span to sdktrace.ReadOnlySpan so the stock otlptrace
// exporters accept our externally-minted ids and timestamps. The OTel SDK
// tracer cannot do this: it insists on generating ids itself.
type roSpan struct {
    s     spans.Span
    res   *resource.Resource
    scope instrumentation.Scope
}

func (r roSpan) Name() string { return r.s.Name }
func (r roSpan) SpanContext() trace.SpanContext {
    return trace.NewSpanContext(trace.SpanContextConfig{
        TraceID: r.s.TraceID, SpanID: r.s.SpanID,
        TraceFlags: trace.FlagsSampled,
    })
}
func (r roSpan) Parent() trace.SpanContext {
    if !r.s.ParentID.IsValid() {
        return trace.SpanContext{}
    }
    return trace.NewSpanContext(trace.SpanContextConfig{
        TraceID: r.s.TraceID, SpanID: r.s.ParentID,
        TraceFlags: trace.FlagsSampled, Remote: true,
    })
}
func (r roSpan) SpanKind() trace.SpanKind            { return r.s.Kind }
func (r roSpan) StartTime() time.Time                { return r.s.Start }
func (r roSpan) EndTime() time.Time                  { return r.s.End }
func (r roSpan) Attributes() []attribute.KeyValue    { return r.s.Attrs }
func (r roSpan) Links() []sdktrace.Link              { return nil }
func (r roSpan) Events() []sdktrace.Event            { return nil }
func (r roSpan) Status() sdktrace.Status             { return sdktrace.Status{} }
func (r roSpan) InstrumentationScope() instrumentation.Scope { return r.scope }
func (r roSpan) Resource() *resource.Resource        { return r.res }
func (r roSpan) DroppedAttributes() int              { return 0 }
func (r roSpan) DroppedLinks() int                   { return 0 }
func (r roSpan) DroppedEvents() int                  { return 0 }
func (r roSpan) ChildSpanCount() int                 { return 0 }
```

Then `go build ./internal/tracer/export/`: the compiler will name any
interface methods the installed SDK version still requires (a deprecated
`InstrumentationLibrary()` may be one; add it delegating to the scope value
if so). `ended()`-style unexported methods cannot be required by an external
interface; if the build reports one, the SDK version chosen in Step 1 is
wrong — pick one where `ReadOnlySpan` is implementable out-of-tree (1.40/1.41
are; this is why the version alignment step comes first).

- [ ] **Step 5: Implement `batcher.go`**

```go
package export

import (
    "context"
    "fmt"
    "time"

    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promauto"
    "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
    "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
    "go.opentelemetry.io/otel/sdk/instrumentation"
    "go.opentelemetry.io/otel/sdk/resource"
    sdktrace "go.opentelemetry.io/otel/sdk/trace"
    semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

    "github.com/bluedynamics/cloud-vinyl/internal/tracer/spans"
)

var (
    exportedTotal = promauto.NewCounter(prometheus.CounterOpts{
        Name: "vinyl_tracer_spans_exported_total",
        Help: "Spans handed to the OTLP exporter.",
    })
    droppedTotal = promauto.NewCounter(prometheus.CounterOpts{
        Name: "vinyl_tracer_spans_dropped_total",
        Help: "Spans dropped because the export queue was full.",
    })
    exportErrors = promauto.NewCounter(prometheus.CounterOpts{
        Name: "vinyl_tracer_export_errors_total",
        Help: "Failed OTLP export batches.",
    })
)

// NewExporter builds the OTLP span exporter. protocol: "grpc" or
// "http/protobuf".
func NewExporter(ctx context.Context, endpoint, protocol string, insecure bool) (sdktrace.SpanExporter, error) {
    switch protocol {
    case "", "grpc":
        opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(endpoint)}
        if insecure {
            opts = append(opts, otlptracegrpc.WithInsecure())
        }
        return otlptracegrpc.New(ctx, opts...)
    case "http/protobuf":
        opts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(endpoint)}
        if insecure {
            opts = append(opts, otlptracehttp.WithInsecure())
        }
        return otlptracehttp.New(ctx, opts...)
    default:
        return nil, fmt.Errorf("unknown OTLP protocol %q", protocol)
    }
}

type Batcher struct {
    ch    chan spans.Span
    exp   sdktrace.SpanExporter
    res   *resource.Resource
    scope instrumentation.Scope
}

func NewBatcher(exp sdktrace.SpanExporter, serviceName string, queueLen int) *Batcher {
    return &Batcher{
        ch:  make(chan spans.Span, queueLen),
        exp: exp,
        res: resource.NewWithAttributes(semconv.SchemaURL,
            semconv.ServiceName(serviceName)),
        scope: instrumentation.Scope{Name: "vinyl-tracer"},
    }
}

// Enqueue never blocks: the request path must never notice the tracer, and
// trace loss must be a visible counter, not backpressure.
func (b *Batcher) Enqueue(s spans.Span) {
    select {
    case b.ch <- s:
    default:
        droppedTotal.Inc()
    }
}

// Run flushes batches every interval until ctx is cancelled, then drains.
func (b *Batcher) Run(ctx context.Context) {
    const flushEvery = 3 * time.Second
    const maxBatch = 512
    tick := time.NewTicker(flushEvery)
    defer tick.Stop()
    var buf []sdktrace.ReadOnlySpan
    flush := func() {
        if len(buf) == 0 {
            return
        }
        expCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
        if err := b.exp.ExportSpans(expCtx, buf); err != nil {
            exportErrors.Inc()
        } else {
            exportedTotal.Add(float64(len(buf)))
        }
        cancel()
        buf = buf[:0]
    }
    for {
        select {
        case s := <-b.ch:
            buf = append(buf, roSpan{s: s, res: b.res, scope: b.scope})
            if len(buf) >= maxBatch {
                flush()
            }
        case <-tick.C:
            flush()
        case <-ctx.Done():
            for {
                select {
                case s := <-b.ch:
                    buf = append(buf, roSpan{s: s, res: b.res, scope: b.scope})
                default:
                    flush()
                    return
                }
            }
        }
    }
}
```

- [ ] **Step 6: Run tests until green**

Run: `go test ./internal/tracer/... -v`
Expected: PASS, including both new batcher tests.

- [ ] **Step 7: Lint and commit**

```bash
make lint
git add internal/tracer/export go.mod go.sum
git commit -m "feat(tracer): OTLP exporter with bounded drop-counting batcher"
```

---

### Task 5: `cmd/tracer` main with varnishlog supervision

**Files:**
- Create: `cmd/tracer/main.go`
- Create: `cmd/tracer/supervise.go`
- Test: `cmd/tracer/supervise_test.go`
- Modify: `Makefile` (`build` target, around line 102)

**Interfaces:**
- Consumes: Tasks 2–4 (`vsl.NewParser`, `spans.Build`, `spans.NewRandomIDs`, `export.NewExporter`, `export.NewBatcher`).
- Env contract (Task 7 sets these from the CRD): `OTLP_ENDPOINT` (required),
  `OTLP_PROTOCOL` (grpc|http/protobuf, default grpc), `OTLP_INSECURE`
  ("true"/"false", default false), `TRACER_SERVICE_NAME` (required),
  `TRACER_METRICS_ADDR` (default `:9464`), `VARNISHLOG_PATH` (default
  `varnishlog`, for tests).

Conventions from `cmd/agent/main.go`: env-only (no flags), `envOrDefault`
helper, numbered startup comments, `signal.NotifyContext(SIGINT, SIGTERM)`,
errors to stderr + `os.Exit(1)`.

- [ ] **Step 1: Write the failing supervision test**

The loop is factored so tests drive it with a stub script instead of real
varnishlog — the stub imitates only "a subprocess that emits VSL text and
exits", which is exactly the contract, and the VSL text itself comes from a
recorded fixture (never hand-written).

```go
package main

import (
    "context"
    "os"
    "path/filepath"
    "testing"
    "time"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

// writeStub creates an executable that cats a real fixture once, then exits,
// simulating varnishlog dying (varnishd restart).
func writeStub(t *testing.T) string {
    t.Helper()
    fixture, err := filepath.Abs("../../internal/tracer/vsl/testdata/miss_then_hit.txt")
    require.NoError(t, err)
    dir := t.TempDir()
    stub := filepath.Join(dir, "varnishlog-stub")
    script := "#!/bin/sh\ncat \"" + fixture + "\"\nexit 1\n"
    require.NoError(t, os.WriteFile(stub, []byte(script), 0o755))
    return stub
}

func TestSupervise_ParsesOutputAndRestartsOnExit(t *testing.T) {
    stub := writeStub(t)
    var groups int
    restarts := make(chan struct{}, 8)

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    s := &supervisor{
        binary:  stub,
        backoff: 10 * time.Millisecond,
        onGroup: func() { groups++ },
        onRestart: func() {
            restarts <- struct{}{}
            if len(restarts) >= 2 {
                cancel() // saw at least two spawns: restart works
            }
        },
    }
    s.run(ctx)
    assert.GreaterOrEqual(t, groups, 2, "fixture has two request groups")
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cmd/tracer/ -v`
Expected: FAIL (no package yet).

- [ ] **Step 3: Implement `supervise.go`**

```go
package main

import (
    "context"
    "fmt"
    "os"
    "os/exec"
    "time"

    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promauto"

    "github.com/bluedynamics/cloud-vinyl/internal/tracer/vsl"
)

var (
    varnishlogRestarts = promauto.NewCounter(prometheus.CounterOpts{
        Name: "vinyl_tracer_varnishlog_restarts_total",
        Help: "Times the varnishlog subprocess was (re)spawned.",
    })
    groupsUnusable = promauto.NewCounter(prometheus.CounterOpts{
        Name: "vinyl_tracer_groups_unusable_total",
        Help: "Request groups that yielded no spans (truncated/overrun log data). Trace loss must be visible, never silent.",
    })
)

// supervisor keeps one varnishlog subprocess running and feeds its stdout
// through the VSL parser. handle is called per parsed top-level group;
// onGroup/onRestart are test seams and metrics hooks.
type supervisor struct {
    binary    string
    backoff   time.Duration // initial; doubles to a 30s cap, resets on output
    handle    func(*vsl.Tx)
    onGroup   func()
    onRestart func()
}

func (s *supervisor) run(ctx context.Context) {
    backoff := s.backoff
    for ctx.Err() == nil {
        varnishlogRestarts.Inc()
        if s.onRestart != nil {
            s.onRestart()
        }
        // -t off: wait for the VSM indefinitely, so the sidecar starting
        // before varnishd is not an error.
        cmd := exec.CommandContext(ctx, s.binary, "-g", "request", "-t", "off")
        cmd.Stderr = os.Stderr
        out, err := cmd.StdoutPipe()
        if err == nil {
            if err = cmd.Start(); err == nil {
                p := vsl.NewParser(out)
                for {
                    tx, perr := p.Next()
                    if perr != nil {
                        break
                    }
                    backoff = s.backoff // making progress: reset backoff
                    if s.handle != nil {
                        s.handle(tx)
                    }
                    if s.onGroup != nil {
                        s.onGroup()
                    }
                }
                _ = cmd.Wait()
            }
        }
        if err != nil {
            fmt.Fprintf(os.Stderr, "varnishlog spawn: %v\n", err)
        }
        select {
        case <-ctx.Done():
            return
        case <-time.After(backoff):
        }
        if backoff *= 2; backoff > 30*time.Second {
            backoff = 30 * time.Second
        }
    }
}
```

- [ ] **Step 4: Implement `main.go`**

```go
// vinyl-tracer reads the Varnish Shared memory Log via a version-matched
// varnishlog subprocess, builds OTel spans with real VSL timestamps, and
// exports them via OTLP. It runs as a sidecar in the varnish pod; the
// operator sets its environment from spec.tracing.
package main

import (
    "context"
    "fmt"
    "net/http"
    "os"
    "os/signal"
    "syscall"
    "time"

    "github.com/prometheus/client_golang/prometheus/promhttp"

    "github.com/bluedynamics/cloud-vinyl/internal/tracer/export"
    "github.com/bluedynamics/cloud-vinyl/internal/tracer/spans"
    "github.com/bluedynamics/cloud-vinyl/internal/tracer/vsl"
)

func envOrDefault(key, def string) string {
    if v := os.Getenv(key); v != "" {
        return v
    }
    return def
}

func main() {
    ctx, stop := signal.NotifyContext(context.Background(),
        syscall.SIGINT, syscall.SIGTERM)
    defer stop()

    endpoint := os.Getenv("OTLP_ENDPOINT")
    service := os.Getenv("TRACER_SERVICE_NAME")
    if endpoint == "" || service == "" {
        fmt.Fprintln(os.Stderr, "OTLP_ENDPOINT and TRACER_SERVICE_NAME are required")
        os.Exit(1)
    }

    // 1. OTLP exporter + batcher.
    exp, err := export.NewExporter(ctx, endpoint,
        envOrDefault("OTLP_PROTOCOL", "grpc"),
        os.Getenv("OTLP_INSECURE") == "true")
    if err != nil {
        fmt.Fprintf(os.Stderr, "otlp exporter: %v\n", err)
        os.Exit(1)
    }
    batcher := export.NewBatcher(exp, service, 2048)
    go batcher.Run(ctx)

    // 2. Metrics endpoint.
    go func() {
        mux := http.NewServeMux()
        mux.Handle("/metrics", promhttp.Handler())
        srv := &http.Server{
            Addr:              envOrDefault("TRACER_METRICS_ADDR", ":9464"),
            Handler:           mux,
            ReadHeaderTimeout: 5 * time.Second,
        }
        if err := srv.ListenAndServe(); err != nil {
            fmt.Fprintf(os.Stderr, "metrics server: %v\n", err)
        }
    }()

    // 3. Supervise varnishlog and pump groups into the pipeline.
    ids := spans.NewRandomIDs()
    s := &supervisor{
        binary:  envOrDefault("VARNISHLOG_PATH", "varnishlog"),
        backoff: time.Second,
        handle: func(tx *vsl.Tx) {
            built := spans.Build(tx, ids)
            if len(built) == 0 && tx.Type == "Request" {
                // Unsampled is intentional silence; a Request group with no
                // usable timestamps is data loss and must be counted. Build
                // cannot tell us which it was cheaply in P1, so count both;
                // unsampled traffic is rare in the deployments this targets.
                groupsUnusable.Inc()
            }
            for _, sp := range built {
                batcher.Enqueue(sp)
            }
        },
    }
    s.run(ctx)
}
```

- [ ] **Step 5: Add the build line and run everything**

In `Makefile`, `build` target, after the agent line:

```make
	go build -o bin/tracer ./cmd/tracer
```

Run: `go test ./cmd/tracer/ -v && make build`
Expected: tests PASS, `bin/tracer` builds.

- [ ] **Step 6: Lint and commit**

```bash
make lint
git add cmd/tracer Makefile
git commit -m "feat(tracer): tracer binary with varnishlog supervision"
```

---

### Task 6: `Dockerfile.tracer`

**Files:**
- Create: `Dockerfile.tracer`
- Modify: `Makefile` (image targets block, near `docker-build-agent` ~line 124)

Mirrors `Dockerfile.exporter`'s pattern (matched binary copied out of the
varnish image + soname guard, see #91) but for `varnishlog`; runtime must be
distroless **base** (varnishlog needs glibc), our Go binary stays static.

- [ ] **Step 1: Write the Dockerfile**

```dockerfile
# vinyl-tracer: reads the VSL via a varnishlog copied from the SAME varnish
# image the cache runs (the VSL shm layout is not a stable ABI), and exports
# OTLP spans. Keep VARNISH_IMAGE in sync with the cache image.
#
# Published as ghcr.io/bluedynamics/cloud-vinyl-tracer:<version>.

ARG VARNISH_IMAGE=varnish:8.0.2

FROM golang:1.26 AS build
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o tracer ./cmd/tracer

# Source of a matching varnishlog + its runtime libraries. The guard RUN
# lives here (a COPY'd-from stage) so BuildKit cannot prune it. varnish 6.0
# ships libvarnishapi.so.1 and is unsupported; see #91.
FROM ${VARNISH_IMAGE} AS varnish
RUN test -e /usr/lib/libvarnishapi.so.3 || { \
        echo "ERROR: VARNISH_IMAGE ships no libvarnishapi.so.3, found:" >&2; \
        ls -1 /usr/lib/libvarnishapi.so.* >&2; \
        exit 1; }

# Must match the Debian release of VARNISH_IMAGE (varnish:8.0.2 = Debian 13)
# so varnishlog finds a compatible glibc.
FROM gcr.io/distroless/base-debian13:nonroot
LABEL org.opencontainers.image.title="vinyl-tracer" \
      org.opencontainers.image.source="https://github.com/bluedynamics/cloud-vinyl" \
      org.opencontainers.image.licenses="Apache-2.0"
COPY --from=varnish /usr/bin/varnishlog /usr/bin/varnishlog
COPY --from=varnish /usr/lib/libvarnishapi.so.3* /usr/lib/
COPY --from=varnish /lib/*-linux-gnu/libpcre2-8.so.0 /usr/lib/libpcre2-8.so.0
COPY --from=build /workspace/tracer /tracer
USER 65532:65532
EXPOSE 9464
ENTRYPOINT ["/tracer"]
```

- [ ] **Step 2: Add the Makefile target**

```make
TRACER_IMG ?= ghcr.io/bluedynamics/cloud-vinyl-tracer:latest

.PHONY: docker-build-tracer
docker-build-tracer: ## Build the tracer docker image (VARNISH_IMAGE pins the varnishlog source).
	$(CONTAINER_TOOL) build -t $(TRACER_IMG) -f Dockerfile.tracer .
```

- [ ] **Step 3: Build and prove the bundled varnishlog actually runs**

Run:
```bash
make docker-build-tracer
docker run --rm --entrypoint /usr/bin/varnishlog \
    ghcr.io/bluedynamics/cloud-vinyl-tracer:latest -V
```
Expected: build succeeds; `-V` prints a varnishlog 8.0.x version banner. If
it fails on a missing shared library, `docker run --rm --entrypoint /bin/sh
varnish:8.0.2 -c "ldd /usr/bin/varnishlog"` lists what to COPY, add it, and
rebuild — the exporter Dockerfile shows the naming pattern.

Also prove the guard fails red once (CLAUDE.md rule for checks): build with
`--build-arg VARNISH_IMAGE=varnish:6.0` and expect the guard error; do not
commit that state.

- [ ] **Step 4: Commit**

```bash
git add Dockerfile.tracer Makefile
git commit -m "feat(tracer): version-locked tracer image"
```

---

### Task 7: CRD `TracingSpec` + validation

**Files:**
- Modify: `api/v1alpha1/vinylcache_types.go` (add types; new field after `Monitoring` at ~line 115)
- Modify: `internal/webhook/vinylcache_validator.go` (~line 123 `ValidateVinylCache`)
- Test: `internal/webhook/vinylcache_validator_test.go`
- Regenerate: `api/v1alpha1/zz_generated.deepcopy.go`, `config/crd/bases/vinyl.bluedynamics.eu_vinylcaches.yaml`, `charts/cloud-vinyl/crds/vinylcache.yaml`

**Interfaces:**
- Produces (used by Task 8):

```go
type TracingSpec struct {
    Enabled     bool                        `json:"enabled,omitempty"`
    OTLP        OTLPSpec                    `json:"otlp,omitempty"`
    ServiceName string                      `json:"serviceName,omitempty"`
    Resources   corev1.ResourceRequirements `json:"resources,omitempty"`
}
type OTLPSpec struct {
    Endpoint string `json:"endpoint,omitempty"`
    Protocol string `json:"protocol,omitempty"` // enum grpc|http/protobuf
    Insecure bool   `json:"insecure,omitempty"`
}
```
Defaults (protocol→grpc, serviceName→VinylCache name) are applied in the
controller builder (Task 8), following the exporter convention. The webhook
only validates.

- [ ] **Step 1: Write the failing validator tests**

Follow the existing style in `vinylcache_validator_test.go` (plain testing +
testify; construct a `*v1alpha1.VinylCache`, call `ValidateVinylCache`,
assert on the error):

```go
func TestValidateVinylCache_TracingRequiresEndpoint(t *testing.T) {
    vc := validVinylCache() // reuse the fixture helper the file already has
    vc.Spec.Tracing = v1alpha1.TracingSpec{Enabled: true}
    _, err := webhook.ValidateVinylCache(vc)
    require.Error(t, err)
    assert.Contains(t, err.Error(), "tracing.otlp.endpoint is required")
}

func TestValidateVinylCache_TracingEndpointMustBeHostPort(t *testing.T) {
    vc := validVinylCache()
    vc.Spec.Tracing = v1alpha1.TracingSpec{
        Enabled: true,
        OTLP:    v1alpha1.OTLPSpec{Endpoint: "no-port-here"},
    }
    _, err := webhook.ValidateVinylCache(vc)
    require.Error(t, err)
    assert.Contains(t, err.Error(), "tracing.otlp.endpoint")
}

func TestValidateVinylCache_TracingDisabledSkipsChecks(t *testing.T) {
    vc := validVinylCache()
    vc.Spec.Tracing = v1alpha1.TracingSpec{} // disabled, empty
    _, err := webhook.ValidateVinylCache(vc)
    assert.NoError(t, err)
}
```

(If the test file's existing valid-CR helper has a different name, use that
one; do not invent a second fixture builder.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/webhook/ -run TestValidateVinylCache_Tracing -v`
Expected: FAIL to compile (no TracingSpec yet).

- [ ] **Step 3: Add the API types**

In `vinylcache_types.go`, after the `Monitoring` field:

```go
	// tracing configures the vinyl-tracer sidecar that exports OpenTelemetry
	// trace spans built from the Varnish Shared memory Log.
	// +optional
	Tracing TracingSpec `json:"tracing,omitempty"`
```

and next to `MonitoringSpec`:

```go
// TracingSpec configures OpenTelemetry trace export for the Varnish cluster.
type TracingSpec struct {
	// enabled activates the vinyl-tracer sidecar.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// otlp configures the OTLP trace exporter. endpoint is required when
	// tracing is enabled.
	// +optional
	OTLP OTLPSpec `json:"otlp,omitempty"`

	// serviceName is the OTel service.name resource attribute.
	// Defaults to the VinylCache name.
	// +optional
	ServiceName string `json:"serviceName,omitempty"`

	// resources are the tracer container's resource requirements.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// OTLPSpec is the OTLP exporter configuration.
type OTLPSpec struct {
	// endpoint is the collector's host:port (no scheme).
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// protocol selects the OTLP transport. Defaults to grpc.
	// +kubebuilder:validation:Enum=grpc;http/protobuf
	// +optional
	Protocol string `json:"protocol,omitempty"`

	// insecure disables TLS for the OTLP connection.
	// +optional
	Insecure bool `json:"insecure,omitempty"`
}
```

- [ ] **Step 4: Add validation**

In `ValidateVinylCache`, following the accumulate-into-`errs` style:

```go
	// Validate tracing: an enabled tracer without a collector endpoint (or
	// with a malformed one) would crash-loop the sidecar.
	if vc.Spec.Tracing.Enabled {
		ep := vc.Spec.Tracing.OTLP.Endpoint
		if ep == "" {
			errs = append(errs, "tracing.otlp.endpoint is required when tracing is enabled")
		} else if _, _, err := net.SplitHostPort(ep); err != nil {
			errs = append(errs, fmt.Sprintf("tracing.otlp.endpoint %q must be host:port: %v", ep, err))
		}
	}
```

(add `net` to the imports).

- [ ] **Step 5: Regenerate, test, sync the chart CRD**

Run:
```bash
make manifests generate
cp config/crd/bases/vinyl.bluedynamics.eu_vinylcaches.yaml charts/cloud-vinyl/crds/vinylcache.yaml
go test ./internal/webhook/... ./api/... -v
```
Expected: deepcopy grows `TracingSpec`/`OTLPSpec`, CRD yaml gains the
tracing block in both copies, tests PASS. (Check how the chart CRD copy
tracked past CRD changes — `git log --oneline -- charts/cloud-vinyl/crds/` —
and follow that mechanism if it is not a plain copy.)

- [ ] **Step 6: Lint and commit**

```bash
make lint
git add api/ internal/webhook/ config/crd/ charts/cloud-vinyl/crds/
git commit -m "feat(api): spec.tracing for the vinyl-tracer sidecar"
```

---

### Task 8: Operator wiring (`buildTracerContainer`)

**Files:**
- Modify: `internal/controller/statefulset.go` (consts ~line 54; sidecar append at lines 282-285; new builder next to `buildExporterContainer` at ~line 332)
- Test: `internal/controller/statefulset_test.go`

**Interfaces:**
- Consumes: `v1alpha1.TracingSpec` (Task 7); env contract of Task 5.
- Produces: tracer container named `vinyl-tracer` in the pod when
  `spec.tracing.enabled`.

- [ ] **Step 1: Write the failing tests (existing helper style)**

```go
func tracingVC(name string) *v1alpha1.VinylCache {
    vc := minimalVC(name) // reuse whatever base-CR helper statefulset_test.go uses
    vc.Spec.Tracing = v1alpha1.TracingSpec{
        Enabled: true,
        OTLP: v1alpha1.OTLPSpec{
            Endpoint: "collector.monitoring.svc:4317",
            Insecure: true,
        },
    }
    return vc
}

func TestReconcileStatefulSet_TracerSidecarWhenEnabled(t *testing.T) {
    ss := getStatefulSet(t, tracingVC("traced"))
    var tracer *corev1.Container
    for i := range ss.Spec.Template.Spec.Containers {
        if ss.Spec.Template.Spec.Containers[i].Name == "vinyl-tracer" {
            tracer = &ss.Spec.Template.Spec.Containers[i]
        }
    }
    require.NotNil(t, tracer, "vinyl-tracer container missing")

    env := map[string]string{}
    for _, e := range tracer.Env {
        env[e.Name] = e.Value
    }
    assert.Equal(t, "collector.monitoring.svc:4317", env["OTLP_ENDPOINT"])
    assert.Equal(t, "grpc", env["OTLP_PROTOCOL"], "protocol defaults to grpc")
    assert.Equal(t, "true", env["OTLP_INSECURE"])
    assert.Equal(t, "traced", env["TRACER_SERVICE_NAME"], "serviceName defaults to CR name")

    require.Len(t, tracer.VolumeMounts, 1)
    assert.Equal(t, "/var/lib/varnish", tracer.VolumeMounts[0].MountPath)
    assert.True(t, tracer.VolumeMounts[0].ReadOnly)
}

func TestReconcileStatefulSet_NoTracerByDefault(t *testing.T) {
    ss := getStatefulSet(t, minimalVC("plain"))
    for _, c := range ss.Spec.Template.Spec.Containers {
        assert.NotEqual(t, "vinyl-tracer", c.Name)
    }
}

func TestReconcileStatefulSet_TracerImageFromEnv(t *testing.T) {
    t.Setenv("TRACER_IMAGE", "example.org/tracer:test")
    ss := getStatefulSet(t, tracingVC("traced-img"))
    for _, c := range ss.Spec.Template.Spec.Containers {
        if c.Name == "vinyl-tracer" {
            assert.Equal(t, "example.org/tracer:test", c.Image)
            return
        }
    }
    t.Fatal("vinyl-tracer container missing")
}
```

(Use the actual base-CR helper name found in `statefulset_test.go`; if there
is none generic enough, build the CR inline the way
`TestReconcileStatefulSet_ExporterSidecarWhenEnabled` does.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/controller/ -run TestReconcileStatefulSet_Tracer -run TestReconcileStatefulSet_NoTracer -v`
Expected: FAIL (no container built).

- [ ] **Step 3: Implement**

Constants next to `defaultExporterImage`:

```go
	// tracerMetricsPort is the tracer sidecar's Prometheus /metrics port.
	tracerMetricsPort = int32(9464)
	// defaultTracerImage is used when TRACER_IMAGE is unset (Helm sets it).
	defaultTracerImage = "ghcr.io/bluedynamics/cloud-vinyl-tracer:latest"
```

Builder, next to `buildExporterContainer` (defaults live HERE, exporter
convention):

```go
// buildTracerContainer returns the vinyl-tracer sidecar. It shares the
// varnish-workdir volume read-only to read the VSL, and exports OTLP spans
// to the endpoint from spec.tracing.
func buildTracerContainer(vc *v1alpha1.VinylCache) corev1.Container {
	tr := vc.Spec.Tracing
	image := os.Getenv("TRACER_IMAGE")
	if image == "" {
		image = defaultTracerImage
	}
	protocol := tr.OTLP.Protocol
	if protocol == "" {
		protocol = "grpc"
	}
	service := tr.ServiceName
	if service == "" {
		service = vc.Name
	}
	return corev1.Container{
		Name:  "vinyl-tracer",
		Image: image,
		Env: []corev1.EnvVar{
			{Name: "OTLP_ENDPOINT", Value: tr.OTLP.Endpoint},
			{Name: "OTLP_PROTOCOL", Value: protocol},
			{Name: "OTLP_INSECURE", Value: strconv.FormatBool(tr.OTLP.Insecure)},
			{Name: "TRACER_SERVICE_NAME", Value: service},
		},
		Ports: []corev1.ContainerPort{
			{Name: "tracer-metrics", ContainerPort: tracerMetricsPort, Protocol: corev1.ProtocolTCP},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: volumeVarnishWorkdir, MountPath: "/var/lib/varnish", ReadOnly: true},
		},
		SecurityContext: &corev1.SecurityContext{
			RunAsNonRoot:             new(true),
			ReadOnlyRootFilesystem:   new(true),
			AllowPrivilegeEscalation: new(false),
		},
		Resources: tr.Resources,
	}
}
```

Append point (statefulset.go:282-285 block):

```go
		if vc.Spec.Tracing.Enabled {
			containers = append(containers, buildTracerContainer(vc))
		}
```

No NetworkPolicy change: the repo's policies are all ingress-only, so pod
egress (tracer → collector) is unrestricted today. Clusters with their own
default-deny egress must allow 4317/4318 from the varnish pods; that goes in
the how-to page (later plan), not in code.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/controller/ -v`
Expected: all PASS, including the pre-existing statefulset tests.

- [ ] **Step 5: Lint and commit**

```bash
make lint
git add internal/controller
git commit -m "feat(operator): wire vinyl-tracer sidecar from spec.tracing"
```

---

### Task 9: Helm chart wiring

**Files:**
- Modify: `charts/cloud-vinyl/values.yaml` (image block)
- Modify: `charts/cloud-vinyl/values.schema.json` (image block: `additionalProperties: false` — MUST add tracer or installs fail)
- Modify: `charts/cloud-vinyl/templates/deployment.yaml` (manager env, after AGENT_IMAGE)
- Test: `charts/cloud-vinyl/tests/deployment_test.yaml`

- [ ] **Step 1: Add the failing helm-unittest case**

In `deployment_test.yaml`, following the existing suite format:

```yaml
  - it: sets TRACER_IMAGE from image.tracer values
    set:
      image.tracer.repository: example.org/tracer
      image.tracer.tag: 1.2.3
    asserts:
      - contains:
          path: spec.template.spec.containers[0].env
          content:
            name: TRACER_IMAGE
            value: "example.org/tracer:1.2.3"
```

Run: `helm unittest charts/cloud-vinyl/` (install the plugin if absent:
`helm plugin install https://github.com/helm-unittest/helm-unittest --version v0.7.2`).
Expected: FAIL.

- [ ] **Step 2: values.yaml**

```yaml
  tracer:
    repository: ghcr.io/bluedynamics/cloud-vinyl-tracer
    # Defaults to Chart appVersion when empty.
    tag: ""
    pullPolicy: IfNotPresent
```

(inside the existing `image:` block, after `agent:`).

- [ ] **Step 3: values.schema.json**

Mirror the `agent` object under `image.properties` as `tracer` (same
`repository`/`tag`/`pullPolicy` properties, `additionalProperties: false`)
and add `"tracer"` to the `image.required` array — copy the agent stanza
verbatim and rename.

- [ ] **Step 4: deployment.yaml**

After the `AGENT_IMAGE` env entry:

```yaml
              - name: TRACER_IMAGE
                value: "{{ .Values.image.tracer.repository }}:{{ .Values.image.tracer.tag | default .Chart.AppVersion }}"
```

- [ ] **Step 5: Run chart tests + a lint install**

Run:
```bash
helm unittest charts/cloud-vinyl/
helm lint charts/cloud-vinyl/
helm template charts/cloud-vinyl/ >/dev/null
```
Expected: all pass; the new test case green.

- [ ] **Step 6: Commit**

```bash
git add charts/cloud-vinyl
git commit -m "feat(chart): tracer image wiring"
```

---

### Task 10: vinylprobe OTLP sink + span assertions

**Files:**
- Create: `internal/probe/otlp.go`
- Test: `internal/probe/otlp_test.go`
- Modify: `cmd/vinylprobe/main.go` (flags ~line 58-92, validate ~line 97, dispatch ~line 130-152, help consts ~line 22)
- Test: `cmd/vinylprobe/main_test.go`

**Interfaces:**
- Produces (used by the E2E test in Task 11):
  - `vinylprobe -otlp-sink :4318` — serves POST `/v1/traces` (OTLP/http-protobuf), GET `/spans` (JSON `[{"name":..., "traceID":..., "spanID":..., "parentID":..., "attrs":{...}}]`), GET `/healthz` (200). Runs until killed.
  - `vinylprobe -assert-spans -sink http://otlp-sink:4318 -span-name "varnish request" -min-count 1 -attr varnish.handling=hit -within 60s` — polls `/spans`, exit 0 when satisfied, exit 1 on timeout, exit 2 on usage/transport errors.
- New imports for `cmd/vinylprobe` (via `internal/probe`):
  `go.opentelemetry.io/proto/otlp/...` + `google.golang.org/protobuf` — both
  free of `k8s.io`, so the boundary holds; Step 6 proves it.

- [ ] **Step 1: Write failing sink tests**

The test posts a REAL `ExportTraceServiceRequest` marshaled with the same
proto types the tracer's OTLP exporter sends — counterpart-true by
construction:

```go
package probe

import (
    "bytes"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
    "google.golang.org/protobuf/proto"

    collpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
    commonpb "go.opentelemetry.io/proto/otlp/common/v1"
    tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

func exportReq(name, attrKey, attrVal string) []byte {
    req := &collpb.ExportTraceServiceRequest{
        ResourceSpans: []*tracepb.ResourceSpans{{
            ScopeSpans: []*tracepb.ScopeSpans{{
                Spans: []*tracepb.Span{{
                    Name:    name,
                    TraceId: bytes.Repeat([]byte{0xaa}, 16),
                    SpanId:  bytes.Repeat([]byte{0xbb}, 8),
                    Attributes: []*commonpb.KeyValue{{
                        Key: attrKey,
                        Value: &commonpb.AnyValue{
                            Value: &commonpb.AnyValue_StringValue{StringValue: attrVal},
                        },
                    }},
                }},
            }},
        }},
    }
    b, _ := proto.Marshal(req)
    return b
}

func TestOTLPSink_ReceivesAndListsSpans(t *testing.T) {
    sink := NewOTLPSink(128)
    srv := httptest.NewServer(sink.Handler())
    defer srv.Close()

    resp, err := http.Post(srv.URL+"/v1/traces", "application/x-protobuf",
        bytes.NewReader(exportReq("varnish request", "varnish.handling", "hit")))
    require.NoError(t, err)
    assert.Equal(t, http.StatusOK, resp.StatusCode)

    list, err := http.Get(srv.URL + "/spans")
    require.NoError(t, err)
    var got []SpanSummary
    require.NoError(t, json.NewDecoder(list.Body).Decode(&got))
    require.Len(t, got, 1)
    assert.Equal(t, "varnish request", got[0].Name)
    assert.Equal(t, "hit", got[0].Attrs["varnish.handling"])
    assert.NotEmpty(t, got[0].TraceID)
}
```

- [ ] **Step 2: Run to verify failure, then implement `internal/probe/otlp.go`**

Run: `go test ./internal/probe/ -run TestOTLPSink -v` → FAIL, then:

```go
// Package probe: OTLP sink + span assertions for E2E. HTTP only — this
// package is imported by cmd/vinylprobe, which must not pull in k8s.io.
package probe

import (
    "encoding/hex"
    "encoding/json"
    "io"
    "net/http"
    "sync"

    "google.golang.org/protobuf/proto"

    collpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
)

type SpanSummary struct {
    Name     string            `json:"name"`
    TraceID  string            `json:"traceID"`
    SpanID   string            `json:"spanID"`
    ParentID string            `json:"parentID"`
    Attrs    map[string]string `json:"attrs"`
}

// OTLPSink is an in-memory OTLP/http-protobuf trace receiver with a ring
// buffer of the most recent spans.
type OTLPSink struct {
    mu    sync.Mutex
    max   int
    spans []SpanSummary
}

func NewOTLPSink(max int) *OTLPSink { return &OTLPSink{max: max} }

func (s *OTLPSink) Handler() http.Handler {
    mux := http.NewServeMux()
    mux.HandleFunc("POST /v1/traces", s.receive)
    mux.HandleFunc("GET /spans", s.list)
    mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
        w.WriteHeader(http.StatusOK)
    })
    return mux
}

func (s *OTLPSink) receive(w http.ResponseWriter, r *http.Request) {
    body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
    if err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }
    var req collpb.ExportTraceServiceRequest
    if err := proto.Unmarshal(body, &req); err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }
    s.mu.Lock()
    defer s.mu.Unlock()
    for _, rs := range req.GetResourceSpans() {
        for _, ss := range rs.GetScopeSpans() {
            for _, sp := range ss.GetSpans() {
                attrs := map[string]string{}
                for _, kv := range sp.GetAttributes() {
                    attrs[kv.GetKey()] = kv.GetValue().GetStringValue()
                    if attrs[kv.GetKey()] == "" {
                        attrs[kv.GetKey()] = kv.GetValue().String()
                    }
                }
                s.spans = append(s.spans, SpanSummary{
                    Name:     sp.GetName(),
                    TraceID:  hex.EncodeToString(sp.GetTraceId()),
                    SpanID:   hex.EncodeToString(sp.GetSpanId()),
                    ParentID: hex.EncodeToString(sp.GetParentSpanId()),
                    Attrs:    attrs,
                })
            }
        }
    }
    if over := len(s.spans) - s.max; over > 0 {
        s.spans = s.spans[over:]
    }
    w.WriteHeader(http.StatusOK)
}

func (s *OTLPSink) list(w http.ResponseWriter, _ *http.Request) {
    s.mu.Lock()
    defer s.mu.Unlock()
    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(s.spans)
}
```

Run the sink test again → PASS.

- [ ] **Step 3: Write the failing decision-function test (`cmd/vinylprobe`)**

Follow the `decidePurge` pattern — pure verdict function, unit-tested
without HTTP:

```go
func TestDecideSpans_SatisfiedPasses(t *testing.T) {
    spans := []probe.SpanSummary{
        {Name: "varnish request", Attrs: map[string]string{"varnish.handling": "hit"}},
        {Name: "varnish request", Attrs: map[string]string{"varnish.handling": "miss"}},
    }
    v := decideSpans(spans, "varnish request", map[string]string{"varnish.handling": "hit"}, 1)
    if !v.satisfied {
        t.Fatalf("want satisfied, got %+v", v)
    }
}

func TestDecideSpans_CountShortfallNotSatisfied(t *testing.T) {
    spans := []probe.SpanSummary{{Name: "varnish request", Attrs: map[string]string{}}}
    v := decideSpans(spans, "varnish request", nil, 2)
    if v.satisfied {
        t.Fatal("one span must not satisfy min-count 2")
    }
}

func TestDecideSpans_AttrMismatchNotCounted(t *testing.T) {
    spans := []probe.SpanSummary{
        {Name: "varnish request", Attrs: map[string]string{"varnish.handling": "miss"}},
    }
    v := decideSpans(spans, "varnish request", map[string]string{"varnish.handling": "hit"}, 1)
    if v.satisfied {
        t.Fatal("attr mismatch must not count")
    }
}
```

- [ ] **Step 4: Implement the vinylprobe modes**

New flags in `parseFlags` (long help strings into the existing `const`
block per the lll convention): `-otlp-sink` (string listen addr),
`-assert-spans` (bool), `-sink` (string URL), `-span-name` (string),
`-min-count` (int, default 1), `-attr` (string `key=value`, may repeat via
`flag.Func` appending to a slice), `-within` (duration, default 60s).
Extend `validate()`: `-otlp-sink` and `-assert-spans` are mutually exclusive
with each other and with `-url`/`-purge`/`-seed`/`-check`; `-assert-spans`
requires `-sink` and `-span-name`.

Dispatch (two new cases before the existing ones):

```go
	case f.otlpSink != "":
		runOTLPSink(f)
	case f.assertSpans:
		runAssertSpans(ctx, f)
```

```go
type spanVerdict struct{ satisfied bool; matched int }

// decideSpans is pure so the pass/fail rule is unit-testable (decidePurge
// pattern): a span counts when its name matches and every wanted attr equals.
func decideSpans(spans []probe.SpanSummary, name string, attrs map[string]string, minCount int) spanVerdict {
	matched := 0
	for _, s := range spans {
		if s.Name != name {
			continue
		}
		ok := true
		for k, want := range attrs {
			if s.Attrs[k] != want {
				ok = false
				break
			}
		}
		if ok {
			matched++
		}
	}
	return spanVerdict{satisfied: matched >= minCount, matched: matched}
}

func runOTLPSink(f probeFlags) {
	sink := probe.NewOTLPSink(4096)
	srv := &http.Server{Addr: f.otlpSink, Handler: sink.Handler(),
		ReadHeaderTimeout: 5 * time.Second}
	fmt.Printf("OK: otlp sink listening on %s\n", f.otlpSink)
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "sink: %v\n", err)
		os.Exit(2)
	}
}

func runAssertSpans(ctx context.Context, f probeFlags) {
	deadline := time.Now().Add(f.within)
	var last spanVerdict
	for time.Now().Before(deadline) {
		spans, err := fetchSpans(ctx, f.sink) // GET <sink>/spans, decode JSON
		if err == nil {
			last = decideSpans(spans, f.spanName, f.attrs, f.minCount)
			if last.satisfied {
				fmt.Printf("OK: %d span(s) named %q matched\n", last.matched, f.spanName)
				os.Exit(0)
			}
		}
		time.Sleep(2 * time.Second)
	}
	fmt.Printf("FAIL: only %d span(s) named %q matched within %s\n",
		last.matched, f.spanName, f.within)
	os.Exit(1)
}
```

(`fetchSpans` is ~15 lines of `http.NewRequestWithContext` + JSON decode into
`[]probe.SpanSummary`; put it next to `runAssertSpans`.)

- [ ] **Step 5: Run all probe tests**

Run: `go test ./cmd/vinylprobe/ ./internal/probe/ -v`
Expected: PASS.

- [ ] **Step 6: Prove the boundary still holds**

Run: `bash hack/check-e2e-boundary.sh`
Expected: `OK: E2E layer boundary intact`. If it fails on k8s.io: a
transitive dep of the otlp proto module leaked; find it with
`go mod why -m k8s.io/api` and re-plan the import (this must not be waved
through).

- [ ] **Step 7: Lint and commit**

```bash
make lint
git add internal/probe cmd/vinylprobe go.mod go.sum
git commit -m "feat(probe): otlp sink and span assertion modes"
```

---

### Task 11: E2E test (fast suite)

**Files:**
- Create: `e2e/fixtures/probe/otlp-sink.yaml`
- Create: `e2e/fixtures/vinylcaches/tracing.yaml`
- Create: `e2e/tests/tracing/chainsaw-test.yaml`
- Modify: `.github/workflows/e2e-chainsaw.yml` (image build/load steps)
- Modify: `e2e/setup/install-operator.sh` (TRACER_IMAGE)

- [ ] **Step 1: Sink fixture**

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: otlp-sink
  labels:
    app: otlp-sink
spec:
  restartPolicy: Never
  containers:
    - name: sink
      image: cloud-vinyl-probe:dev
      imagePullPolicy: Never
      args: ["-otlp-sink", ":4318"]
---
apiVersion: v1
kind: Service
metadata:
  name: otlp-sink
spec:
  selector:
    app: otlp-sink
  ports:
    - port: 4318
      targetPort: 4318
```

(`Dockerfile.probe` sets `ENTRYPOINT ["/vinylprobe"]`, so bare `args:` are
correct here; `probe-pod.yaml` overrides with `command: ["sleep","3600"]`
because that pod is exec-driven instead.)

- [ ] **Step 2: VinylCache fixture**

Copy `e2e/fixtures/vinylcaches/minimal.yaml` as `tracing.yaml`, rename the
CR `traced-cache`, and add (service names resolve inside the test
namespace, so no namespace templating is needed):

```yaml
  tracing:
    enabled: true
    otlp:
      endpoint: otlp-sink:4318
      protocol: http/protobuf
      insecure: true
```

- [ ] **Step 3: The chainsaw test**

`e2e/tests/tracing/chainsaw-test.yaml`, modeled on
`cache-and-invalidate/chainsaw-test.yaml` (same apiVersion, timeouts,
description prose style; NO `curl`/`wget` substrings anywhere, prose
included):

```yaml
apiVersion: chainsaw.kyverno.io/v1alpha1
kind: Test
metadata:
  name: tracing
  labels:
    suite: fast
spec:
  description: |
    Proves the vinyl-tracer sidecar exports OTLP spans for real traffic:
    a miss produces a "varnish request" span (varnish.handling=miss) and a
    "varnish fetch" child; a repeat request produces a hit span. The sink is
    vinylprobe in -otlp-sink mode; assertions poll it via -assert-spans, so
    no sleeps are needed.
    SUBTLETY: spans arrive batched (~3s flush), hence -within 60.
    NOT COVERED (later plans): trace-context rewrite toward the backend
    (P2), coalescing/grace/ESI truth (P3).
  timeouts:
    assert: 180s
    delete: 60s
    exec: 120s
  steps:
    - name: deploy-backend
      try:
        - apply:
            file: ../../fixtures/backends/echo-service.yaml
        - assert:
            resource:
              apiVersion: apps/v1
              kind: Deployment
              metadata:
                name: echo-backend
              status:
                readyReplicas: 1
    - name: start-sink-and-probe
      try:
        - apply:
            file: ../../fixtures/probe/otlp-sink.yaml
        - apply:
            file: ../../fixtures/probe/probe-pod.yaml
        - assert:
            resource:
              apiVersion: v1
              kind: Pod
              metadata:
                name: otlp-sink
              status:
                phase: Running
        - assert:
            resource:
              apiVersion: v1
              kind: Pod
              metadata:
                name: probe
              status:
                phase: Running
    - name: create-cache
      try:
        - apply:
            file: ../../fixtures/vinylcaches/tracing.yaml
        - assert:
            resource:
              apiVersion: apps/v1
              kind: StatefulSet
              metadata:
                name: traced-cache
              status:
                readyReplicas: 1
    - name: traffic-miss-then-hit
      try:
        - script:
            content: |
              set -eu
              kubectl exec -n $NAMESPACE probe -- \
                /vinylprobe -url "http://traced-cache:8080/traced/a" -expect miss
              kubectl exec -n $NAMESPACE probe -- \
                /vinylprobe -url "http://traced-cache:8080/traced/a" -expect hit
    - name: assert-spans
      try:
        - script:
            content: |
              set -eu
              kubectl exec -n $NAMESPACE probe -- \
                /vinylprobe -assert-spans -sink "http://otlp-sink:4318" \
                  -span-name "varnish request" -attr varnish.handling=miss \
                  -min-count 1 -within 90s
              kubectl exec -n $NAMESPACE probe -- \
                /vinylprobe -assert-spans -sink "http://otlp-sink:4318" \
                  -span-name "varnish fetch" -min-count 1 -within 30s
              kubectl exec -n $NAMESPACE probe -- \
                /vinylprobe -assert-spans -sink "http://otlp-sink:4318" \
                  -span-name "varnish request" -attr varnish.handling=hit \
                  -min-count 1 -within 90s
```

Adapt the service-name details to how `minimal.yaml`'s service is actually
named (read `minimal.yaml` and the basic-lifecycle test; `traced-cache`
above assumes the service equals the CR name, as `my-cache` does, and that
`minimal.yaml`'s backend points at `echo-backend`; if minimal.yaml
references a different backend, keep ITS backend fixture in
`deploy-backend` instead).

- [ ] **Step 4: Workflow + operator install wiring**

In `.github/workflows/e2e-chainsaw.yml`, next to the agent image step:

```yaml
      - name: Build tracer image
        run: |
          docker build -f Dockerfile.tracer \
            --build-arg VARNISH_IMAGE=varnish:8.0.2 \
            -t ghcr.io/bluedynamics/cloud-vinyl-tracer:dev .
          kind load docker-image ghcr.io/bluedynamics/cloud-vinyl-tracer:dev \
            --name cloud-vinyl-e2e
```

In `e2e/setup/install-operator.sh`, alongside the AGENT_IMAGE handling:
accept `TRACER_IMAGE` env and pass
`--set image.tracer.repository="${TRACER_IMAGE%:*}" --set "image.tracer.tag=${TRACER_IMAGE##*:}"`;
set `TRACER_IMAGE=ghcr.io/bluedynamics/cloud-vinyl-tracer:dev` in the
workflow's install step, mirroring how AGENT_IMAGE reaches the script.

- [ ] **Step 5: Label check + local run**

Run:
```bash
bash hack/check-suite-labels.sh
bash hack/check-e2e-boundary.sh
```
Expected: both OK. Then, if a local kind cluster per `e2e/setup/` is
feasible in the environment, run the single test:
`chainsaw test --test-dir e2e/tests/tracing`; otherwise rely on CI and say
so explicitly in the task report — never claim the E2E passed without
having seen it pass.

- [ ] **Step 6: Commit**

```bash
git add e2e/ .github/workflows/e2e-chainsaw.yml
git commit -m "test(e2e): tracing fast-suite test with otlp sink"
```

---

### Task 12: Release wiring

**Files:**
- Modify: `.github/workflows/release.yml` (docker-build matrix)

The tracer ships as a container only (the bare binary is useless without a
matched varnishlog), so `.goreleaser.yaml` stays untouched. Add two matrix
entries (amd64 on `ubuntu-latest`, arm64 on `ubuntu-24.04-arm`) mirroring the
agent's, with `file: Dockerfile.tracer`, image
`ghcr.io/bluedynamics/cloud-vinyl-tracer`, and
`build-args: VARNISH_IMAGE=varnish:8.0.2`, plus the tracer image in the
multi-arch manifest job. Copy the agent entries verbatim and adjust those
three fields; keep whatever tag-exists guard pattern the file uses.

- [ ] **Step 1: Edit the matrix + manifest job as above**
- [ ] **Step 2: Validate the workflow file**

Run: `gh workflow view release --repo bluedynamics/cloud-vinyl >/dev/null 2>&1 || true` — syntax is only truly validated by GitHub; at minimum
`python3 -c "import yaml,sys; yaml.safe_load(open('.github/workflows/release.yml'))"`.
Expected: parses.

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/release.yml
git commit -m "ci: release the tracer image"
```

---

### Task 13: Full verification pass

- [ ] **Step 1: The whole battery**

```bash
make manifests generate && git diff --exit-code   # nothing ungenerated
make lint
make test
make test-int
helm unittest charts/cloud-vinyl/
bash hack/check-suite-labels.sh
bash hack/check-e2e-boundary.sh
make docker-build-tracer
```
Expected: every command exits 0. `git diff --exit-code` proves generated
files are committed.

- [ ] **Step 2: Fix anything red, re-run until all green, commit fixes**

- [ ] **Step 3: Report** — per verification-before-completion: paste the
  actual command outputs (tails) in the report; no green claims without
  evidence. E2E status: state explicitly whether the tracing chainsaw test
  ran locally or is pending CI.
