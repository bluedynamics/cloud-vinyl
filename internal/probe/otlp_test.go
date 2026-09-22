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
