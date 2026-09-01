package main

import (
	"strings"
	"testing"
)

// These tests exist because cmd/vinylprobe previously had none at all: the
// pass/fail decision that makes #103's nil-vs-known-zero distinction real —
// "the operator did not report a count" must never quietly satisfy
// -expect-purged, not even -expect-purged 0 — lived only in runPurge, and
// nothing but a full chainsaw run against a real cluster would have noticed
// a regression there. decidePurge pulls that decision out as a pure
// function (no HTTP, no os.Exit) specifically so it has a unit-level
// falsification, not just an E2E one.

func TestDecidePurge_NoExpectation_AlwaysExitsZero(t *testing.T) {
	cases := []struct {
		name string
		n    *int
	}{
		{"unknown count", nil},
		{"known zero", new(0)},
		{"known nonzero", new(3)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := probeFlags{url: "http://example/x"} // expectPurgedSet left false
			v := decidePurge(c.n, f)
			if v.exitCode != 0 {
				t.Fatalf("exitCode = %d, want 0 (no -expect-purged means always pass): message %q", v.exitCode, v.message)
			}
		})
	}
}

// TestDecidePurge_UnknownNeverSatisfiesExpectPurged is the falsification
// target named in review: an unknown count must fail -expect-purged for
// every requested value, including 0 — the one value a careless comparison
// (e.g. treating nil as the zero value of *int) could make pass by
// accident.
func TestDecidePurge_UnknownNeverSatisfiesExpectPurged(t *testing.T) {
	for _, want := range []int{0, 1, 3} {
		f := probeFlags{url: "http://example/x", expectPurgedSet: true, expectPurged: want}
		v := decidePurge(nil, f)
		if v.exitCode == 0 {
			t.Fatalf("-expect-purged %d: unknown count must not pass, got exitCode 0, message %q", want, v.message)
		}
		if !strings.Contains(v.message, "did not report") {
			t.Fatalf("-expect-purged %d: message should explain the count was unknown, got %q", want, v.message)
		}
	}
}

func TestDecidePurge_MatchingCountPasses(t *testing.T) {
	cases := []int{0, 1, 3}
	for _, n := range cases {
		f := probeFlags{url: "http://example/x", expectPurgedSet: true, expectPurged: n}
		v := decidePurge(new(n), f)
		if v.exitCode != 0 {
			t.Fatalf("-expect-purged %d with actual %d: want exitCode 0, got %d, message %q", n, n, v.exitCode, v.message)
		}
		if !strings.Contains(v.message, "OK:") {
			t.Fatalf("-expect-purged %d: expected an OK message, got %q", n, v.message)
		}
	}
}

func TestDecidePurge_MismatchingCountFails(t *testing.T) {
	f := probeFlags{url: "http://example/x", expectPurgedSet: true, expectPurged: 3}
	v := decidePurge(new(2), f)
	if v.exitCode != 1 {
		t.Fatalf("exitCode = %d, want 1 (assertion failure convention shared with runCheck/runDetect)", v.exitCode)
	}
	if !strings.Contains(v.message, "purged 2 objects, want 3") {
		t.Fatalf("message should state both the actual and expected count, got %q", v.message)
	}
}

func TestDecidePurge_ExitCodesMatchWhatRunPurgeActsOn(t *testing.T) {
	// runPurge (see main.go) treats exitCode == 0 as "print and fall
	// through" and any non-zero exitCode as "print and os.Exit(code)".
	// chainsaw scripts (e.g. e2e/tests/shard-routing) rely on that: a
	// script step running vinylprobe -purge -expect-purged N fails the
	// step, via `set -eu`, exactly when this exit code is non-zero.
	pass := decidePurge(new(3), probeFlags{expectPurgedSet: true, expectPurged: 3})
	if pass.exitCode != 0 {
		t.Fatalf("a satisfied expectation must exit 0, got %d", pass.exitCode)
	}
	fail := decidePurge(nil, probeFlags{expectPurgedSet: true, expectPurged: 3})
	if fail.exitCode != 1 {
		// Not 2: main.go reserves that for transport/protocol errors, a
		// different failure class from an unsatisfied assertion.
		t.Fatalf("an unsatisfied expectation must exit 1, got %d", fail.exitCode)
	}
}
