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

// exportReqTyped builds an ExportTraceServiceRequest whose single span
// carries the given typed attributes verbatim — real oneof AnyValue
// variants, counterpart-true to what the tracer's own exporter sends (e.g.
// attribute.Int("http.response.status_code", 200),
// attribute.Bool("varnish.bgfetch", true)), not just strings.
func exportReqTyped(name string, attrs []*commonpb.KeyValue) []byte {
	req := &collpb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{{
					Name:       name,
					TraceId:    bytes.Repeat([]byte{0xaa}, 16),
					SpanId:     bytes.Repeat([]byte{0xbb}, 8),
					Attributes: attrs,
				}},
			}},
		}},
	}
	b, _ := proto.Marshal(req)
	return b
}

// TestOTLPSink_NonStringAttrsFormatAsPlainText is the falsification target
// for the review finding: an int or bool attribute must summarize as its
// natural decimal text ("200", "true"), not prototext ("int_value:200",
// "bool_value:true"). The tracer's own spans carry exactly these types
// (attribute.Int, attribute.Bool), so a wrong format here would make every
// -assert-spans -attr on a non-string attribute silently never match.
func TestOTLPSink_NonStringAttrsFormatAsPlainText(t *testing.T) {
	sink := NewOTLPSink(128)
	srv := httptest.NewServer(sink.Handler())
	defer srv.Close()

	body := exportReqTyped("varnish request", []*commonpb.KeyValue{
		{
			Key: "http.response.status_code",
			Value: &commonpb.AnyValue{
				Value: &commonpb.AnyValue_IntValue{IntValue: 200},
			},
		},
		{
			Key: "varnish.bgfetch",
			Value: &commonpb.AnyValue{
				Value: &commonpb.AnyValue_BoolValue{BoolValue: true},
			},
		},
	})
	resp, err := http.Post(srv.URL+"/v1/traces", "application/x-protobuf", bytes.NewReader(body))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	list, err := http.Get(srv.URL + "/spans")
	require.NoError(t, err)
	var got []SpanSummary
	require.NoError(t, json.NewDecoder(list.Body).Decode(&got))
	require.Len(t, got, 1)
	assert.Equal(t, "200", got[0].Attrs["http.response.status_code"])
	assert.Equal(t, "true", got[0].Attrs["varnish.bgfetch"])
}

// TestOTLPSink_EmptyStringAttrIsPreserved confirms the "" sentinel bug is
// gone: a genuinely empty string attribute value must round-trip as "",
// not fall through to the array/kvlist/bytes prototext fallback.
func TestOTLPSink_EmptyStringAttrIsPreserved(t *testing.T) {
	sink := NewOTLPSink(128)
	srv := httptest.NewServer(sink.Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/traces", "application/x-protobuf",
		bytes.NewReader(exportReq("varnish request", "varnish.route", "")))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	list, err := http.Get(srv.URL + "/spans")
	require.NoError(t, err)
	var got []SpanSummary
	require.NoError(t, json.NewDecoder(list.Body).Decode(&got))
	require.Len(t, got, 1)
	val, ok := got[0].Attrs["varnish.route"]
	assert.True(t, ok, "attribute key must be present even with an empty value")
	assert.Equal(t, "", val)
}
