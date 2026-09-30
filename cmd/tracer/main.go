// vinyl-tracer reads the Varnish Shared memory Log via a version-matched
// varnishlog subprocess, builds OTel spans with real VSL timestamps, and
// exports them via OTLP. It runs as a sidecar in the varnish pod; the
// operator sets its environment from spec.tracing.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/bluedynamics/cloud-vinyl/internal/tracer/export"
	"github.com/bluedynamics/cloud-vinyl/internal/tracer/spans"
	"github.com/bluedynamics/cloud-vinyl/internal/tracer/vsl"
)

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	endpoint := os.Getenv("OTLP_ENDPOINT")
	service := os.Getenv("TRACER_SERVICE_NAME")
	if endpoint == "" || service == "" {
		fmt.Fprintln(os.Stderr, "OTLP_ENDPOINT and TRACER_SERVICE_NAME are required")
		os.Exit(1)
	}

	// 1. OTLP exporter + batcher.
	exp, err := export.NewExporter(ctx, endpoint,
		envOrDefault("OTLP_PROTOCOL", "grpc"),
		os.Getenv("OTLP_INSECURE") == "true")
	if err != nil {
		fmt.Fprintf(os.Stderr, "otlp exporter: %v\n", err)
		os.Exit(1)
	}
	batcher := export.NewBatcher(exp, service, 2048)
	// The batcher runs on its OWN cancellation, deliberately NOT the signal
	// ctx. ctx.Done() fires for s.run and the batcher at the very same
	// instant a SIGTERM arrives; Run's ctx.Done() branch drains whatever is
	// in the channel AT THAT INSTANT and returns within microseconds — long
	// before s.run(ctx) finishes tearing down the varnishlog subprocess and
	// before the post-shutdown linker.Flush() loop below has enqueued the
	// parking lot's contents. Sharing ctx would mean every tail handle()
	// Enqueue call still in flight during that teardown, and every span
	// Flush releases, lands in a channel nobody is reading anymore:
	// Enqueue still succeeds (there is room in the buffer), so
	// droppedTotal would not even fire — spans lost doubly silently.
	// batcherCtx is cancelled explicitly, below, only once both s.run(ctx)
	// and the Flush-enqueue loop have returned — see
	// TestShutdown_PostSupervisorSpansStillExport for the regression this
	// fixes (the previous shared-ctx wiring fails that test).
	batcherCtx, stopBatcher := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() { batcher.Run(batcherCtx) })
	// Joined after stopBatcher() below, so Run's drain-on-cancel branch only
	// runs once nothing more will ever be enqueued (see
	// internal/tracer/export.TestBatcher_ExportsEnqueuedSpans for the
	// drain-on-cancel behavior itself).
	defer wg.Wait()

	// 2. Metrics endpoint.
	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		srv := &http.Server{
			Addr:              envOrDefault("TRACER_METRICS_ADDR", ":9464"),
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		}
		if err := srv.ListenAndServe(); err != nil {
			fmt.Fprintf(os.Stderr, "metrics server: %v\n", err)
		}
	}()

	// 3. Supervise varnishlog and pump groups into the pipeline.
	ids := spans.NewRandomIDs()
	// P3 Task 5: spans.Linker wraps BuildWithOutcome with cross-transaction
	// correlation (hit-to-fetch links with coalescing detection, restart
	// continuation stitching) that a single top-level Tx cannot see on its
	// own. It is stateful but single-goroutine — handle below is called
	// serially by supervisor.run, matching the Linker's documented
	// no-mutex contract.
	linker := spans.NewLinker(4096, 5*time.Minute, ids)
	linker.OnCacheMiss = linkCacheMisses.Inc
	linker.OnHitParked = hitsParked.Inc
	s := &supervisor{
		binary:  envOrDefault("VARNISHLOG_PATH", "varnishlog"),
		backoff: time.Second,
		handle: func(tx *vsl.Tx) {
			// P3: spans.BuildWithOutcome (wrapped here by linker.Build)
			// distinguishes intentional silence (an unsampled trace,
			// spans.OutcomeUnsampled) from real data loss (a group missing
			// timestamps a span needs, e.g. truncated/overrun log data,
			// spans.OutcomeUnusable) instead of lumping both into one
			// counter as the P1 version of this closure did.
			built, outcome := linker.Build(tx)
			switch outcome {
			case spans.OutcomeUnsampled:
				groupsUnsampled.Inc()
			case spans.OutcomeUnusable:
				groupsUnusable.Inc()
			}
			for _, sp := range built {
				batcher.Enqueue(sp)
			}
		},
	}
	s.run(ctx)

	// Graceful shutdown: s.run returned because ctx was cancelled (SIGINT/
	// SIGTERM). Any span sets still in the Linker's parking lot (P3's
	// bounded wait for a waiter-first hit's initiator fetch, see
	// spans.Linker.Flush's doc) would otherwise never be resolved or
	// exported — flush them, degraded.
	for _, sp := range linker.Flush() {
		batcher.Enqueue(sp)
	}
	// Only now is it safe to let the batcher drain and return: everything
	// this process will ever enqueue has been enqueued. The deferred
	// wg.Wait() above blocks until Run's own drain-on-cancel finishes.
	stopBatcher()
}
