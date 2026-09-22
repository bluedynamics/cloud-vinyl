// Command vinylprobe checks cache behaviour over HTTP from inside the cluster.
//
// It must not import k8s.io/*: chainsaw owns Kubernetes state, this owns HTTP.
// hack/check-e2e-boundary.sh enforces that.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/bluedynamics/cloud-vinyl/internal/probe"
)

// Flag help strings long enough to trip lll (golangci-lint's 120-char line
// limit, which applies to cmd/* but not internal/*) are declared here,
// wrapped, rather than shortened into uselessness.
const (
	expectHelp = `expected outcome: "hit" or "miss" ` +
		`(Detect mode only; ignored with -purge, -seed, -check)`
	expectStateHelp = `expected state for -check: "cached" or "not-cached" ` +
		`(required with -check)`
	seedHelp = "issue a single GET to -url with a fresh token, populate the cache, " +
		"and print the token to stdout"
	expectPurgedHelp = "expected objectsPurged count after -purge (requires -purge). " +
		"The operator reporting no count at all is always a failure, never treated as 0."
	hostHelp = "override the HTTP Host header sent, independent of the address -url dials " +
		"(requires -seed, -check, or -purge). Needed because Varnish hashes Host into the " +
		"cache key: addressing pods individually by their own StatefulSet DNS name (to force " +
		"which pod handles a request) gives each pod a different Host, and so a different " +
		"cache key, unless -host pins them to one shared value. See fetch's doc comment in " +
		"internal/probe/cache.go."
	otlpSinkHelp = "listen address for an in-memory OTLP/http-protobuf trace sink " +
		"(serves POST /v1/traces, GET /spans, GET /healthz; runs until killed). " +
		"Mutually exclusive with -url, -purge, -seed, -check, and -assert-spans."
	assertSpansHelp = "poll -sink's /spans until a span matching -span-name (and every -attr) " +
		"appears -min-count times, or -within elapses. " +
		"Mutually exclusive with -url, -purge, -seed, -check, and -otlp-sink."
	sinkHelp     = "base URL of the OTLP sink to poll (requires -assert-spans)"
	spanNameHelp = "exact span name a matching span must carry (requires -assert-spans)"
	attrHelp     = "key=value attribute a matching span must carry; " +
		"may be repeated to require several attributes (only with -assert-spans)"
	minCountHelp = "minimum number of matching spans required to satisfy -assert-spans"
	withinHelp   = "deadline for -assert-spans to keep polling -sink before failing"
)

// probeFlags holds every flag plus which ones were explicitly passed
// (flag.Visit only tells you that once, at parse time, so it is captured
// here rather than re-derived later).
type probeFlags struct {
	url          string
	expect       string
	expectState  string
	check        string
	timeout      time.Duration
	purge        bool
	seed         bool
	expectPurged int
	host         string

	// otlp-sink and assert-spans mode flags (see otlpSinkHelp/assertSpansHelp).
	otlpSink    string
	assertSpans bool
	sink        string
	spanName    string
	minCount    int
	attrs       map[string]string
	within      time.Duration

	expectSet       bool
	expectStateSet  bool
	expectPurgedSet bool
}

