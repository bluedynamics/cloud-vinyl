// Package spans turns parsed VSL transaction groups into OTel-shaped spans
// with explicit ids and real VSL timestamps: request + fetch spans, parented
// from the incoming traceparent. When the VCL snippet has minted a fetch
// span id onto bereq's traceparent (P2), that id is adopted so the backend
// parents its own spans onto the fetch span rather than landing as a
// sibling; without it (a P1-only deployment), the fetch span still mints
// its own id.
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

// Span is one OTel-shaped span built from a VSL transaction (or child
// transaction), with explicit ids and timestamps taken from the VSL record.
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

// IDSource mints trace and span ids for spans that Build constructs. Tests
// supply a deterministic fake; production uses NewRandomIDs.
type IDSource interface {
	TraceID() trace.TraceID
	SpanID() trace.SpanID
}

type randomIDs struct{}

// NewRandomIDs returns an IDSource that mints cryptographically random trace
// and span ids, suitable for self-rooted spans in production.
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
	end, okEnd := respEnd(tx)
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
	// P2/P3: the VCL snippet mints the fetch span id (and, for a self-root,
	// the trace id) onto a bereq's own traceparent in vcl_backend_fetch —
	// once per BACKEND_FETCH call, so every attempt/bgfetch/ESI-fragment
	// bereq anywhere in the descendant tree mints its own. Collect all of
	// them, not just direct children of tx, so retry attempts (nested under
	// the failed attempt's BeReq, not under the Request) and ESI fragments
	// (nested under the ESI child Request) are found too. A BereqHeader
	// whose span id merely equals the incoming parent id is an unrewritten
	// P1-style forward — not a minted id — and is ignored.
	//
	// The top request's trace id still comes from incoming-or-first-minted
	// (depth-first over the tree: a direct BeReq wins over one nested
	// deeper, e.g. under an ESI child). Resolved per the P3 interface
	// contract: every descendant span — however many levels of retry/ESI/
	// bgfetch deep — emits under THAT ONE trace id, never its own minted
	// one. A minted trace id that differs from the adopted one (self-root
	// bgfetch or ESI fragment minting fresh, since Varnish does not
	// propagate req.http.traceparent into bgfetch or ESI subrequests) still
	// gets its bereq's own minted SPAN id adopted, but is flagged
	// varnish.minted_trace_mismatch=true on that fetch span so the
	// backend-side orphan is at least visible to a Linker/backend correlating
	// by trace id.
	mintedSpanIDs := make(map[*vsl.Tx]trace.SpanID)
	mintedTraceIDs := make(map[*vsl.Tx]trace.TraceID)
	var mintedTraceID trace.TraceID
	var collectMinted func(node *vsl.Tx)
	collectMinted = func(node *vsl.Tx) {
		for _, child := range node.Children {
			if child.Type == "BeReq" {
				if raw, ok := child.Header("BereqHeader", "traceparent"); ok {
					if tid, sid, _, valid := parseTraceparent(raw); valid && sid != parentID {
						mintedSpanIDs[child] = sid
						mintedTraceIDs[child] = tid
						if !mintedTraceID.IsValid() {
							mintedTraceID = tid
						}
					}
				}
			}
			collectMinted(child)
		}
	}
	collectMinted(tx)

	if !traceID.IsValid() {
		if mintedTraceID.IsValid() {
			traceID = mintedTraceID // join the VCL-minted trace
		} else {
			traceID = ids.TraceID() // self-rooted: Varnish is the edge
		}
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
	if n := countRestarts(tx); n > 0 {
		// Own-records only: a "Link req <vxid> restart" record here means
		// THIS tx's vcl_deliver decided to restart. The restarted request
		// is a separate top-level Tx (see respEnd's doc), not a Child of
		// this one — stitching the two into one logical span tree is the
		// Linker's job (P3 Task-3 controller ruling), not Build's.
		req.Attrs = append(req.Attrs, attribute.Int("varnish.restarts", n))
	}
	out := []Span{req}
	out = buildChildren(tx, req.SpanID, traceID, 0, mintedSpanIDs, mintedTraceIDs, ids, out)
	return out
}

