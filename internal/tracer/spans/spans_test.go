package spans

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
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

// spansNamed filters a span slice by Name, preserving order.
func spansNamed(spans []Span, name string) []Span {
	var out []Span
	for _, s := range spans {
		if s.Name == name {
			out = append(out, s)
		}
	}
	return out
}

// attrHas reports whether attrs carries a key at all, regardless of value.
func attrHas(attrs []attribute.KeyValue, key string) bool {
	for _, a := range attrs {
		if string(a.Key) == key {
			return true
		}
	}
	return false
}

// TestBuild_RetryEmitsOneFetchSpanPerAttempt: retry.txt's NOTES.md entry
// records the retried bereq (32769) nested UNDER the failed attempt (3) at
// "***" depth, not as a sibling "**" BeReq — "The retried bereq sits nested
// under the first bereq (***, one level deeper than **) ... it is not a
// sibling BeReq group at the same ** depth as attempt 1." Build must walk
// into a BeReq's own Children to find the next attempt.
func TestBuild_RetryEmitsOneFetchSpanPerAttempt(t *testing.T) {
	txs := fixtureTxs(t, "retry.txt")
	got := Build(txs[0], &seqIDs{})
	fetches := spansNamed(got, "varnish fetch")
	require.Len(t, fetches, 2, "retry: two backend attempts, two fetch spans")
	assert.Equal(t, "500", attrString(t, fetches[0].Attrs, "http.response.status_code"))
	assert.Equal(t, "200", attrString(t, fetches[1].Attrs, "http.response.status_code"))
	assert.False(t, attrHas(fetches[0].Attrs, "varnish.retry"), "first attempt carries no retry ordinal")
	assert.Equal(t, "1", attrString(t, fetches[1].Attrs, "varnish.retry"))
	req := spansNamed(got, "varnish request")[0]
	for _, f := range fetches {
		assert.Equal(t, req.TraceID, f.TraceID)
	}
	// NOTES.md: "the trace id stays 677cf25c... across both attempts while
	// the span id changes per attempt" — the retried attempt's fetch span
	// must adopt its OWN freshly minted span id, distinct from attempt 1's.
	assert.NotEqual(t, fetches[0].SpanID, fetches[1].SpanID)
	// Retry nesting per the interface contract: "nested BeReq under a BeReq
	// (retry nesting) -> fetch span parented to the outer FETCH span" — not
	// to the top request span.
	assert.Equal(t, fetches[0].SpanID, fetches[1].ParentID,
		"retried attempt is a grandchild of the request, child of attempt 1's fetch span")
}

// TestBuild_ESISubrequestBecomesChildSpan: esi.txt's NOTES.md entry records
// the ESI child Request group (vxid 4) nested at the SAME "**" depth as its
// parent's BeReq (vxid 3) — both direct children of Request 2 in the parsed
// tree — and its own fetch (BeReq 5) one level deeper again.
func TestBuild_ESISubrequestBecomesChildSpan(t *testing.T) {
	txs := fixtureTxs(t, "esi.txt")
	got := Build(txs[0], &seqIDs{})
	reqs := spansNamed(got, "varnish request")
	require.GreaterOrEqual(t, len(reqs), 2, "parent + at least one ESI child")
	child := reqs[1]
	assert.Equal(t, "true", attrString(t, child.Attrs, "varnish.esi"))
	assert.Equal(t, reqs[0].SpanID, child.ParentID)
	assert.Equal(t, reqs[0].TraceID, child.TraceID)

	// Controller ruling: "ESI subrequests nest as child Request groups but
	// their bereq traceparent self-roots with an UNRELATED trace id (no
	// header propagation into ESI subreqs) ... every descendant span uses
	// the TOP span's trace id, with varnish.minted_trace_mismatch=true
	// where the minted trace id differs (this will be TRUE for ESI
	// fragments — assert it)." NOTES.md confirms bereq 5's minted trace id
	// (0f219f33...) differs entirely from bereq 3's (62f872aa...), which
	// the top request span adopts as its own trace id.
	fetches := spansNamed(got, "varnish fetch")
	require.Len(t, fetches, 2, "the /esi fetch and the /frag ESI fragment's own fetch")
	esiFragmentFetch := fetches[1]
	assert.Equal(t, reqs[0].TraceID, esiFragmentFetch.TraceID,
		"the ESI fragment's fetch span still emits under the group's (top's) trace id")
	assert.Equal(t, "true", attrString(t, esiFragmentFetch.Attrs, "varnish.minted_trace_mismatch"))
	assert.False(t, attrHas(fetches[0].Attrs, "varnish.minted_trace_mismatch"),
		"the /esi fetch's own minted trace id IS what the top adopted, so no mismatch")
}

// TestBuild_RestartCountFromOwnRecords replaces the brief's
// TestBuild_RestartChildAndCount per the controller ruling: Task 2
// established that restart.txt's Request 4 (the restarted request) is a
// SEPARATE top-level Tx (txs[1]) — linked to txs[0] only via the Records-
// level "Link req 4 restart", never a parsed-tree Child — so it is not
// reachable from Build(txs[0], ...) at all. Cross-tx stitching (giving the
// TOP span the post-restart delivered status) is the Linker's job.
//
// What THIS Tx's own records genuinely show, per NOTES.md's restart.txt
// section: no "Resp" timestamp on Request 2 at all (VCL_return was
// "restart", not "deliver" — delivery never happened on this Tx), only a
// "Timestamp Restart:" record closes the group, and the RespStatus record
// logged during the aborted vcl_deliver attempt (418) is the only status
// this Tx's own records carry — coincidentally the same 418 the client
// ultimately sees (NOTES.md: "the final response delivered to the client
// is the same 418 both times"), but for the OWN-RECORDS reason, not because
// Build reached across the restart.
func TestBuild_RestartCountFromOwnRecords(t *testing.T) {
	txs := fixtureTxs(t, "restart.txt")
	require.Len(t, txs, 2, "original and restarted request are two top-level Tx's")
	got := Build(txs[0], &seqIDs{})
	require.NotEmpty(t, got, "a Request tx with no Resp timestamp must still close via Restart")
	top := got[0]
	assert.Equal(t, "1", attrString(t, top.Attrs, "varnish.restarts"),
		"one Link req <vxid> restart record in this tx's own Records")
	assert.Equal(t, "418", attrString(t, top.Attrs, "http.response.status_code"),
		"this tx's own last-logged RespStatus, from the aborted pre-restart deliver attempt")
}

// TestBuild_GraceHitEmitsBgfetchSpan
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