func parseFlags() probeFlags {
	url := flag.String("url", "", "URL to probe (required)")
	expect := flag.String("expect", "hit", expectHelp)
	expectState := flag.String("expect-state", "", expectStateHelp)
	check := flag.String("check", "", "seed token to check for at -url with a single request (requires -expect-state)")
	timeout := flag.Duration("timeout", 30*time.Second, "overall deadline")
	purge := flag.Bool("purge", false, "issue an HTTP PURGE for -url instead of detecting hit/miss")
	seed := flag.Bool("seed", false, seedHelp)
	expectPurged := flag.Int("expect-purged", 0, expectPurgedHelp)
	host := flag.String("host", "", hostHelp)

	otlpSink := flag.String("otlp-sink", "", otlpSinkHelp)
	assertSpans := flag.Bool("assert-spans", false, assertSpansHelp)
	sink := flag.String("sink", "", sinkHelp)
	spanName := flag.String("span-name", "", spanNameHelp)
	minCount := flag.Int("min-count", 1, minCountHelp)
	within := flag.Duration("within", 60*time.Second, withinHelp)
	var attrPairs []string
	flag.Func("attr", attrHelp, func(s string) error {
		if !strings.Contains(s, "=") {
			return fmt.Errorf("-attr %q: want key=value", s)
		}
		attrPairs = append(attrPairs, s)
		return nil
	})

	flag.Parse()

	attrs := map[string]string{}
	for _, p := range attrPairs {
		k, v, _ := strings.Cut(p, "=")
		attrs[k] = v
	}

	f := probeFlags{
		url:          *url,
		expect:       *expect,
		expectState:  *expectState,
		check:        *check,
		timeout:      *timeout,
		purge:        *purge,
		seed:         *seed,
		expectPurged: *expectPurged,
		host:         *host,
		otlpSink:     *otlpSink,
		assertSpans:  *assertSpans,
		sink:         *sink,
		spanName:     *spanName,
		minCount:     *minCount,
		attrs:        attrs,
		within:       *within,
	}
	flag.Visit(func(fl *flag.Flag) {
		switch fl.Name {
		case "expect":
			f.expectSet = true
		case "expect-state":
			f.expectStateSet = true
		case "expect-purged":
			f.expectPurgedSet = true
		}
	})
	return f
}

// validateSpanModes checks the -otlp-sink/-assert-spans flag combinations.
// They are two more modes, disjoint from the -url-based family validate
// checks below: neither takes -url. handled is true once one of the two was
// selected, whether or not the combination turned out valid (err carries
// that); handled is false to tell validate to fall through to its own
// -url-based checks unchanged. Split out from validate solely to keep that
// function's cyclomatic complexity under the repo's gocyclo limit.
func (f probeFlags) validateSpanModes() (handled bool, err error) {
	switch {
	case f.otlpSink != "" && f.assertSpans:
		return true, errors.New("-otlp-sink and -assert-spans are mutually exclusive")
	case f.otlpSink != "" && (f.url != "" || f.purge || f.seed || f.check != ""):
		return true, errors.New("-otlp-sink is mutually exclusive with -url, -purge, -seed, and -check")
	case f.assertSpans && (f.url != "" || f.purge || f.seed || f.check != ""):
		return true, errors.New("-assert-spans is mutually exclusive with -url, -purge, -seed, and -check")
	case f.assertSpans && f.sink == "":
		return true, errors.New("-assert-spans requires -sink")
	case f.assertSpans && f.spanName == "":
		return true, errors.New("-assert-spans requires -span-name")
	case f.assertSpans, f.otlpSink != "":
		return true, nil
	default:
		return false, nil
	}
}

// validate checks flag combinations that flag.Parse cannot express itself:
// mutually exclusive modes, and options that only make sense with one
// particular mode. It changes nothing; main exits on a non-nil result.
func (f probeFlags) validate() error {
	if handled, err := f.validateSpanModes(); handled {
		return err
	}

	if f.url == "" {
		return errors.New("-url is required")
	}

	// -purge, -seed and -check are three different, mutually exclusive modes;
	// anything left over falls through to the original Detect (-expect
	// hit|miss) mode.
	modes := 0
	for _, on := range []bool{f.purge, f.seed, f.check != ""} {
		if on {
			modes++
		}
	}
	switch {
	case modes > 1:
		return errors.New("-purge, -seed and -check are mutually exclusive")
	case (f.purge || f.seed) && f.expectSet:
		return errors.New("-expect has no effect with -purge or -seed; do not pass it")
	case f.check != "" && f.expectSet:
		return errors.New("-expect has no effect with -check; use -expect-state instead")
	case f.check == "" && f.expectStateSet:
		return errors.New("-expect-state only applies to -check")
	case !f.purge && f.expectPurgedSet:
		return errors.New("-expect-purged only applies to -purge")
	case f.expectPurgedSet && f.expectPurged < 0:
		return errors.New("-expect-purged must not be negative")
	case f.host != "" && !f.purge && !f.seed && f.check == "":
		return errors.New("-host only applies to -purge, -seed, or -check")
	}
	return nil
}

