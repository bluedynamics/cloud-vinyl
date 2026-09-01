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

// TestHTTPBroadcaster_SetsHostHeader is the receiver-side check for #101: the
// bug was that the outbound PURGE carried no Host at all, so Varnish hashed
// it against the pod's own address instead of the cache's real hostname.
//
// This asserts on what a real HTTP server actually observed on the wire
// (net/http parses the request line + Host header into r.Host itself, on the
// receiving side of a real TCP connection) rather than on the
// BroadcastRequest map the producer built. A map-shaped assertion would have
// passed even with the old bug, because the bug was specifically that Host
// never made it into any map/header at all — asserting "the map contains
// what we put in the map" cannot catch "we forgot to put Host anywhere".
func TestHTTPBroadcaster_SetsHostHeader(t *testing.T) {
	var gotHost string
	var gotHostHeaderValues []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		gotHostHeaderValues = r.Header["Host"]
		w.WriteHeader(http.StatusOK)
	}))
	defer s.Close()

	b := NewHTTPBroadcaster(5 * time.Second)
	pods := []string{podAddr(s.URL)}
	req := BroadcastRequest{
		Method: "PURGE",
		Path:   "/product/123",
		Host:   "my-cache.example.com",
	}

	result := b.Broadcast(context.Background(), pods, req)

	require.Equal(t, "ok", result.Status)
	assert.Equal(t, "my-cache.example.com", gotHost,
		"the receiving HTTP server must see the real cache hostname as Host, "+
			"not the pod's own address — otherwise Varnish hashes the purge "+
			"against a URL that was never cached")
	// Go's server never surfaces Host via r.Header; guard against a future
	// "fix" that sets it as a regular header instead of httpReq.Host, which
	// would look right in a map-shaped test but not survive an actual
	// HTTP/1.1 request line + Host: header on the wire the way r.Host does.
	assert.Empty(t, gotHostHeaderValues,
		"Host must be carried as the request's Host, not as a regular header")
}

// TestHTTPBroadcaster_EmptyHost_LeavesDefaultHost confirms the zero-value
// BroadcastRequest.Host (e.g. from a caller that never populates it) does not
// break the request — Go falls back to its usual default (the dial address)
// rather than sending an empty Host line.
func TestHTTPBroadcaster_EmptyHost_LeavesDefaultHost(t *testing.T) {
	var gotHost string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		w.WriteHeader(http.StatusOK)
	}))
	defer s.Close()

	b := NewHTTPBroadcaster(5 * time.Second)
	pods := []string{podAddr(s.URL)}
	req := BroadcastRequest{Method: "PURGE", Path: "/"}

	result := b.Broadcast(context.Background(), pods, req)

	require.Equal(t, "ok", result.Status)
	assert.NotEmpty(t, gotHost, "an empty BroadcastRequest.Host must not produce an empty Host header")
}

// ---------- objects-purged (#103) ----------
//
// vcl_synth copies the purge count onto an X-Vinyl-Purged response header
// (see internal/generator/templates/vcl_synth.vcl.tmpl). These tests drive
// a real httptest.Server that sets (or withholds) that header, so they
// observe what callPod actually reads off the wire — a hand-built PodResult
// would prove nothing about whether the header-parsing code path exists at
// all, which is exactly the shape of gap that let #93/#94/#95/#101 hide.

// purgeHandler returns a handler that answers 200 OK and, if header is
// non-empty, sets X-Vinyl-Purged to it.
func purgeHandler(header string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if header != "" {
			w.Header().Set("X-Vinyl-Purged", header)
		}
		w.WriteHeader(http.StatusOK)
	}
}

func TestHTTPBroadcaster_ObjectsPurged_ParsesHeader(t *testing.T) {
	s := httptest.NewServer(purgeHandler("3"))
	defer s.Close()

	b := NewHTTPBroadcaster(5 * time.Second)
	pods := []string{podAddr(s.URL)}
	req := BroadcastRequest{Method: "PURGE", Path: "/product/123"}

	result := b.Broadcast(context.Background(), pods, req)

	require.Len(t, result.Results, 1)
	require.NotNil(t, result.Results[0].ObjectsPurged,
		"a well-formed X-Vinyl-Purged header must parse to a known count")
	assert.Equal(t, 3, *result.Results[0].ObjectsPurged)
	require.NotNil(t, result.ObjectsPurged, "the aggregate must be known when every pod reported a count")
	assert.Equal(t, 3, *result.ObjectsPurged)
}

func TestHTTPBroadcaster_ObjectsPurged_MissingHeader_IsUnknownNotZero(t *testing.T) {
	// No X-Vinyl-Purged header at all — e.g. an older varnishd, or a
	// regression that drops the header. This must surface as "unknown", not
	// silently collapse to 0: 0 is a legitimate, common outcome (#92
	// sharding means most pods legitimately purge nothing), so a missing
	// header reported as 0 would be indistinguishable from a healthy purge —
	// recreating the exact blindness #103 exists to remove.
	s := httptest.NewServer(purgeHandler(""))
	defer s.Close()

	b := NewHTTPBroadcaster(5 * time.Second)
	pods := []string{podAddr(s.URL)}
	req := BroadcastRequest{Method: "PURGE", Path: "/product/123"}

	result := b.Broadcast(context.Background(), pods, req)

	require.Len(t, result.Results, 1)
	assert.Nil(t, result.Results[0].ObjectsPurged,
		"a missing header must leave ObjectsPurged nil (unknown), not 0")
	assert.Nil(t, result.ObjectsPurged,
		"the aggregate must be unknown, not 0, when the only pod's count is unknown")
}

