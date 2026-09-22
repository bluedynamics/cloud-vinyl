package export

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

	"github.com/bluedynamics/cloud-vinyl/internal/tracer/spans"
)

var (
	exportedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "vinyl_tracer_spans_exported_total",
		Help: "Spans handed to the OTLP exporter.",
	})
	droppedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "vinyl_tracer_spans_dropped_total",
		Help: "Spans dropped because the export queue was full.",
	})
	exportErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "vinyl_tracer_export_errors_total",
		Help: "Failed OTLP export batches.",
	})
)

// NewExporter builds the OTLP span exporter. protocol: "grpc" or
// "http/protobuf".
func NewExporter(ctx context.Context, endpoint, protocol string, insecure bool) (sdktrace.SpanExporter, error) {
	switch protocol {
	case "", "grpc":
		opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(endpoint)}
		if insecure {
			opts = append(opts, otlptracegrpc.WithInsecure())
		}
		return otlptracegrpc.New(ctx, opts...)
	case "http/protobuf":
		opts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(endpoint)}
		if insecure {
			opts = append(opts, otlptracehttp.WithInsecure())
		}
		return otlptracehttp.New(ctx, opts...)
	default:
		return nil, fmt.Errorf("unknown OTLP protocol %q", protocol)
	}
}

// Batcher buffers spans behind a bounded, non-blocking queue and flushes
// them to a sdktrace.SpanExporter on a timer, on a size threshold, or on
// shutdown.
type Batcher struct {
	ch    chan spans.Span
	exp   sdktrace.SpanExporter
	res   *resource.Resource
	scope instrumentation.Scope
}

// NewBatcher returns a Batcher that exports through exp, tagging spans with
// serviceName, and queues at most queueLen spans before Enqueue starts
// dropping.
func NewBatcher(exp sdktrace.SpanExporter, serviceName string, queueLen int) *Batcher {
	return &Batcher{
		ch:  make(chan spans.Span, queueLen),
		exp: exp,
		res: resource.NewWithAttributes(semconv.SchemaURL,
			semconv.ServiceName(serviceName)),
		scope: instrumentation.Scope{Name: "vinyl-tracer"},
	}
}

// Enqueue never blocks: the request path must never notice the tracer, and
// trace loss must be a visible counter, not backpressure.
func (b *Batcher) Enqueue(s spans.Span) {
	select {
	case b.ch <- s:
	default:
		droppedTotal.Inc()
	}
}

// Run flushes batches every interval until ctx is cancelled, then drains.
func (b *Batcher) Run(ctx context.Context) {
	const flushEvery = 3 * time.Second
	const maxBatch = 512
	tick := time.NewTicker(flushEvery)
	defer tick.Stop()
	var buf []sdktrace.ReadOnlySpan
	flush := func() {
		if len(buf) == 0 {
			return
		}
		expCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := b.exp.ExportSpans(expCtx, buf); err != nil {
			exportErrors.Inc()
		} else {
			exportedTotal.Add(float64(len(buf)))
		}
		cancel()
		buf = buf[:0]
	}
	for {
		select {
		case s := <-b.ch:
			buf = append(buf, roSpan{s: s, res: b.res, scope: b.scope})
			if len(buf) >= maxBatch {
				flush()
			}
		case <-tick.C:
			flush()
		case <-ctx.Done():
			for {
				select {
				case s := <-b.ch:
					buf = append(buf, roSpan{s: s, res: b.res, scope: b.scope})
				default:
					flush()
					return
				}
			}
		}
	}
}
