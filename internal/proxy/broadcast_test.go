package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeHandler returns the given HTTP status code for any request.
func fakeHandler(statusCode int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(statusCode)
	}
}

// podAddr strips the "http://" scheme from a test-server URL so it can be
// used as the pod address (host:port).
func podAddr(url string) string {
	return strings.TrimPrefix(url, "http://")
}

func TestHTTPBroadcaster_AllSucceed(t *testing.T) {
	s1 := httptest.NewServer(fakeHandler(http.StatusOK))
	s2 := httptest.NewServer(fakeHandler(http.StatusOK))
	s3 := httptest.NewServer(fakeHandler(http.StatusOK))
	defer s1.Close()
	defer s2.Close()
	defer s3.Close()

	b := NewHTTPBroadcaster(5 * time.Second)
	pods := []string{podAddr(s1.URL), podAddr(s2.URL), podAddr(s3.URL)}
	req := BroadcastRequest{Method: "PURGE", Path: "/product/123"}

	result := b.Broadcast(context.Background(), pods, req)

	assert.Equal(t, "ok", result.Status)
	assert.Equal(t, 3, result.Total)
	assert.Equal(t, 3, result.Succeeded)
	require.Len(t, result.Results, 3)
	for _, r := range result.Results {
		assert.Empty(t, r.Error)
		assert.Equal(t, http.StatusOK, r.Status)
	}
}

func TestHTTPBroadcaster_PartialSuccess(t *testing.T) {
	s1 := httptest.NewServer(fakeHandler(http.StatusOK))
	s2 := httptest.NewServer(fakeHandler(http.StatusOK))
	s3 := httptest.NewServer(fakeHandler(http.StatusInternalServerError))
	defer s1.Close()
	defer s2.Close()
	defer s3.Close()

	b := NewHTTPBroadcaster(5 * time.Second)
	pods := []string{podAddr(s1.URL), podAddr(s2.URL), podAddr(s3.URL)}
	req := BroadcastRequest{Method: "PURGE", Path: "/product/123"}

	result := b.Broadcast(context.Background(), pods, req)

	assert.Equal(t, "partial", result.Status)
	assert.Equal(t, 3, result.Total)
	assert.Equal(t, 2, result.Succeeded)
}

func TestHTTPBroadcaster_AllFail(t *testing.T) {
	s1 := httptest.NewServer(fakeHandler(http.StatusInternalServerError))
	s2 := httptest.NewServer(fakeHandler(http.StatusInternalServerError))
	s3 := httptest.NewServer(fakeHandler(http.StatusInternalServerError))
	defer s1.Close()
	defer s2.Close()
	defer s3.Close()

	b := NewHTTPBroadcaster(5 * time.Second)
	pods := []string{podAddr(s1.URL), podAddr(s2.URL), podAddr(s3.URL)}
	req := BroadcastRequest{Method: "PURGE", Path: "/product/123"}

	result := b.Broadcast(context.Background(), pods, req)

	assert.Equal(t, "failed", result.Status)
	assert.Equal(t, 3, result.Total)
	assert.Equal(t, 0, result.Succeeded)
}

func TestHTTPBroadcaster_Timeout(t *testing.T) {
	// Pod that blocks until the request context is cancelled.
	s1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
		// Write nothing — the client already timed out.
	}))
	s2 := httptest.NewServer(fakeHandler(http.StatusOK))
	defer s1.Close()
	defer s2.Close()

	b := NewHTTPBroadcaster(100 * time.Millisecond)
	pods := []string{podAddr(s1.URL), podAddr(s2.URL)}
	req := BroadcastRequest{Method: "PURGE", Path: "/product/123"}

	result := b.Broadcast(context.Background(), pods, req)

	// One pod timed out (failure), one succeeded → partial.
	assert.Equal(t, "partial", result.Status)
	assert.Equal(t, 2, result.Total)
	assert.Equal(t, 1, result.Succeeded)

	// Verify that the timed-out pod has an error.
	errorCount := 0
	for _, r := range result.Results {
		if r.Error != "" {
			errorCount++
		}
	}
	assert.Equal(t, 1, errorCount)
}

func TestHTTPBroadcaster_ParallelExecution(t *testing.T) {
	// Each of three pods delays 100 ms. Parallel execution should complete
	// in well under 200 ms (serial would take ≥300 ms).
	const delay = 100 * time.Millisecond

	var requestCount atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		time.Sleep(delay)
		w.WriteHeader(http.StatusOK)
	})

	s1 := httptest.NewServer(handler)
	s2 := httptest.NewServer(handler)
	s3 := httptest.NewServer(handler)
	defer s1.Close()
	defer s2.Close()
	defer s3.Close()

	b := NewHTTPBroadcaster(5 * time.Second)
	pods := []string{podAddr(s1.URL), podAddr(s2.URL), podAddr(s3.URL)}
	req := BroadcastRequest{Method: "PURGE", Path: "/"}

	start := time.Now()
	result := b.Broadcast(context.Background(), pods, req)
	elapsed := time.Since(start)

	assert.Equal(t, "ok", result.Status)
	assert.Equal(t, int32(3), requestCount.Load())
	assert.Less(t, elapsed, 200*time.Millisecond,
		"parallel execution of 3×100ms pods should finish in <200ms, got %s", elapsed)
}

func TestStatusString(t *testing.T) {
	tests := []struct {
		total, succeeded int
		want             string
	}{
		{3, 3, "ok"},
		{0, 0, "ok"},
		{3, 0, "failed"},
		{3, 1, "partial"},
		{3, 2, "partial"},
	}
	for _, tc := range tests {
		got := statusString(tc.total, tc.succeeded)
		assert.Equal(t, tc.want, got, "statusString(%d,%d)", tc.total, tc.succeeded)
	}
}
