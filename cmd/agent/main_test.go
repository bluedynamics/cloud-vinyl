package main

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"
)

func testWaiter(buf *bytes.Buffer) portWaiter {
	return portWaiter{
		log:          slog.New(slog.NewTextHandler(buf, nil)),
		dialTimeout:  50 * time.Millisecond,
		pollInterval: 5 * time.Millisecond,
		logInterval:  time.Hour,
	}
}

// closedAddr returns a local address nothing listens on.
func closedAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func TestWaitForPortReadyAfterFailures(t *testing.T) {
	addr := closedAddr(t)
	var buf bytes.Buffer
	go func() {
		time.Sleep(60 * time.Millisecond)
		l, err := net.Listen("tcp", addr)
		if err != nil {
			return
		}
		t.Cleanup(func() { _ = l.Close() })
	}()
	if err := testWaiter(&buf).wait(context.Background(), addr, 5*time.Second); err != nil {
		t.Fatalf("wait: %v", err)
	}
	out := buf.String()
	if strings.Count(out, "waiting for varnish admin port") != 1 {
		t.Errorf("want exactly one waiting line (logInterval is 1h), got:\n%s", out)
	}
	if !strings.Contains(out, "varnish admin port reachable") || !strings.Contains(out, "elapsed=") {
		t.Errorf("missing ready line with elapsed, got:\n%s", out)
	}
}

func TestWaitForPortImmediate(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	var buf bytes.Buffer
	if err := testWaiter(&buf).wait(context.Background(), l.Addr().String(), time.Second); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if strings.Contains(buf.String(), "waiting for") {
		t.Errorf("no waiting line expected, got:\n%s", buf.String())
	}
}

func TestWaitForPortTimeout(t *testing.T) {
	var buf bytes.Buffer
	err := testWaiter(&buf).wait(context.Background(), closedAddr(t), 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timeout waiting for") {
		t.Fatalf("want timeout error, got %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "waiting for varnish admin port") || !strings.Contains(out, "last_error=") {
		t.Errorf("missing waiting line with last_error, got:\n%s", buf.String())
	}
}

func TestWaitForPortPeriodicLogging(t *testing.T) {
	var buf bytes.Buffer
	w := testWaiter(&buf)
	w.logInterval = 20 * time.Millisecond
	_ = w.wait(context.Background(), closedAddr(t), 120*time.Millisecond)
	if n := strings.Count(buf.String(), "waiting for varnish admin port"); n < 2 || n > 8 {
		t.Errorf("want a few periodic lines, got %d:\n%s", n, buf.String())
	}
}

func TestWaitForPortContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var buf bytes.Buffer
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	err := testWaiter(&buf).wait(ctx, closedAddr(t), 5*time.Second)
	if err != context.Canceled {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if !strings.Contains(buf.String(), "stopped waiting") {
		t.Errorf("missing stop line, got:\n%s", buf.String())
	}
}
