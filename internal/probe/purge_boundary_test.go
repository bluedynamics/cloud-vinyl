package probe

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/bluedynamics/cloud-vinyl/internal/proxy"
)

// purgeResponse (purge.go) deliberately does not import
// internal/proxy.BroadcastResult — internal/proxy pulls in k8s.io/*
// transitively, and cmd/vinylprobe must not (hack/check-e2e-boundary.sh
// enforces that). Nothing else ties the two shapes together, though: a
// field rename in BroadcastResult would silently desync purgeResponse's
// parsing, and the only symptom would be Purge reporting "unknown" forever,
// with no error anywhere.
//
// Importing internal/proxy here, from a _test.go file, does not violate the
// boundary: hack/check-e2e-boundary.sh runs `go list -deps ./cmd/vinylprobe`
// without `-test`, which resolves only the non-test build graph.
// internal/probe's own _test.go files are never part of what cmd/vinylprobe
// links, so this import is invisible to that check — confirmed by running
// hack/check-e2e-boundary.sh with this file present; it stays green.
func TestPurgeResponseParsesARealBroadcastResult(t *testing.T) {
	n := 3
	real := proxy.BroadcastResult{
		Status:        "ok",
		Total:         3,
		Succeeded:     3,
		ObjectsPurged: &n,
		Results:       []proxy.PodResult{},
	}

	b, err := json.Marshal(real)
	if err != nil {
		t.Fatalf("marshaling a real BroadcastResult: %v", err)
	}

	var parsed purgeResponse
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatalf("purgeResponse failed to parse a real BroadcastResult's JSON: %v", err)
	}
	if parsed.ObjectsPurged == nil {
		t.Fatal("purgeResponse.ObjectsPurged is nil after parsing a BroadcastResult with a known count — " +
			"the field name or tag has drifted between the two shapes")
	}
	if *parsed.ObjectsPurged != n {
		t.Fatalf("purgeResponse.ObjectsPurged = %d, want %d", *parsed.ObjectsPurged, n)
	}
}

// TestPurgeResponseOmitsObjectsPurgedTheSameWayBroadcastResultDoes confirms
// the "unknown, never a fabricated zero" contract (#103) survives the
// round trip too: a BroadcastResult with ObjectsPurged left nil must
// serialize with the field omitted (its `omitempty` tag), and purgeResponse
// must decode that back into nil, not 0.
func TestPurgeResponseOmitsObjectsPurgedTheSameWayBroadcastResultDoes(t *testing.T) {
	real := proxy.BroadcastResult{
		Status:    "ok",
		Total:     3,
		Succeeded: 3,
		Results:   []proxy.PodResult{}, // ObjectsPurged left nil: unknown
	}

	b, err := json.Marshal(real)
	if err != nil {
		t.Fatalf("marshaling a real BroadcastResult: %v", err)
	}

	var parsed purgeResponse
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatalf("purgeResponse failed to parse a real BroadcastResult's JSON: %v", err)
	}
	if parsed.ObjectsPurged != nil {
		t.Fatalf("purgeResponse.ObjectsPurged = %d, want nil (unknown) for an omitted field", *parsed.ObjectsPurged)
	}
}

// recordingBroadcaster is a proxy.Broadcaster that records every request the
// real proxy handler builds and answers each with one pod reporting one
// purged object.
type recordingBroadcaster struct {
	mu   sync.Mutex
	reqs []proxy.BroadcastRequest
}

func (b *recordingBroadcaster) Broadcast(_ context.Context, pods []string, req proxy.BroadcastRequest) proxy.BroadcastResult {
	b.mu.Lock()
	b.reqs = append(b.reqs, req)
	b.mu.Unlock()
	one := 1
	return proxy.BroadcastResult{
		Status:        "ok",
		Total:         len(pods),
		Succeeded:     len(pods),
		ObjectsPurged: &one,
		Results:       []proxy.PodResult{{Pod: pods[0], Status: 200, ObjectsPurged: &one}},
	}
}

// TestPurgeXkeyRequestIsWhatTheRealProxyDecodes sends PurgeXkey's request to
// the real invalidation proxy handler. internal/proxy's xkeyRequest is
// unexported, so the JSON body shape cannot be compared field by field the
// way purgeResponse is compared with BroadcastResult above. A drift (say
// "key" instead of "keys") would otherwise surface only in E2E, as a 400.
func TestPurgeXkeyRequestIsWhatTheRealProxyDecodes(t *testing.T) {
	rec := &recordingBroadcaster{}
	pods := proxy.NewPodMap()
	pods.Update("ns", "cache", []string{"10.0.0.1"})
	// StaticRouter strips the port, so the httptest listener's host routes.
	router := proxy.NewStaticRouter(map[string][2]string{"127.0.0.1": {"ns", "cache"}})
	ts := httptest.NewServer(proxy.NewServer(":0", router, pods, rec, nil))
	defer ts.Close()

	n, err := PurgeXkey(context.Background(), ts.Client(), ts.URL+"/purge/xkey", []string{"k1", "k2"})
	if err != nil {
		t.Fatalf("PurgeXkey against the real proxy handler: %v", err)
	}
	if n == nil || *n != 2 {
		t.Fatalf("got count %v, want 2 (one object per key)", n)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.reqs) != 2 {
		t.Fatalf("proxy broadcast %d requests, want 2 (one per key)", len(rec.reqs))
	}
	for i, want := range []string{"k1", "k2"} {
		r := rec.reqs[i]
		if r.Method != "PURGE" || r.Path != "/" || r.Headers["X-Xkey-Purge"] != want {
			t.Fatalf("broadcast %d = %s %s X-Xkey-Purge=%q, want PURGE / X-Xkey-Purge=%q",
				i, r.Method, r.Path, r.Headers["X-Xkey-Purge"], want)
		}
	}
}
