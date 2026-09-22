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
