package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/bluedynamics/cloud-vinyl/internal/tracer/vsl"
)

var (
	varnishlogRestarts = promauto.NewCounter(prometheus.CounterOpts{
		Name: "vinyl_tracer_varnishlog_restarts_total",
		Help: "Times the varnishlog subprocess was (re)spawned.",
	})
	groupsUnusable = promauto.NewCounter(prometheus.CounterOpts{
		Name: "vinyl_tracer_groups_unusable_total",
		Help: "Request groups that were genuinely unusable (missing a " +
			"timestamp a span needs, e.g. truncated/overrun log data) and " +
			"yielded no spans. Trace loss must be visible, never silent.",
	})
	groupsUnsampled = promauto.NewCounter(prometheus.CounterOpts{
		Name: "vinyl_tracer_groups_unsampled_total",
		Help: "Request groups skipped because the incoming trace context is unsampled.",
	})
	linkCacheMisses = promauto.NewCounter(prometheus.CounterOpts{
		Name: "vinyl_tracer_link_cache_misses_total",
		Help: "spans.Linker correlations (hit-to-fetch or restart continuation) that " +
			"could not resolve because the target vxid was never cached or aged out, " +
			"including a parked hit released without ever resolving (age-out, " +
			"parking-lot overflow eviction, or Flush at shutdown).",
	})
	hitsParked = promauto.NewCounter(prometheus.CounterOpts{
		Name: "vinyl_tracer_hits_parked_total",
		Help: "Hit records spans.Linker withheld (parked) because they proved " +
			"themselves genuine coalescing waiters (their own Waitinglist record) " +
			"whose fetch vxid had not yet resolved — the waiter-first half of the " +
			"known VSL group-ordering race. An ordinary warm hit (no Waitinglist " +
			"record) is never parked and does not count here. Most parked hits go " +
			"on to resolve a Build call or two later; see " +
			"vinyl_tracer_link_cache_misses_total for the ones that never do.",
	})
)

// supervisor keeps one varnishlog subprocess running and feeds its stdout
// through the VSL parser. handle is called per parsed top-level group;
// onGroup/onRestart are test seams and metrics hooks.
type supervisor struct {
	binary    string
	backoff   time.Duration // initial; doubles to a 30s cap, resets on output
	handle    func(*vsl.Tx)
	onGroup   func()
	onRestart func()
}

func (s *supervisor) run(ctx context.Context) {
	backoff := s.backoff
	for ctx.Err() == nil {
		varnishlogRestarts.Inc()
		if s.onRestart != nil {
			s.onRestart()
		}
		// -t off: wait for the VSM indefinitely, so the sidecar starting
		// before varnishd is not an error.
		cmd := exec.CommandContext(ctx, s.binary, "-g", "request", "-t", "off")
		cmd.Stderr = os.Stderr
		out, err := cmd.StdoutPipe()
		if err == nil {
			if err = cmd.Start(); err == nil {
				p := vsl.NewParser(out)
				for {
					tx, perr := p.Next()
					if perr != nil {
						break
					}
					backoff = s.backoff // making progress: reset backoff
					if s.handle != nil {
						s.handle(tx)
					}
					if s.onGroup != nil {
						s.onGroup()
					}
				}
				_ = cmd.Wait()
			}
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "varnishlog spawn: %v\n", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}
