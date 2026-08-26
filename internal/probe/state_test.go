package probe

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fixedTokenServer always answers with tok, regardless of what it receives.
// This is what a URL still serving a seeded object looks like: the backend
// is never touched again, so the response never changes.
func fixedTokenServer(tok string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"headers":{"x-probe":%q}}`, tok)
	}))
}

// countingServer echoes the current X-Probe value and counts how many
// requests it received, so tests can assert Seed/Check make exactly one
// request each — the property that makes Check a measurement rather than a
// mutation of the state it observes.
func countingServer() (*httptest.Server, *int32) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		fmt.Fprintf(w, `{"headers":{"x-probe":%q}}`, r.Header.Get("X-Probe"))
	}))
	return srv, &n
}

func TestSeedReturnsANonEmptyToken(t *testing.T) {
	srv := echoingServer()
	defer srv.Close()

	tok, err := Seed(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("Seed returned error: %v", err)
	}
	if tok == "" {
		t.Fatal("expected a non-empty token")
	}
}

func TestSeedIssuesExactlyOneRequest(t *testing.T) {
	srv, n := countingServer()
	defer srv.Close()

	if _, err := Seed(context.Background(), srv.Client(), srv.URL); err != nil {
		t.Fatalf("Seed returned error: %v", err)
	}
	if got := atomic.LoadInt32(n); got != 1 {
		t.Fatalf("expected exactly 1 request, got %d", got)
	}
}

func TestSeedPropagatesTransportErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if _, err := Seed(ctx, srv.Client(), srv.URL); err == nil {
		t.Fatal("expected an error when the context expires, got nil")
	}
}

func TestCheckReportsCachedWhenBodyEchoesTheSeedToken(t *testing.T) {
	seed := "probe-seed-token"
	srv := fixedTokenServer(seed)
	defer srv.Close()

	got, err := Check(context.Background(), srv.Client(), srv.URL, seed)
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if got != Cached {
		t.Fatalf("got %v, want cached", got)
	}
}

func TestCheckReportsNotCachedWhenBodyEchoesItsOwnToken(t *testing.T) {
	srv := echoingServer()
	defer srv.Close()

	// echoingServer always echoes whatever token Check itself sends, which is
	// what a URL looks like once the seeded object is gone: the backend is
	// hit fresh and the response carries this request's own token, not the
	// seed's.
	got, err := Check(context.Background(), srv.Client(), srv.URL, "some-other-seed-token")
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if got != NotCached {
		t.Fatalf("got %v, want not-cached", got)
	}
}

func TestCheckErrorsWhenBodyEchoesNeitherSeedNorOwnToken(t *testing.T) {
	srv := fixedTokenServer("someone-elses-token")
	defer srv.Close()

	_, err := Check(context.Background(), srv.Client(), srv.URL, "the-seed-we-expected")
	if err == nil {
		t.Fatal("expected an error when the body echoes neither token, got nil")
	}
	if !strings.Contains(err.Error(), "neither") {
		t.Fatalf("error should explain the cause, got: %v", err)
	}
}

func TestCheckIssuesExactlyOneRequest(t *testing.T) {
	srv, n := countingServer()
	defer srv.Close()

	if _, err := Check(context.Background(), srv.Client(), srv.URL, "whatever"); err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if got := atomic.LoadInt32(n); got != 1 {
		t.Fatalf("expected exactly 1 request, got %d", got)
	}
}

func TestCheckPropagatesTransportErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if _, err := Check(ctx, srv.Client(), srv.URL, "whatever"); err == nil {
		t.Fatal("expected an error when the context expires, got nil")
	}
}

func TestStateStringCachedAndNotCached(t *testing.T) {
	if got := Cached.String(); got != "cached" {
		t.Fatalf("Cached.String() = %q, want %q", got, "cached")
	}
	if got := NotCached.String(); got != "not-cached" {
		t.Fatalf("NotCached.String() = %q, want %q", got, "not-cached")
	}
}
