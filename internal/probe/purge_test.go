package probe

import (
	"context"
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

func TestPurgeSendsMethodPurgeNotGet(t *testing.T) {
	var gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := Purge(context.Background(), srv.Client(), srv.URL); err != nil {
		t.Fatalf("Purge returned error: %v", err)
	}
	if gotMethod != purgeMethod {
		t.Fatalf("got method %q, want %q", gotMethod, purgeMethod)
	}
}

func TestPurgeReturnsNilOnMatchedPurge(t *testing.T) {
	srv := statusServer(http.StatusOK)
	defer srv.Close()

	if err := Purge(context.Background(), srv.Client(), srv.URL); err != nil {
		t.Fatalf("Purge returned error on 200: %v", err)
	}
}

func TestPurgeReturnsNilOnNothingToPurge(t *testing.T) {
	srv := statusServer(http.StatusNotFound)
	defer srv.Close()

	if err := Purge(context.Background(), srv.Client(), srv.URL); err != nil {
		t.Fatalf("Purge returned error on 404: %v", err)
	}
}

func TestPurgeReturnsErrorOnMethodNotAllowed(t *testing.T) {
	srv := statusServer(http.StatusMethodNotAllowed)
	defer srv.Close()

	err := Purge(context.Background(), srv.Client(), srv.URL)
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

	err := Purge(context.Background(), srv.Client(), srv.URL)
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

	if err := Purge(ctx, srv.Client(), srv.URL); err == nil {
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

	if err := Purge(ctx, srv.Client(), srv.URL); err == nil {
		t.Fatal("expected an error when the context expires, got nil")
	}
}