func TestHTTPBroadcaster_ObjectsPurged_MalformedHeader_IsUnknown(t *testing.T) {
	s := httptest.NewServer(purgeHandler("not-a-number"))
	defer s.Close()

	b := NewHTTPBroadcaster(5 * time.Second)
	pods := []string{podAddr(s.URL)}
	req := BroadcastRequest{Method: "PURGE", Path: "/product/123"}

	result := b.Broadcast(context.Background(), pods, req)

	require.Len(t, result.Results, 1)
	assert.Nil(t, result.Results[0].ObjectsPurged,
		"an unparseable header must not fail the purge or fabricate a count — it must leave ObjectsPurged nil")
	assert.Equal(t, http.StatusOK, result.Results[0].Status,
		"a malformed count header must not turn an otherwise-successful purge into a failure")
}

func TestHTTPBroadcaster_ObjectsPurged_NegativeHeader_IsUnknown(t *testing.T) {
	// Varnish never returns a negative count; a negative value here can only
	// mean something upstream is malformed. Treat it the same as unparseable
	// rather than propagating a nonsensical count.
	s := httptest.NewServer(purgeHandler("-1"))
	defer s.Close()

	b := NewHTTPBroadcaster(5 * time.Second)
	pods := []string{podAddr(s.URL)}
	req := BroadcastRequest{Method: "PURGE", Path: "/product/123"}

	result := b.Broadcast(context.Background(), pods, req)

	require.Len(t, result.Results, 1)
	assert.Nil(t, result.Results[0].ObjectsPurged)
}

func TestHTTPBroadcaster_ObjectsPurged_AggregatesAcrossPods(t *testing.T) {
	s1 := httptest.NewServer(purgeHandler("3"))
	s2 := httptest.NewServer(purgeHandler("0"))
	s3 := httptest.NewServer(purgeHandler("5"))
	defer s1.Close()
	defer s2.Close()
	defer s3.Close()

	b := NewHTTPBroadcaster(5 * time.Second)
	pods := []string{podAddr(s1.URL), podAddr(s2.URL), podAddr(s3.URL)}
	req := BroadcastRequest{Method: "PURGE", Path: "/product/123"}

	result := b.Broadcast(context.Background(), pods, req)

	require.NotNil(t, result.ObjectsPurged)
	// #92: after sharding a URL lives on one owner pod, so most pods
	// legitimately purge 0 — the aggregate must still be their sum (8), not
	// treat any individual 0 as an error or as unknown.
	assert.Equal(t, 8, *result.ObjectsPurged)
}

func TestHTTPBroadcaster_ObjectsPurged_AllZero_IsKnownZero_NotUnknown(t *testing.T) {
	// Every pod explicitly reported 0 (a well-formed header, value "0") —
	// this is the expected common case post-#92 and must read as a known
	// zero, distinct from no pod reporting anything at all.
	s1 := httptest.NewServer(purgeHandler("0"))
	s2 := httptest.NewServer(purgeHandler("0"))
	defer s1.Close()
	defer s2.Close()

	b := NewHTTPBroadcaster(5 * time.Second)
	pods := []string{podAddr(s1.URL), podAddr(s2.URL)}
	req := BroadcastRequest{Method: "PURGE", Path: "/product/123"}

	result := b.Broadcast(context.Background(), pods, req)

	require.NotNil(t, result.ObjectsPurged, "an explicit all-zero result is known, not unknown")
	assert.Equal(t, 0, *result.ObjectsPurged)
}

func TestHTTPBroadcaster_ObjectsPurged_MixedKnownAndUnknown_SumsKnownOnes(t *testing.T) {
	// One pod reports a count, the other doesn't (e.g. mixed varnishd
	// versions during a rollout). The aggregate reflects what is known
	// rather than collapsing the whole broadcast to "unknown" because of one
	// pod — a partial signal is still more useful than none, and Total/
	// Succeeded already track how many pods actually answered.
	s1 := httptest.NewServer(purgeHandler("4"))
	s2 := httptest.NewServer(purgeHandler(""))
	defer s1.Close()
	defer s2.Close()

	b := NewHTTPBroadcaster(5 * time.Second)
	pods := []string{podAddr(s1.URL), podAddr(s2.URL)}
	req := BroadcastRequest{Method: "PURGE", Path: "/product/123"}

	result := b.Broadcast(context.Background(), pods, req)

	require.NotNil(t, result.ObjectsPurged)
	assert.Equal(t, 4, *result.ObjectsPurged)
}

func TestHTTPBroadcaster_ObjectsPurged_PodError_IsUnknown(t *testing.T) {
	// A pod that never answers (network error) obviously never delivers a
	// header; confirm that failure path also leaves ObjectsPurged nil rather
	// than panicking or defaulting to 0.
	b := NewHTTPBroadcaster(50 * time.Millisecond)
	pods := []string{"127.0.0.1:1"} // nothing listens here
	req := BroadcastRequest{Method: "PURGE", Path: "/product/123"}

	result := b.Broadcast(context.Background(), pods, req)

	require.Len(t, result.Results, 1)
	assert.NotEmpty(t, result.Results[0].Error)
	assert.Nil(t, result.Results[0].ObjectsPurged)
	assert.Nil(t, result.ObjectsPurged)
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
