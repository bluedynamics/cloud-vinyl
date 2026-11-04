package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/bluedynamics/cloud-vinyl/internal/agent"
)

const (
	defaultAdminAddr   = "127.0.0.1:6082"
	defaultVarnishAddr = "http://127.0.0.1:8080"
	defaultListenAddr  = ":9090"
	defaultSecretPath  = "/etc/varnish/secret"    //nolint:gosec // file path, not a credential
	tokenPath          = "/run/vinyl/agent-token" //nolint:gosec // file path, not a credential
	adminMaxWait       = 60 * time.Second
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("vinyl-agent failed", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	adminAddr := envOrDefault("VARNISH_ADMIN_ADDR", defaultAdminAddr)
	secretPath := envOrDefault("VARNISH_SECRET_PATH", defaultSecretPath)
	varnishAddr := envOrDefault("VARNISH_HTTP_ADDR", defaultVarnishAddr)
	listenAddr := envOrDefault("AGENT_LISTEN_ADDR", defaultListenAddr)

	log.Info("vinyl-agent starting",
		"revision", revision(),
		"listen", listenAddr,
		"varnish_admin", adminAddr,
		"varnish_http", varnishAddr,
		"secret_path", secretPath,
		"token_path", tokenPath,
	)

	// 1. Read Bearer token
	tokenBytes, err := os.ReadFile(tokenPath)
	if err != nil {
		return fmt.Errorf("read token from %s: %w", tokenPath, err)
	}
	token := string(tokenBytes)
	log.Info("read bearer token", "path", tokenPath)

	// 2. Wait for varnishd admin port
	w := portWaiter{
		log:          log,
		dialTimeout:  2 * time.Second,
		pollInterval: 2 * time.Second,
		logInterval:  10 * time.Second,
	}
	if err := w.wait(ctx, adminAddr, adminMaxWait); err != nil {
		return fmt.Errorf("varnish admin port not available: %w", err)
	}

	// 3. Read varnish admin secret
	secretBytes, err := os.ReadFile(secretPath)
	if err != nil {
		return fmt.Errorf("read varnish secret from %s: %w", secretPath, err)
	}
	log.Info("read varnish admin secret", "path", secretPath)

	// 4. Create admin client
	adminClient := agent.NewAdminClient(adminAddr, string(secretBytes))

	// 5. Create xkey purger
	xkeyPurger := agent.NewXkeyPurger(varnishAddr)

	// 6. Start HTTP server
	srv := agent.NewServer(listenAddr, token, adminClient, xkeyPurger)
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", listenAddr, err)
	}
	log.Info("vinyl-agent listening", "addr", ln.Addr().String())
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ln)
	}()

	select {
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		return fmt.Errorf("server error: %w", err)
	}
}

// revision returns the VCS revision embedded by the Go toolchain, or
// "unknown" when the binary was built without VCS information.
func revision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && s.Value != "" {
			return s.Value
		}
	}
	return "unknown"
}

// portWaiter polls a TCP address until it accepts connections. The intervals
// are fields so tests can run it in milliseconds.
type portWaiter struct {
	log          *slog.Logger
	dialTimeout  time.Duration
	pollInterval time.Duration
	logInterval  time.Duration
}

// wait blocks until addr accepts a TCP connection, maxWait elapses or ctx is
// done. It logs the first failed attempt, then at most once per logInterval.
func (w portWaiter) wait(ctx context.Context, addr string, maxWait time.Duration) error {
	start := time.Now()
	deadline := start.Add(maxWait)
	var lastLog time.Time
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, w.dialTimeout)
		if err == nil {
			_ = conn.Close()
			w.log.Info("varnish admin port reachable", "addr", addr, "elapsed", time.Since(start).Round(time.Millisecond))
			return nil
		}
		lastErr = err
		if lastLog.IsZero() || time.Since(lastLog) >= w.logInterval {
			w.log.Info("waiting for varnish admin port",
				"addr", addr, "elapsed", time.Since(start).Round(time.Millisecond),
				"max_wait", maxWait, "last_error", err)
			lastLog = time.Now()
		}
		select {
		case <-ctx.Done():
			w.log.Info("stopped waiting for varnish admin port",
				"addr", addr, "elapsed", time.Since(start).Round(time.Millisecond), "reason", ctx.Err())
			return ctx.Err()
		case <-time.After(w.pollInterval):
		}
	}
	return fmt.Errorf("timeout waiting for %s after %v: %w", addr, maxWait, lastErr)
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
