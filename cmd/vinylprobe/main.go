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
	"unicode"

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
	expectPurgedHelp = "expected objectsPurged count after -purge or -purge-xkey (requires one of them). " +
		"The operator reporting no count at all is always a failure, never treated as 0."
	purgeHostHelp = "site host(s) for -purge, sent as X-Vinyl-Purge-Host (comma-separated). " +
		"The invalidation proxy is reached through its own Host, so this names the Host the " +
		"objects were cached under, i.e. the -host the matching -seed/-check used (#134)."
	hostHelp = "override the HTTP Host header sent, independent of the address -url dials " +
		"(requires -seed, -check, or -purge). Needed because Varnish hashes Host into the " +
		"cache key: addressing pods individually by their own StatefulSet DNS name (to force " +
		"which pod handles a request) gives each pod a different Host, and so a different " +
		"cache key, unless -host pins them to one shared value. See fetch's doc comment in " +
		"internal/probe/cache.go."
	otlpSinkHelp = "listen address for an in-memory OTLP/http-protobuf trace sink " +
		"(serves POST /v1/traces, GET /spans, GET /healthz; runs until killed). " +
		"Mutually exclusive with -url, -purge, -seed, -check, -body-capture, and -assert-spans."
	assertSpansHelp = "poll -sink's /spans until a span matching -span-name (and every -attr) " +
		"appears -min-count times, or -within elapses. " +
		"Mutually exclusive with -url, -purge, -seed, -check, -body-capture, and -otlp-sink."
	sinkHelp     = "base URL of the OTLP sink to poll (requires -assert-spans)"
	spanNameHelp = "exact span name a matching span must carry (requires -assert-spans)"
	attrHelp     = "key=value attribute a matching span must carry; " +
		"may be repeated to require several attributes (only with -assert-spans)"
	minCountHelp    = "minimum number of matching spans required to satisfy -assert-spans"
	withinHelp      = "deadline for -assert-spans to keep polling -sink before failing"
	bodyCaptureHelp = "regex with one capture group applied to the -url response body; " +
		"prints the first group (exit 0), FAIL exit 1 on no match"
	traceIDHelp    = "assert-spans: only count spans with this exact hex trace id"
	spanIDHelp     = "assert-spans: only count spans with this exact hex span id"
	linkSpanIDHelp = "assert-spans: only count spans carrying a link whose span id equals this exact hex value " +
		"(requires -assert-spans)"
	expectStatusHelp = "GET -url, polling until it answers with this HTTP status or -timeout elapses; " +
		"exit 0 on a match, 1 otherwise. Asserts eventual states such as a route answering 503."
	minDurationHelp = "assert-spans: only count spans whose end-start duration is at least this long " +
		"(requires -assert-spans)"
	xkeyHelp = "comma-separated xkey keys (requires -seed or -purge-xkey). With -seed, the echo backend is " +
		"asked through X-Echo-Header to tag the seeded response with an xkey header carrying them. " +
		"With -purge-xkey, the keys to purge. A key must not contain whitespace: vmod_xkey splits on it."
	purgeXkeyHelp = "POST the -xkey keys as {\"keys\": [...]} to -url, the full URL of the invalidation " +
		"service's /purge/xkey endpoint, and require HTTP 200 (every Ready pod the proxy asked answered). " +
		"Combine with -expect-purged to assert the count."
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
	purgeXkey    bool
	seed         bool
	expectPurged int
	host         string
	purgeHost    string
	xkeys        []string

	// otlp-sink and assert-spans mode flags (see otlpSinkHelp/assertSpansHelp).
	otlpSink    string
	assertSpans bool
	sink        string
	spanName    string
	minCount    int
	attrs       map[string]string
	within      time.Duration
	traceID     string
	spanID      string
	linkSpanID  string
	minDuration time.Duration

	// bodyCapture is a -url-family mode: see bodyCaptureHelp.
	bodyCapture string

	// expectStatus is a -url-family mode: see expectStatusHelp. 0 means unset.
	expectStatus int

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
	purgeHost := flag.String("purge-host", "", purgeHostHelp)
	xkey := flag.String("xkey", "", xkeyHelp)
	purgeXkey := flag.Bool("purge-xkey", false, purgeXkeyHelp)

	otlpSink := flag.String("otlp-sink", "", otlpSinkHelp)
	assertSpans := flag.Bool("assert-spans", false, assertSpansHelp)
	sink := flag.String("sink", "", sinkHelp)
	spanName := flag.String("span-name", "", spanNameHelp)
	minCount := flag.Int("min-count", 1, minCountHelp)
	within := flag.Duration("within", 60*time.Second, withinHelp)
	traceID := flag.String("trace-id", "", traceIDHelp)
	spanID := flag.String("span-id", "", spanIDHelp)
	linkSpanID := flag.String("link-span-id", "", linkSpanIDHelp)
	minDuration := flag.Duration("min-duration", 0, minDurationHelp)
	bodyCapture := flag.String("body-capture", "", bodyCaptureHelp)
	expectStatus := flag.Int("expect-status", 0, expectStatusHelp)
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
		purgeXkey:    *purgeXkey,
		seed:         *seed,
		expectPurged: *expectPurged,
		host:         *host,
		purgeHost:    *purgeHost,
		xkeys:        splitKeys(*xkey),
		otlpSink:     *otlpSink,
		assertSpans:  *assertSpans,
		sink:         *sink,
		spanName:     *spanName,
		minCount:     *minCount,
		attrs:        attrs,
		within:       *within,
		traceID:      *traceID,
		spanID:       *spanID,
		linkSpanID:   *linkSpanID,
		minDuration:  *minDuration,
		bodyCapture:  *bodyCapture,
		expectStatus: *expectStatus,
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