func main() {
	f := parseFlags()
	if err := f.validate(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}

	// -otlp-sink and -assert-spans do not observe -timeout the way the
	// -url-based modes do: the sink runs until killed, and -assert-spans
	// has its own -within deadline. Building a context.WithTimeout(f.timeout)
	// unconditionally here would cut an -assert-spans poll short at the
	// default 30s timeout regardless of -within, so it is deferred to those
	// two dispatch branches instead of shared across all modes.
	switch {
	case f.otlpSink != "":
		runOTLPSink(f)
		return
	case f.assertSpans:
		ctx, cancel := context.WithTimeout(context.Background(), f.within)
		defer cancel()
		runAssertSpans(ctx, f)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), f.timeout)
	defer cancel()

	client := &http.Client{}

	switch {
	case f.purge:
		runPurge(ctx, client, f)
	case f.seed:
		runSeed(ctx, client, f)
	case f.check != "":
		runCheck(ctx, client, f)
	default:
		runDetect(ctx, client, f)
	}
}

// purgeVerdict is the fully-decided outcome of a -purge call: whether it
// passes, what to print, and what process exit code that implies. Pulling
// this out of runPurge as a pure function — no HTTP, no os.Exit — is what
// lets the pass/fail decision be unit tested directly, rather than resting
// on a full chainsaw run against a cluster to notice a regression.
type purgeVerdict struct {
	// exitCode is 0 (pass, matches main's implicit success exit) or 1
	// (FAIL, matches runCheck/runDetect's assertion-failure convention).
	exitCode int
	message  string
}

// decidePurge is the exact decision #103 exists to make possible: n is nil
// when the response carried no parseable objectsPurged count — distinct
// from a known 0 — and that must never satisfy -expect-purged, no matter
// what count was asked for, including 0 itself. Collapsing "unknown" into a
// match here would silently undo the distinction the last three PRs went to
// trouble to preserve on the wire.
func decidePurge(n *int, f probeFlags) purgeVerdict {
	got := "unknown"
	if n != nil {
		got = fmt.Sprintf("%d", *n)
	}
	okMsg := fmt.Sprintf("OK: purged %s (objectsPurged=%s)", f.url, got)

	if !f.expectPurgedSet {
		return purgeVerdict{exitCode: 0, message: okMsg}
	}
	if n == nil {
		return purgeVerdict{
			exitCode: 1,
			message:  fmt.Sprintf("FAIL: %s purge did not report an objects-purged count, want %d", f.url, f.expectPurged),
		}
	}
	if *n != f.expectPurged {
		return purgeVerdict{
			exitCode: 1,
			message:  fmt.Sprintf("FAIL: %s purged %d objects, want %d", f.url, *n, f.expectPurged),
		}
	}
	return purgeVerdict{exitCode: 0, message: okMsg}
}

// runPurge issues -purge and applies decidePurge's verdict. Only the HTTP
// call and the process exit live here; the decision itself is in
// decidePurge so it can be tested without either.
func runPurge(ctx context.Context, client *http.Client, f probeFlags) {
	n, err := probe.Purge(ctx, client, f.url, f.host)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	v := decidePurge(n, f)
	fmt.Println(v.message)
	if v.exitCode != 0 {
		os.Exit(v.exitCode)
	}
}

func runSeed(ctx context.Context, client *http.Client, f probeFlags) {
	tok, err := probe.Seed(ctx, client, f.url, f.host)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	// Bare token on stdout: a chainsaw script step captures this into a
	// shell variable to pass to a later -check call.
	fmt.Println(tok)
}

func runCheck(ctx context.Context, client *http.Client, f probeFlags) {
	if !f.expectStateSet {
		fmt.Fprintln(os.Stderr, `error: -check requires -expect-state ("cached" or "not-cached")`)
		os.Exit(2)
	}
	var want probe.State
	switch f.expectState {
	case "cached":
		want = probe.Cached
	case "not-cached":
		want = probe.NotCached
	default:
		fmt.Fprintf(os.Stderr, "error: -expect-state must be \"cached\" or \"not-cached\", got %q\n", f.expectState)
		os.Exit(2)
	}
	got, err := probe.Check(ctx, client, f.url, f.check, f.host)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	if got != want {
		fmt.Printf("FAIL: %s expected %s, got %s\n", f.url, want, got)
		os.Exit(1)
	}
	fmt.Printf("OK: %s is %s\n", f.url, got)
}

