// Package probe: OTLP sink + span assertions for E2E. HTTP only — this
// package is imported by cmd/vinylprobe, which must not pull in k8s.io.
package probe

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"sync"

	"google.golang.org/protobuf/proto"

	collpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

// SpanSummary is the JSON shape GET /spans returns: enough of a received
// OTLP span for an E2E assertion to match on, with trace/span/parent IDs
// hex-encoded for readability.
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

// NewOTLPSink creates a sink that keeps at most the max most recently
// received spans, dropping the oldest once that bound is exceeded.
func NewOTLPSink(max int) *OTLPSink { return &OTLPSink{max: max} }

// Handler returns the sink's HTTP routes: POST /v1/traces (OTLP/http-protobuf
// ingest), GET /spans (JSON list of what was received), and GET /healthz.
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
					attrs[kv.GetKey()] = formatAnyValue(kv.GetValue())
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

// formatAnyValue renders an OTLP attribute value as the string an
// -assert-spans -attr key=value comparison can match against. A string
// value is returned as-is, even when empty — GetStringValue's own zero
// value cannot be used as an "absent" sentinel, since "" is itself a
// legitimate attribute value. Int/bool/double get their natural decimal
// text (strconv), not prototext ("int_value:200", "bool_value:true"): the
// tracer's own exporter emits exactly these types (attribute.Int(...),
// attribute.Bool(...)), so falling back to .String() here would make every
// -attr on a non-string attribute silently never match. array/kvlist/bytes
// values have no natural scalar text, so .String() (prototext) remains the
// last-resort fallback for those.
func formatAnyValue(v *commonpb.AnyValue) string {
	switch x := v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		return x.StringValue
	case *commonpb.AnyValue_BoolValue:
		return strconv.FormatBool(x.BoolValue)
	case *commonpb.AnyValue_IntValue:
		return strconv.FormatInt(x.IntValue, 10)
	case *commonpb.AnyValue_DoubleValue:
		return strconv.FormatFloat(x.DoubleValue, 'g', -1, 64)
	default:
		return v.String()
	}
}

func (s *OTLPSink) list(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.spans)
}
