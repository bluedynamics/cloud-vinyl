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
		Help: "Request groups that yielded no spans (truncated/overrun log data). Trace loss must be visible, never silent.",
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
