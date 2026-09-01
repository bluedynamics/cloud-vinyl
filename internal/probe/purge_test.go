package probe

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// statusServer answers every request with code, regardless of method.
func statusServer(code int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
	}))
}

// jsonPurgeServer answers with code and a JSON body carrying the given
// "objectsPurged" value — a stand-in for the invalidation proxy's real
// response (see internal/proxy.BroadcastResult), which is where the
// objectsPurged field actually comes from.
func jsonPurgeServer(code int, body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		fmt.Fprint(w, body)
	}))
}

func TestPurgeSendsMethodPurgeNotGet(t *testing.T) {
	var gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if _, err := Purge(context.Background(), srv.Client(), srv.URL, ""); err != nil {
		t.Fatalf("Purge returned error: %v", err)
	}
	if gotMethod != purgeMethod {
		t.Fatalf("got method %q, want %q", gotMethod, purgeMethod)
	}
}

func TestPurgeReturnsNilErrorOnMatchedPurge(t *testing.T) {
	srv := statusServer(http.StatusOK)
	defer srv.Close()

	if _, err := Purge(context.Background(), srv.Client(), srv.URL, ""); err != nil {
		t.Fatalf("Purge returned error on 200: %v", err)
	}
}

func TestPurgeReturnsNilErrorOnNothingToPurge(t *testing.T) {
	srv := statusServer(http.StatusNotFound)
	defer srv.Close()

	if _, err := Purge(context.Background(), srv.Client(), srv.URL, ""); err != nil {
		t.Fatalf("Purge returned error on 404: %v", err)
	}
}

func TestPurgeReturnsNilErrorOnPartialSuccess(t *testing.T) {
	// 207 Multi-Status is what the invalidation proxy answers when some
	// pods failed and some succeeded (BroadcastResult.Status == "partial").
	// That is still an understood, executed purge, not a probe error.
	srv := statusServer(http.StatusMultiStatus)
	defer srv.Close()

	if _, err := Purge(context.Background(), srv.Client(), srv.URL, ""); err != nil {
		t.Fatalf("Purge returned error on 207: %v", err)
	}
}

func TestPurgeReturnsErrorOnMethodNotAllowed(t *testing.T) {
	srv := statusServer(http.StatusMethodNotAllowed)
	defer srv.Close()

	_, err := Purge(context.Background(), srv.Client(), srv.URL, "")
	if err == nil {
		t.Fatal("expected an error on 405, got nil")
	}
	if !strings.Contains(err.Error(), strconv.Itoa(http.StatusMethodNotAllowed)) {
		t.Fatalf("error should mention the status code, got: %v", err)
	}
}

func TestPurgeReturnsErrorOnServerError(t *testing.T) {
	srv := statusServer(http.StatusInternalServerError)
	defer srv.Close()

	_, err := Purge(context.Background(), srv.Client(), srv.URL, "")
	if err == nil {
		t.Fatal("expected an error on 500, got nil")
	}
	if !strings.Contains(err.Error(), strconv.Itoa(http.StatusInternalServerError)) {
		t.Fatalf("error should mention the status code, got: %v", err)
	}
}

func TestPurgeReturnsErrorWhenContextAlreadyCancelled(t *testing.T) {
	srv := statusServer(http.StatusOK)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := Purge(ctx, srv.Client(), srv.URL, ""); err == nil {
		t.Fatal("expected an error when the context is already cancelled, got nil")
	}
}

func TestPurgeReturnsErrorWhenContextExpires(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if _, err := Purge(ctx, srv.Client(), srv.URL, ""); err == nil {
		t.Fatal("expected an error when the context expires, got nil")
	}
}

// --- objectsPurged count parsing ---
//
// These mirror the distinction internal/proxy.BroadcastResult.ObjectsPurged
// makes (see internal/proxy/broadcast.go): a known count, even zero, must
// never be confused with an unknown one. #103 built that distinction into
// the wire format; these tests confirm Purge does not collapse it back
// together while reading it.