// splitKeys splits -xkey's comma-separated value. Empty entries are kept, so
// validateXkey can reject "k1,,k2" instead of quietly using fewer keys than
// the caller wrote; an empty flag yields nil, meaning -xkey was not given.
func splitKeys(raw string) []string {
	if raw == "" {
		return nil
	}
	keys := strings.Split(raw, ",")
	for i, k := range keys {
		keys[i] = strings.TrimSpace(k)
	}
	return keys
}

// validateXkey checks the -xkey/-purge-xkey rules. Split out from validate,
// like validateSpanModes, to keep that function under the gocyclo limit.
func (f probeFlags) validateXkey() error {
	switch {
	case f.purgeXkey && len(f.xkeys) == 0:
		return errors.New("-purge-xkey requires -xkey")
	case len(f.xkeys) > 0 && !f.seed && !f.purgeXkey:
		return errors.New("-xkey only applies to -seed or -purge-xkey")
	}
	for _, k := range f.xkeys {
		if k == "" {
			return errors.New("-xkey has an empty key")
		}
		if strings.IndexFunc(k, unicode.IsSpace) >= 0 {
			return fmt.Errorf("-xkey key %q contains whitespace; vmod_xkey would read it as several keys", k)
		}
	}
	return nil
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
	case f.otlpSink != "" && (f.url != "" || f.purge || f.seed || f.check != "" || f.bodyCapture != ""):
		return true, errors.New("-otlp-sink is mutually exclusive with -url, -purge, -seed, -check, and -body-capture")
	case f.assertSpans && (f.url != "" || f.purge || f.seed || f.check != "" || f.bodyCapture != ""):
		return true, errors.New("-assert-spans is mutually exclusive with -url, -purge, -seed, -check, and -body-capture")
	case f.assertSpans && f.sink == "":
		return true, errors.New("-assert-spans requires -sink")
	case f.assertSpans && f.spanName == "":
		return true, errors.New("-assert-spans requires -span-name")
	case (f.traceID != "" || f.spanID != "") && !f.assertSpans:
		return true, errors.New("-trace-id/-span-id require -assert-spans")
	case (f.linkSpanID != "" || f.minDuration != 0) && !f.assertSpans:
		return true, errors.New("-link-span-id/-min-duration require -assert-spans")
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
	if err := f.validateXkey(); err != nil {
		return err
	}

	// -purge, -purge-xkey, -seed, -check, -body-capture and -expect-status are
	// mutually exclusive modes; anything left over falls through to the
	// original Detect (-expect hit|miss) mode.
	modes := 0
	for _, on := range []bool{f.purge, f.purgeXkey, f.seed, f.check != "", f.bodyCapture != "", f.expectStatus != 0} {
		if on {
			modes++
		}
	}
	switch {
	case modes > 1:
		return errors.New("-purge, -purge-xkey, -seed, -check, -body-capture and -expect-status are mutually exclusive")
	case f.expectStatus != 0 && f.expectSet:
		return errors.New("-expect has no effect with -expect-status; do not pass it")
	case f.expectStatus < 0 || f.expectStatus > 599:
		return errors.New("-expect-status must be an HTTP status code")
	case (f.purge || f.purgeXkey || f.seed) && f.expectSet:
		return errors.New("-expect has no effect with -purge, -purge-xkey or -seed; do not pass it")
	case f.check != "" && f.expectSet:
		return errors.New("-expect has no effect with -check; use -expect-state instead")
	case f.check == "" && f.expectStateSet:
		return errors.New("-expect-state only applies to -check")
	case f.host != "" && !f.purge && !f.seed && f.check == "" && f.bodyCapture == "" && f.expectStatus == 0:
		return errors.New("-host only applies to -purge, -seed, -check, -body-capture, or -expect-status")
	}
	return f.validatePurgeOptions()
}

