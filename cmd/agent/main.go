package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bluedynamics/cloud-vinyl/internal/agent"
)

const (
	defaultAdminAddr   = "127.0.0.1:6082"
	defaultVarnishAddr = "http://127.0.0.1:8080"
	defaultListenAddr  = ":9090"
	tokenPath          = "/run/vinyl/agent-token" //nolint:gosec // file path, not a credential
	adminMaxWait       = 60 * time.Second
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// 1. Read Bearer token
	tokenBytes, err := os.ReadFile(tokenPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read token from %s: %v\n", tokenPath, err)
		os.Exit(1)
	}
	token := string(tokenBytes)

	// 2. Wait for varnishd admin port
	adminAddr := envOrDefault("VARNISH_ADMIN_ADDR", defaultAdminAddr)
	if err := waitForPort(ctx, adminAddr, adminMaxWait); err != nil {
		fmt.Fprintf(os.Stderr, "varnish admin port not available: %v\n", err)
		os.Exit(1)
	}

	// 3. Read varnish admin secret
	secretPath := envOrDefault("VARNISH_SECRET_PATH", "/etc/varnish/secret")
	secretBytes, err := os.ReadFile(secretPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read varnish secret from %s: %v\n", secretPath, err)
		os.Exit(1)
	}

	// 4. Create admin client
	adminClient := agent.NewAdminClient(adminAddr, string(secretBytes))

	// 5. Create xkey purger
	varnishAddr := envOrDefault("VARNISH_HTTP_ADDR", defaultVarnishAddr)
	xkeyPurger := agent.NewXkeyPurger(varnishAddr)

	// 6. Start HTTP server
	listenAddr := envOrDefault("AGENT_LISTEN_ADDR", defaultListenAddr)
	srv := agent.NewServer(listenAddr, token, adminClient, xkeyPurger)

	fmt.Printf("vinyl-agent starting on %s\n", listenAddr)
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		_ = srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}

func waitForPort(ctx context.Context, addr string, maxWait time.Duration) error {
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("timeout waiting for %s after %v", addr, maxWait)
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
