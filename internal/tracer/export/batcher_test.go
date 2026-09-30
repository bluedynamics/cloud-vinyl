package export

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
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

// svcName walks got.Resource.Attributes() for service.name.
func svcName(t *testing.T, s tracetest.SpanStub) string {
	t.Helper()
	for _, kv := range s.Resource.Attributes() {
		if kv.Key == attribute.Key("service.name") {
			return kv.Value.AsString()
		}
	}
	return ""
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

// TestBatcher_ExportsLinkedSpanRoundTrip is the export layer's TDD target
// for Task 6: a spans.Span carrying a Link must survive roSpan.Links()'s
// mapping and the batcher's real export path (tracetest.InMemoryExporter,
// not a hand-rolled fake) with its trace/span id and attributes intact.
func TestBatcher_ExportsLinkedSpanRoundTrip(t *testing.T) {
	mem := tracetest.NewInMemoryExporter()
	b := NewBatcher(mem, "test-svc", 16)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.Run(ctx); close(done) }()

	linked := testSpan(1)
	linked.Links = []spans.Link{{
		TraceID: trace.TraceID{0xcc, 1},
		SpanID:  trace.SpanID{0xdd, 1},
		Attrs:   []attribute.KeyValue{attribute.String("varnish.link", "origin-fetch")},
	}}
	b.Enqueue(linked)
	cancel()
	<-done

	got := mem.GetSpans()
	require.Len(t, got, 1)
	require.Len(t, got[0].Links, 1)
	link := got[0].Links[0]
	assert.Equal(t, trace.TraceID{0xcc, 1}, link.SpanContext.TraceID())
	assert.Equal(t, trace.SpanID{0xdd, 1}, link.SpanContext.SpanID())
	require.Len(t, link.Attributes, 1)
	assert.Equal(t, attribute.String("varnish.link", "origin-fetch"), link.Attributes[0])
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