// validatePurgeOptions checks the options that only make sense with a purge
// mode. Split out from validate, like validateXkey, to keep that function
// under the gocyclo limit.
func (f probeFlags) validatePurgeOptions() error {
	switch {
	case !f.purge && !f.purgeXkey && f.expectPurgedSet:
		return errors.New("-expect-purged only applies to -purge or -purge-xkey")
	case f.expectPurgedSet && f.expectPurged < 0:
		return errors.New("-expect-purged must not be negative")
	case f.purgeHost != "" && !f.purge:
		return errors.New("-purge-host only applies to -purge")
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
	case f.purgeXkey:
		runPurgeXkey(ctx, client, f)
	case f.seed:
		runSeed(ctx, client, f)
	case f.check != "":
		runCheck(ctx, client, f)
	case f.bodyCapture != "":
		runBodyCapture(ctx, client, f)
	case f.expectStatus != 0:
		runExpectStatus(ctx, client, f)
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
	target := f.purgeTarget()
	okMsg := fmt.Sprintf("OK: purged %s (objectsPurged=%s)", target, got)

	if !f.expectPurgedSet {
		return purgeVerdict{exitCode: 0, message: okMsg}
	}
	if n == nil {
		return purgeVerdict{
			exitCode: 1,
			message:  fmt.Sprintf("FAIL: %s purge did not report an objects-purged count, want %d", target, f.expectPurged),
		}
	}
	if *n != f.expectPurged {
		return purgeVerdict{
			exitCode: 1,
			message:  fmt.Sprintf("FAIL: %s purged %d objects, want %d", target, *n, f.expectPurged),
		}
	}
	return purgeVerdict{exitCode: 0, message: okMsg}
}

// purgeTarget names what a purge addressed, for decidePurge's messages: the
// URL for -purge, the keys for -purge-xkey, whose -url is the same endpoint
// for every key.
func (f probeFlags) purgeTarget() string {
	if f.purgeXkey {
		return "xkey " + strings.Join(f.xkeys, ",")
	}
	return f.url
}

// runPurgeXkey issues -purge-xkey and applies decidePurge's verdict, the
// same way runPurge does for -purge.
func runPurgeXkey(ctx context.Context, client *http.Client, f probeFlags) {
	n, err := probe.PurgeXkey(ctx, client, f.url, f.xkeys)
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

// runPurge issues -purge and applies decidePurge's verdict. Only the HTTP
// call and the process exit live here; the decision itself is in
// decidePurge so it can be tested without either.
func runPurge(ctx context.Context, client *http.Client, f probeFlags) {
	n, err := probe.Purge(ctx, client, f.url, f.host, f.purgeHost)
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
	tok, err := probe.Seed(ctx, client, f.url, f.host, f.xkeys...)
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

// runBodyCapture GETs -url once and prints the first capture group of
// -body-capture's regex match against the response body, the same bare-value
// stdout contract -seed uses for its token.
func runBodyCapture(ctx context.Context, client *http.Client, f probeFlags) {
	got, matched, err := probe.BodyCapture(ctx, client, f.url, f.host, f.bodyCapture)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	if !matched {
		fmt.Printf("FAIL: %s body did not match %q\n", f.url, f.bodyCapture)
		os.Exit(1)
	}
	// Bare capture on stdout: chainsaw script steps shell-capture it, the
	// same contract as -seed's token.
	fmt.Println(got)
}

// runExpectStatus polls -url until it answers with -expect-status. A cache
// route without a healthy backend answers 503, which is the expected state
// here, not an error.
func runExpectStatus(ctx context.Context, client *http.Client, f probeFlags) {
	got, err := probe.WaitStatus(ctx, client, f.url, f.host, f.expectStatus)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	if got != f.expectStatus {
		fmt.Printf("FAIL: %s expected status %d, got %d\n", f.url, f.expectStatus, got)
		os.Exit(1)
	}
	fmt.Printf("OK: %s answered %d\n", f.url, got)
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
// name, its TraceID/SpanID match traceID/spanID (when those filters are
// non-empty), it carries a Link whose SpanID equals linkSpanID (when
// linkSpanID is non-empty), its EndUnixNano-StartUnixNano duration is at
// least minDuration (when minDuration is nonzero), and every entry in attrs
// equals the span's own attribute of that key (a missing attribute compares
// unequal to any wanted value, including "").
func decideSpans(
	spans []probe.SpanSummary, name string, attrs map[string]string,
	traceID, spanID, linkSpanID string, minDuration time.Duration, minCount int,
) spanVerdict {
	matched := 0
	for _, s := range spans {
		if s.Name != name {
			continue
		}
		if traceID != "" && s.TraceID != traceID {
			continue
		}
		if spanID != "" && s.SpanID != spanID {
			continue
		}
		if linkSpanID != "" && !hasLinkSpanID(s.Links, linkSpanID) {
			continue
		}
		if minDuration != 0 && time.Duration(s.EndUnixNano-s.StartUnixNano) < minDuration {
			continue
		}
		ok := true
		for k, want := range attrs {
			v, present := s.Attrs[k]
			if !present || v != want {
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

// hasLinkSpanID reports whether any of a span's links names spanID.
func hasLinkSpanID(links []probe.LinkSummary, spanID string) bool {
	for _, l := range links {
		if l.SpanID == spanID {
			return true
		}
	}
	return false
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
	var lastErr error
	for time.Now().Before(deadline) {
		spans, err := fetchSpans(ctx, f.sink)
		lastErr = err
		if err == nil {
			last = decideSpans(spans, f.spanName, f.attrs, f.traceID, f.spanID, f.linkSpanID, f.minDuration, f.minCount)
			if last.satisfied {
				fmt.Printf("OK: %d span(s) named %q matched\n", last.matched, f.spanName)
				os.Exit(0)
			}
		}
		time.Sleep(2 * time.Second)
	}
	msg := fmt.Sprintf("FAIL: only %d span(s) named %q matched within %s",
		last.matched, f.spanName, f.within)
	if lastErr != nil {
		msg += fmt.Sprintf("; last sink error: %v", lastErr)
	}
	fmt.Println(msg)
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
