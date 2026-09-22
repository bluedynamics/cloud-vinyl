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
