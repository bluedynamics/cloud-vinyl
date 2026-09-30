package probe

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cachingServer answers every request with the first X-Probe value it ever saw,
// which is what a cache in front of an echoing backend looks like.
func cachingServer() *httptest.Server {
	var first string
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if first == "" {
			first = r.Header.Get("X-Probe")
		}
		fmt.Fprintf(w, `{"headers":{"x-probe":%q}}`, first)
	}))
}

// echoingServer answers with the current X-Probe value, which is what an
// uncached path looks like.
func echoingServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"headers":{"x-probe":%q}}`, r.Header.Get("X-Probe"))
	}))
}

func TestDetectReportsHitWhenSecondResponseCarriesTheFirstToken(t *testing.T) {
	srv := cachingServer()
	defer srv.Close()

	got, err := Detect(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("Detect returned error: %v", err)
	}
	if got != Hit {
		t.Fatalf("got %v, want hit", got)
	}
}

func TestDetectReportsMissWhenEachResponseCarriesItsOwnToken(t *testing.T) {
	srv := echoingServer()
	defer srv.Close()

	got, err := Detect(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("Detect returned error: %v", err)
	}
	if got != Miss {
		t.Fatalf("got %v, want miss", got)
	}
}

func TestDetectErrorsWhenTheBackendDoesNotEchoTheHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "no probe header here")
	}))
	defer srv.Close()

	_, err := Detect(context.Background(), srv.Client(), srv.URL)
	if err == nil {
		t.Fatal("expected an error when the backend echoes nothing, got nil")
	}
	if !strings.Contains(err.Error(), "did not echo") {
		t.Fatalf("error should explain the cause, got: %v", err)
	}
}

func TestDetectUsesDistinctTokensPerCall(t *testing.T) {
	seen := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.Header.Get("X-Probe")] = true
		fmt.Fprintf(w, `{"headers":{"x-probe":%q}}`, r.Header.Get("X-Probe"))
	}))
	defer srv.Close()

	if _, err := Detect(context.Background(), srv.Client(), srv.URL); err != nil {
		t.Fatalf("first Detect: %v", err)
	}
	if _, err := Detect(context.Background(), srv.Client(), srv.URL); err != nil {
		t.Fatalf("second Detect: %v", err)
	}
	// Two calls, two requests each, all tokens distinct: otherwise a second
	// Detect against a warm cache could not tell hit from miss.
	if len(seen) != 4 {
		t.Fatalf("expected 4 distinct probe tokens across two calls, got %d", len(seen))
	}
}

func TestDetectRespectsContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if _, err := Detect(ctx, srv.Client(), srv.URL); err == nil {
		t.Fatal("expected an error when the context expires, got nil")
	}
}

// roundTripperWithHeader injects a fixed header into every request before
// delegating, standing in for a real client's transport-level header (e.g.
// one that would carry a VCL-minted traceparent) without needing one.
type roundTripperWithHeader struct {
	base     http.RoundTripper
	key, val string
}

func (rt roundTripperWithHeader) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set(rt.key, rt.val)
	base := rt.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(r)
}

func TestBodyCapture_ExtractsGroupFromEchoedBody(t *testing.T) {
	// The handler reflects a request header into a JSON body the way the
	// real echo backend does (lowercased names, documented in the E2E
	// design spec as `"x-probe":"<value>"`).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"headers":{"traceparent":%q}}`, r.Header.Get("traceparent"))
	}))
	defer srv.Close()

	c := &http.Client{Transport: roundTripperWithHeader{
		key: "traceparent", val: "00-abc-def-01"}}
	got, matched, err := BodyCapture(context.Background(), c, srv.URL, "",
		`"traceparent":"(00-[0-9a-f-]+-01)"`)
	require.NoError(t, err)
	require.True(t, matched)
	assert.Equal(t, "00-abc-def-01", got)
}

func TestBodyCapture_NoMatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"headers":{}}`)
	}))
	defer srv.Close()
	_, matched, err := BodyCapture(context.Background(), srv.Client(), srv.URL, "",
		`"traceparent":"(00-[0-9a-f-]+)"`)
	require.NoError(t, err)
	assert.False(t, matched)
}

func TestBodyCapture_RegexNeedsCaptureGroup(t *testing.T) {
	_, _, err := BodyCapture(context.Background(), http.DefaultClient,
		"http://unused.invalid", "", `no-group-here`)
	require.Error(t, err)
}
