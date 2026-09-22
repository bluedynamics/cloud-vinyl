package export

import (
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/bluedynamics/cloud-vinyl/internal/tracer/spans"
)

// roSpan adapts spans.Span to sdktrace.ReadOnlySpan so the stock otlptrace
// exporters accept our externally-minted ids and timestamps. The OTel SDK
// tracer cannot do this: it insists on generating ids itself.
type roSpan struct {
	// Embed the interface (as a nil value) purely to pick up its unexported
	// private() method, which otherwise seals ReadOnlySpan against external
	// implementations. Every method the interface declares is overridden
	// below, so the embedded nil value is never actually invoked; this is
	// the same technique the SDK's own tracetest.SpanStub.Snapshot() uses
	// ("Embed the interface to implement the private method.").
	sdktrace.ReadOnlySpan

	s     spans.Span
	res   *resource.Resource
	scope instrumentation.Scope
}

func (r roSpan) Name() string { return r.s.Name }
func (r roSpan) SpanContext() trace.SpanContext {
	return trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: r.s.TraceID, SpanID: r.s.SpanID,
		TraceFlags: trace.FlagsSampled,
	})
}
func (r roSpan) Parent() trace.SpanContext {
	if !r.s.ParentID.IsValid() {
		return trace.SpanContext{}
	}
	return trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: r.s.TraceID, SpanID: r.s.ParentID,
		TraceFlags: trace.FlagsSampled, Remote: true,
	})
}
func (r roSpan) SpanKind() trace.SpanKind                    { return r.s.Kind }
func (r roSpan) StartTime() time.Time                        { return r.s.Start }
func (r roSpan) EndTime() time.Time                          { return r.s.End }
func (r roSpan) Attributes() []attribute.KeyValue            { return r.s.Attrs }
func (r roSpan) Links() []sdktrace.Link                      { return nil }
func (r roSpan) Events() []sdktrace.Event                    { return nil }
func (r roSpan) Status() sdktrace.Status                     { return sdktrace.Status{} }
func (r roSpan) InstrumentationScope() instrumentation.Scope { return r.scope }

// InstrumentationLibrary is deprecated in favor of InstrumentationScope, but
// the ReadOnlySpan interface at this SDK version still requires it.
func (r roSpan) InstrumentationLibrary() instrumentation.Scope { return r.scope } //nolint:staticcheck
func (r roSpan) Resource() *resource.Resource                  { return r.res }
func (r roSpan) DroppedAttributes() int                        { return 0 }
func (r roSpan) DroppedLinks() int                             { return 0 }
func (r roSpan) DroppedEvents() int                            { return 0 }
func (r roSpan) ChildSpanCount() int                           { return 0 }

// Compile-time check that roSpan satisfies the installed SDK's ReadOnlySpan
// interface (verified here, ahead of batcher.go, per the alignment step).
var _ sdktrace.ReadOnlySpan = roSpan{}
