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
