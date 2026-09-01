// Command vinylprobe checks cache behaviour over HTTP from inside the cluster.
//
// It must not import k8s.io/*: chainsaw owns Kubernetes state, this owns HTTP.
// hack/check-e2e-boundary.sh enforces that.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
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
	flag.Parse()

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

// validate checks flag combinations that flag.Parse cannot express itself:
// mutually exclusive modes, and options that only make sense with one
// particular mode. It changes nothing; main exits on a non-nil result.
func (f probeFlags) validate() error {
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

// runPurge issues -purge and, if -expect-purged was given, asserts the
// reported count. n is nil when the response carried no parseable
// objectsPurged count — distinct from a known 0 (#103) — so it is rendered
// as "unknown", never silently as "0", and treated as an unconditional
// assertion failure rather than a coerced zero.
func runPurge(ctx context.Context, client *http.Client, f probeFlags) {
	n, err := probe.Purge(ctx, client, f.url, f.host)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	got := "unknown"
	if n != nil {
		got = fmt.Sprintf("%d", *n)
	}
	if f.expectPurgedSet {
		if n == nil {
			fmt.Printf("FAIL: %s purge did not report an objects-purged count, want %d\n", f.url, f.expectPurged)
			os.Exit(1)
		}
		if *n != f.expectPurged {
			fmt.Printf("FAIL: %s purged %d objects, want %d\n", f.url, *n, f.expectPurged)
			os.Exit(1)
		}
	}
	fmt.Printf("OK: purged %s (objectsPurged=%s)\n", f.url, got)
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
