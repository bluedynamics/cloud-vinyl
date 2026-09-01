package probe

import (
	"encoding/json"
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