func TestPurgeReturnsKnownCountFromJSONBody(t *testing.T) {
	srv := jsonPurgeServer(http.StatusOK, `{"status":"ok","total":3,"succeeded":3,"objectsPurged":3,"results":[]}`)
	defer srv.Close()

	n, err := Purge(context.Background(), srv.Client(), srv.URL, "")
	if err != nil {
		t.Fatalf("Purge returned error: %v", err)
	}
	if n == nil {
		t.Fatal("expected a known count, got nil (unknown)")
	}
	if *n != 3 {
		t.Fatalf("got count %d, want 3", *n)
	}
}

func TestPurgeReturnsKnownZeroDistinctFromUnknown(t *testing.T) {
	// A correctly sharded broadcast purge legitimately removes 0 objects on
	// every pod but the URL's owner (#92) — the response still carries
	// "objectsPurged":0, a known fact, not the absence of one.
	srv := jsonPurgeServer(http.StatusOK, `{"status":"ok","total":3,"succeeded":3,"objectsPurged":0,"results":[]}`)
	defer srv.Close()

	n, err := Purge(context.Background(), srv.Client(), srv.URL, "")
	if err != nil {
		t.Fatalf("Purge returned error: %v", err)
	}
	if n == nil {
		t.Fatal("expected a known count of 0, got nil (unknown) — 0 and unknown must stay distinct")
	}
	if *n != 0 {
		t.Fatalf("got count %d, want 0", *n)
	}
}

func TestPurgeReturnsNilCountWhenFieldOmitted(t *testing.T) {
	// The real proxy omits objectsPurged entirely (rather than sending
	// null or 0) when no pod reported a parseable count.
	srv := jsonPurgeServer(http.StatusOK, `{"status":"ok","total":3,"succeeded":3,"results":[]}`)
	defer srv.Close()

	n, err := Purge(context.Background(), srv.Client(), srv.URL, "")
	if err != nil {
		t.Fatalf("Purge returned error: %v", err)
	}
	if n != nil {
		t.Fatalf("expected nil (unknown) count, got %d", *n)
	}
}

func TestPurgeReturnsNilCountOnNonJSONBody(t *testing.T) {
	// A PURGE sent directly to a Varnish pod, rather than through the
	// invalidation proxy, answers with Varnish's own plain-text synth body
	// (e.g. "Purged 1 objects"), not JSON. That must not be treated as an
	// error — just as an unknown count, same as a missing field.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "Purged 1 objects")
	}))
	defer srv.Close()

	n, err := Purge(context.Background(), srv.Client(), srv.URL, "")
	if err != nil {
		t.Fatalf("Purge returned error: %v", err)
	}
	if n != nil {
		t.Fatalf("expected nil (unknown) count for a non-JSON body, got %d", *n)
	}
}

func TestPurgeReturnsNilCountOnEmptyBody(t *testing.T) {
	srv := statusServer(http.StatusOK)
	defer srv.Close()

	n, err := Purge(context.Background(), srv.Client(), srv.URL, "")
	if err != nil {
		t.Fatalf("Purge returned error: %v", err)
	}
	if n != nil {
		t.Fatalf("expected nil (unknown) count for an empty body, got %d", *n)
	}
}

// --- host override ---
//
// See fetch's doc comment in cache.go for the underlying reason this
// exists: Varnish hashes Host into the cache key, so a broadcast PURGE only
// invalidates objects cached under the exact Host it carries.

func TestPurgeSendsExplicitHostWhenGiven(t *testing.T) {
	var gotHost string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if _, err := Purge(context.Background(), srv.Client(), srv.URL, "pinned-host.example"); err != nil {
		t.Fatalf("Purge returned error: %v", err)
	}
	if gotHost != "pinned-host.example" {
		t.Fatalf("got Host %q, want %q", gotHost, "pinned-host.example")
	}
}

func TestPurgeLeavesHostAloneWhenNotGiven(t *testing.T) {
	var gotHost string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if _, err := Purge(context.Background(), srv.Client(), srv.URL, ""); err != nil {
		t.Fatalf("Purge returned error: %v", err)
	}
	// srv.URL is http://127.0.0.1:<port>; an empty host override must leave
	// Go's default (derived from the dialed address) untouched.
	if gotHost == "" || gotHost == "pinned-host.example" {
		t.Fatalf("expected the default dialed-address Host, got %q", gotHost)
	}
}