func runDetect(ctx context.Context, client *http.Client, f probeFlags) {
	var want probe.Outcome
	switch f.expect {
	case "hit":
		want = probe.Hit
	case "miss":
		want = probe.Miss
	default:
		fmt.Fprintf(os.Stderr, "error: -expect must be \"hit\" or \"miss\", got %q\n", f.expect)
		os.Exit(2)
	}

	got, err := probe.Detect(ctx, client, f.url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	if got != want {
		fmt.Printf("FAIL: %s expected %s, got %s\n", f.url, want, got)
		os.Exit(1)
	}
	fmt.Printf("OK: %s is a %s\n", f.url, got)
}

// spanVerdict is decideSpans's fully-decided outcome: whether -assert-spans
// is satisfied, and how many spans matched (reported either way, so a
// timeout message can say how far short of -min-count it got).
type spanVerdict struct {
	satisfied bool
	matched   int
}

// decideSpans is pure so the pass/fail rule is unit-testable, the same
// decidePurge pattern used for -purge: a span counts when its name matches
// name and every entry in attrs equals the span's own attribute of that key
// (a missing attribute compares unequal to any wanted value, including "").
func decideSpans(spans []probe.SpanSummary, name string, attrs map[string]string, minCount int) spanVerdict {
	matched := 0
	for _, s := range spans {
		if s.Name != name {
			continue
		}
		ok := true
		for k, want := range attrs {
			if s.Attrs[k] != want {
				ok = false
				break
			}
		}
		if ok {
			matched++
		}
	}
	return spanVerdict{satisfied: matched >= minCount, matched: matched}
}

// runOTLPSink serves the in-memory OTLP sink until the process is killed or
// the listener fails. It deliberately ignores -timeout: the sink is meant to
// run for the lifetime of an E2E test, not one probe invocation's deadline.
func runOTLPSink(f probeFlags) {
	sink := probe.NewOTLPSink(4096)
	srv := &http.Server{
		Addr:              f.otlpSink,
		Handler:           sink.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	fmt.Printf("OK: otlp sink listening on %s\n", f.otlpSink)
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "sink: %v\n", err)
		os.Exit(2)
	}
}

// runAssertSpans polls -sink every 2s until decideSpans reports satisfied or
// -within elapses (ctx carries that same -within deadline for the individual
// HTTP requests fetchSpans makes; see main's dispatch for why -assert-spans
// does not share the -url-based modes' -timeout-scoped context).
func runAssertSpans(ctx context.Context, f probeFlags) {
	deadline := time.Now().Add(f.within)
	var last spanVerdict
	for time.Now().Before(deadline) {
		spans, err := fetchSpans(ctx, f.sink)
		if err == nil {
			last = decideSpans(spans, f.spanName, f.attrs, f.minCount)
			if last.satisfied {
				fmt.Printf("OK: %d span(s) named %q matched\n", last.matched, f.spanName)
				os.Exit(0)
			}
		}
		time.Sleep(2 * time.Second)
	}
	fmt.Printf("FAIL: only %d span(s) named %q matched within %s\n",
		last.matched, f.spanName, f.within)
	os.Exit(1)
}

// fetchSpans fetches and decodes sink's GET /spans response.
func fetchSpans(ctx context.Context, sink string) ([]probe.SpanSummary, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sink+"/spans", nil)
	if err != nil {
		return nil, fmt.Errorf("building spans request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching spans: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching spans from %s: unexpected status %d", sink, resp.StatusCode)
	}
	var spans []probe.SpanSummary
	if err := json.NewDecoder(resp.Body).Decode(&spans); err != nil {
		return nil, fmt.Errorf("decoding spans from %s: %w", sink, err)
	}
	return spans, nil
}
