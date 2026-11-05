package probe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestWaitStatus_MatchesImmediately(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	got, err := WaitStatus(context.Background(), srv.Client(), srv.URL, "", 503)
	if err != nil || got != 503 {
		t.Fatalf("got %d, %v; want 503, nil", got, err)
	}
}

func TestWaitStatus_PollsUntilStatusChanges(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if n.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	got, err := WaitStatus(ctx, srv.Client(), srv.URL, "", 200)
	if err != nil || got != 200 {
		t.Fatalf("got %d, %v; want 200, nil", got, err)
	}
}

func TestWaitStatus_DeadlineReturnsLastStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	got, err := WaitStatus(ctx, srv.Client(), srv.URL, "", 200)
	if err != nil || got != 503 {
		t.Fatalf("got %d, %v; want 503, nil (caller decides pass/fail)", got, err)
	}
}

func TestWaitStatus_SendsHost(t *testing.T) {
	var host atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host.Store(r.Host)
	}))
	defer srv.Close()
	if _, err := WaitStatus(context.Background(), srv.Client(), srv.URL, "www.example.test", 200); err != nil {
		t.Fatal(err)
	}
	if host.Load() != "www.example.test" {
		t.Fatalf("host = %v", host.Load())
	}
}
