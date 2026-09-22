package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bluedynamics/cloud-vinyl/internal/probe"
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

// decideSpans is the -assert-spans analogue of decidePurge: the pass/fail
// rule pulled out as a pure function so it has a unit-level falsification
// instead of resting on a full E2E run against a real tracer.

func TestDecideSpans_SatisfiedPasses(t *testing.T) {
	spans := []probe.SpanSummary{
		{Name: "varnish request", Attrs: map[string]string{"varnish.handling": "hit"}},
		{Name: "varnish request", Attrs: map[string]string{"varnish.handling": "miss"}},
	}
	v := decideSpans(spans, "varnish request", map[string]string{"varnish.handling": "hit"}, 1)
	if !v.satisfied {
		t.Fatalf("want satisfied, got %+v", v)
	}
}

func TestDecideSpans_CountShortfallNotSatisfied(t *testing.T) {
	spans := []probe.SpanSummary{{Name: "varnish request", Attrs: map[string]string{}}}
	v := decideSpans(spans, "varnish request", nil, 2)
	if v.satisfied {
		t.Fatal("one span must not satisfy min-count 2")
	}
}

func TestDecideSpans_AttrMismatchNotCounted(t *testing.T) {
	spans := []probe.SpanSummary{
		{Name: "varnish request", Attrs: map[string]string{"varnish.handling": "miss"}},
	}
	v := decideSpans(spans, "varnish request", map[string]string{"varnish.handling": "hit"}, 1)
	if v.satisfied {
		t.Fatal("attr mismatch must not count")
	}
}

func TestDecideSpans_NameMismatchNotCounted(t *testing.T) {
	spans := []probe.SpanSummary{{Name: "other span", Attrs: map[string]string{}}}
	v := decideSpans(spans, "varnish request", nil, 1)
	if v.satisfied {
		t.Fatal("a span with a different name must not count")
	}
	if v.matched != 0 {
		t.Fatalf("matched = %d, want 0", v.matched)
	}
}

func TestDecideSpans_MultipleAttrsAllMustMatch(t *testing.T) {
	spans := []probe.SpanSummary{
		{Name: "varnish request", Attrs: map[string]string{"varnish.handling": "hit", "http.status_code": "200"}},
		{Name: "varnish request", Attrs: map[string]string{"varnish.handling": "hit", "http.status_code": "500"}},
	}
	v := decideSpans(spans, "varnish request",
		map[string]string{"varnish.handling": "hit", "http.status_code": "200"}, 1)
	if !v.satisfied {
		t.Fatalf("want satisfied, got %+v", v)
	}
	if v.matched != 1 {
		t.Fatalf("matched = %d, want 1 (only one span satisfies both attrs)", v.matched)
	}
}

// validate() tests for the two new modes: -otlp-sink and -assert-spans are
// disjoint from -url/-purge/-seed/-check, and from each other.

func TestValidate_OTLPSinkAndAssertSpansMutuallyExclusive(t *testing.T) {
	f := probeFlags{otlpSink: ":4318", assertSpans: true, sink: "http://x", spanName: "y"}
	if err := f.validate(); err == nil {
		t.Fatal("want error: -otlp-sink and -assert-spans are mutually exclusive")
	}
}

func TestValidate_OTLPSinkRejectsURL(t *testing.T) {
	f := probeFlags{otlpSink: ":4318", url: "http://example/x"}
	if err := f.validate(); err == nil {
		t.Fatal("want error: -otlp-sink does not take -url")
	}
}

func TestValidate_OTLPSinkRejectsPurge(t *testing.T) {
	f := probeFlags{otlpSink: ":4318", purge: true}
	if err := f.validate(); err == nil {
		t.Fatal("want error: -otlp-sink is mutually exclusive with -purge")
	}
}

func TestValidate_OTLPSinkAloneIsValid(t *testing.T) {
	f := probeFlags{otlpSink: ":4318"}
	if err := f.validate(); err != nil {
		t.Fatalf("want no error, got %v", err)
	}
}

func TestValidate_AssertSpansRequiresSink(t *testing.T) {
	f := probeFlags{assertSpans: true, spanName: "varnish request"}
	if err := f.validate(); err == nil {
		t.Fatal("want error: -assert-spans requires -sink")
	}
}

func TestValidate_AssertSpansRequiresSpanName(t *testing.T) {
	f := probeFlags{assertSpans: true, sink: "http://otlp-sink:4318"}
	if err := f.validate(); err == nil {
		t.Fatal("want error: -assert-spans requires -span-name")
	}
}

func TestValidate_AssertSpansWithSinkAndSpanNameIsValid(t *testing.T) {
	f := probeFlags{assertSpans: true, sink: "http://otlp-sink:4318", spanName: "varnish request"}
	if err := f.validate(); err != nil {
		t.Fatalf("want no error, got %v", err)
	}
}

func TestValidate_AssertSpansRejectsCheck(t *testing.T) {
	f := probeFlags{assertSpans: true, sink: "http://x", spanName: "y", check: "tok"}
	if err := f.validate(); err == nil {
		t.Fatal("want error: -assert-spans is mutually exclusive with -check")
	}
}

func TestValidate_PlainURLModeStillRequiresURL(t *testing.T) {
	f := probeFlags{}
	if err := f.validate(); err == nil {
		t.Fatal("want error: -url is required when no other mode is selected")
	}
}

// fetchSpans polls the sink's GET /spans endpoint; these tests exercise it
// directly against an httptest server rather than through runAssertSpans's
// polling loop, which sleeps and os.Exits.

func TestFetchSpans_DecodesJSONList(t *testing.T) {
	want := []probe.SpanSummary{{Name: "varnish request", TraceID: "aa", Attrs: map[string]string{"k": "v"}}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/spans" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(want)
	}))
	defer srv.Close()

	got, err := fetchSpans(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetchSpans: %v", err)
	}
	if len(got) != 1 || got[0].Name != "varnish request" || got[0].Attrs["k"] != "v" {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestFetchSpans_NonOKStatusIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := fetchSpans(context.Background(), srv.URL); err == nil {
		t.Fatal("want error for a non-200 response")
	}
}

func TestFetchSpans_ContextTimeoutIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, err := fetchSpans(ctx, srv.URL); err == nil {
		t.Fatal("want error when the context deadline is exceeded")
	}
}
