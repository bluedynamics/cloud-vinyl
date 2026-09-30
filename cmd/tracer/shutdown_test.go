package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/bluedynamics/cloud-vinyl/internal/tracer/export"
	"github.com/bluedynamics/cloud-vinyl/internal/tracer/spans"
)

func shutdownTestSpan(n byte) spans.Span {
	return spans.Span{
		TraceID: trace.TraceID{0xaa, n}, SpanID: trace.SpanID{n},
		Name: "varnish request", Kind: trace.SpanKindServer,
		Start: time.Unix(100, 0), End: time.Unix(101, 0),
	}
}

// TestShutdown_PostSupervisorSpansStillExport is a regression test for a
// shutdown-ordering bug code review caught in this package's first version
// of the batcher wiring: the batcher's Run was driven by the SAME
// signal.NotifyContext as the supervisor loop. A SIGTERM fires ctx.Done()
// for both at once, and export.Batcher.Run's ctx.Done() branch drains
// whatever is in its channel AT THAT INSTANT and returns within
// microseconds — long before the supervisor has finished tearing down its
// varnishlog subprocess, and certainly before main's post-supervisor
// linker.Flush() loop runs. Spans enqueued in that window — ordinary
// handle() calls still landing as the last varnishlog records are
// processed, and every span Flush releases from the Linker's parking lot —
// went into a channel nobody was reading anymore: Batcher.Enqueue still
// succeeds (there was room), so even vinyl_tracer_spans_dropped_total never
// fired. Pure, silent loss.
//
// The fix (see main's construction of batcherCtx/stopBatcher): the batcher
// runs on its OWN context, cancelled explicitly only after both the
// supervisor loop and the post-shutdown Flush-enqueue loop have returned.
// This test drives that exact pattern directly, standing in for main()
// itself (not independently testable: real env vars, a real OTLP network
// exporter, os.Exit). Applying the OLD shared-ctx pattern in this test
// instead — driving Run with outerCtx directly — fails it: the two
// post-signal Enqueue calls below land after Run has already drained and
// returned, so only the pre-signal span would ever reach the exporter.
func TestShutdown_PostSupervisorSpansStillExport(t *testing.T) {
	mem := tracetest.NewInMemoryExporter()
	batcher := export.NewBatcher(mem, "test-svc", 16)

	// outerCtx stands in for main's signal.NotifyContext: cancelled first,
	// simulating a SIGTERM landing — exactly like the supervisor's own ctx.
	outerCtx, cancelOuter := context.WithCancel(context.Background())
	defer cancelOuter()

	// batcherCtx/stopBatcher stand in for main's decoupled batcher
	// lifecycle: deliberately NOT outerCtx.
	batcherCtx, stopBatcher := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { batcher.Run(batcherCtx); close(done) }()

	batcher.Enqueue(shutdownTestSpan(1))

	// The "SIGTERM arrives" instant: outerCtx is done, but the batcher's
	// own context is untouched — it must keep accepting spans.
	cancelOuter()
	<-outerCtx.Done()

	// Spans that land AFTER the signal but BEFORE the batcher is told to
	// stop — exactly what a tail handle() call or linker.Flush() produces
	// during real shutdown — must still make it out.
	batcher.Enqueue(shutdownTestSpan(2))
	batcher.Enqueue(shutdownTestSpan(3))

	stopBatcher()
	<-done

	got := mem.GetSpans()
	require.Len(t, got, 3,
		"every span enqueued before stopBatcher, including ones enqueued "+
			"after the outer/signal ctx was already done, must be exported")
}