// buildChildren recursively appends spans for tx's BeReq and ESI Request
// children onto out, parenting each new span on parentSpanID — the span
// that structurally owns it in the parsed tree:
//   - a direct BeReq child of a Request (fetch, bgfetch, or an ESI child's
//     own fetch) parents onto that Request's span;
//   - a retried BeReq nested UNDER another BeReq parents onto the OUTER
//     BeReq's fetch span, not the request span — NOTES.md's retry.txt
//     section: "The retried bereq sits nested under the first bereq (***,
//     one level deeper than **) ... not a sibling BeReq group";
//   - an ESI child Request (Begin payload containing "esi") parents onto
//     the triggering request's span, per NOTES.md's esi.txt section, and is
//     itself walked recursively for its own fetch (and, in principle,
//     further-nested ESI fragments).
//
// retryDepth counts BeReq-under-BeReq nesting: 0 for a first attempt (no
// varnish.retry attribute), 1 for the first retry, 2 for a second, etc. —
// the interface contract's "varnish.retry=<n> for n>=1".
func buildChildren(
	tx *vsl.Tx,
	parentSpanID trace.SpanID,
	traceID trace.TraceID,
	retryDepth int,
	mintedSpanIDs map[*vsl.Tx]trace.SpanID,
	mintedTraceIDs map[*vsl.Tx]trace.TraceID,
	ids IDSource,
	out []Span,
) []Span {
	for _, child := range tx.Children {
		switch child.Type {
		case "BeReq":
			fs, okF := child.Timestamp("Bereq")
			fe, okE := child.Timestamp("BerespBody")
			if !okE {
				fe, okE = child.Timestamp("Beresp")
			}
			if !okF || !okE {
				continue
			}
			spanID, minted := mintedSpanIDs[child]
			if !minted {
				spanID = ids.SpanID()
			}
			attrs := fetchAttrs(child)
			if retryDepth > 0 {
				attrs = append(attrs, attribute.Int("varnish.retry", retryDepth))
			}
			if minted {
				if mtid := mintedTraceIDs[child]; mtid.IsValid() && mtid != traceID {
					attrs = append(attrs, attribute.Bool("varnish.minted_trace_mismatch", true))
				}
			}
			out = append(out, Span{
				TraceID:  traceID,
				SpanID:   spanID,
				ParentID: parentSpanID,
				Name:     "varnish fetch",
				Kind:     trace.SpanKindClient,
				Start:    fs,
				End:      fe,
				Attrs:    attrs,
			})
			// Retry nesting: a BeReq nested under this BeReq is the next
			// attempt, parented onto THIS fetch span, one ordinal deeper.
			out = buildChildren(child, spanID, traceID, retryDepth+1, mintedSpanIDs, mintedTraceIDs, ids, out)
		case "Request":
			b, _ := child.First("Begin")
			if !strings.Contains(b, "esi") {
				continue // not an ESI subrequest shape this task recognizes
			}
			cs, okS := child.Timestamp("Start")
			ce, okE := respEnd(child)
			if !okS || !okE {
				continue
			}
			attrs := requestAttrs(child)
			attrs = append(attrs, attribute.Bool("varnish.esi", true))
			esiSpanID := ids.SpanID()
			out = append(out, Span{
				TraceID:  traceID,
				SpanID:   esiSpanID,
				ParentID: parentSpanID,
				Name:     "varnish request",
				Kind:     trace.SpanKindServer,
				Start:    cs,
				End:      ce,
				Attrs:    attrs,
			})
			// ESI fragments recurse: the fragment's own fetch (and, if
			// Varnish ever nests ESI within ESI, further child requests)
			// parents onto the ESI span just created, retry depth reset.
			out = buildChildren(child, esiSpanID, traceID, 0, mintedSpanIDs, mintedTraceIDs, ids, out)
		}
	}
	return out
}

// respEnd returns the timestamp that closes a Request tx: ordinarily
// "Resp" (the response was actually delivered), but a restarted request's
// original Tx never reaches vcl_deliver's Resp timestamp — its VCL_return
// was "restart", not "deliver" — so NOTES.md's restart.txt section records
// "Timestamp Restart:" as the only record that closes that group. Fall back
// to it so such a Tx still produces a span instead of Build silently
// dropping it.
func respEnd(tx *vsl.Tx) (time.Time, bool) {
	if end, ok := tx.Timestamp("Resp"); ok {
		return end, true
	}
	return tx.Timestamp("Restart")
}

// countRestarts counts this tx's own "Link req <vxid> restart" records
// (NOTES.md's restart.txt section: reason "restart", child type "req", no
// ESI-style trailing sub-level field). Each one names a SEPARATE top-level
// Tx (the restarted request) that this function does not and cannot follow
// — Build only ever sees one top-level Tx at a time — so this counts
// restarts visible from here without resolving where they lead.
func countRestarts(tx *vsl.Tx) int {
	n := 0
	for _, r := range tx.Records {
		if r.Tag != "Link" {
			continue
		}
		f := strings.Fields(r.Payload)
		if len(f) >= 3 && f[0] == "req" && f[2] == "restart" {
			n++
		}
	}
	return n
}

func requestAttrs(tx *vsl.Tx) []attribute.KeyValue {
	var attrs []attribute.KeyValue
	if m, ok := tx.First("ReqMethod"); ok {
		attrs = append(attrs, attribute.String("http.request.method", m))
	}
	if u, ok := tx.First("ReqURL"); ok {
		attrs = append(attrs, attribute.String("url.path", u))
	}
	// Last, not First: a tx can log RespStatus more than once (e.g. a
	// restarted request's original Tx logs one during its aborted
	// vcl_deliver attempt before VCL_return restart fires); the last one
	// logged is this tx's own final word on its own records.
	if s, ok := tx.Last("RespStatus"); ok {
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
	// Last, not First: see requestAttrs' comment on the same substitution.
	if s, ok := tx.Last("BerespStatus"); ok {
		if n, err := strconv.Atoi(s); err == nil {
			attrs = append(attrs, attribute.Int("http.response.status_code", n))
		}
	}
	if b, ok := tx.First("Begin"); ok && strings.Contains(b, "bgfetch") {
		attrs = append(attrs, attribute.Bool("varnish.bgfetch", true))
	}
	return attrs
}
