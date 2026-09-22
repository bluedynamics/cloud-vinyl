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
	go batcher.Run(ctx)

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
	s := &supervisor{
		binary:  envOrDefault("VARNISHLOG_PATH", "varnishlog"),
		backoff: time.Second,
		handle: func(tx *vsl.Tx) {
			built := spans.Build(tx, ids)
			if len(built) == 0 && tx.Type == "Request" {
				// Unsampled is intentional silence; a Request group with no
				// usable timestamps is data loss and must be counted. Build
				// cannot tell us which it was cheaply in P1, so count both;
				// unsampled traffic is rare in the deployments this targets.
				groupsUnusable.Inc()
			}
			for _, sp := range built {
				batcher.Enqueue(sp)
			}
		},
	}
	s.run(ctx)
}
