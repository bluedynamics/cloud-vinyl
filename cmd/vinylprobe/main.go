// Command vinylprobe checks cache behaviour over HTTP from inside the cluster.
//
// It must not import k8s.io/*: chainsaw owns Kubernetes state, this owns HTTP.
// hack/check-e2e-boundary.sh enforces that.
package main

import (
	"context"
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
)

func main() {
	url := flag.String("url", "", "URL to probe (required)")
	expect := flag.String("expect", "hit", expectHelp)
	expectState := flag.String("expect-state", "", expectStateHelp)
	check := flag.String("check", "", "seed token to check for at -url with a single request (requires -expect-state)")
	timeout := flag.Duration("timeout", 30*time.Second, "overall deadline")
	purge := flag.Bool("purge", false, "issue an HTTP PURGE for -url instead of detecting hit/miss")
	seed := flag.Bool("seed", false, seedHelp)
	flag.Parse()

	expectSet := false
	expectStateSet := false
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "expect":
			expectSet = true
		case "expect-state":
			expectStateSet = true
		}
	})

	if *url == "" {
		fmt.Fprintln(os.Stderr, "error: -url is required")
		os.Exit(2)
	}

	// -purge, -seed and -check are three different, mutually exclusive modes;
	// anything left over falls through to the original Detect (-expect
	// hit|miss) mode.
	modes := 0
	if *purge {
		modes++
	}
	if *seed {
		modes++
	}
	if *check != "" {
		modes++
	}
	if modes > 1 {
		fmt.Fprintln(os.Stderr, "error: -purge, -seed and -check are mutually exclusive")
		os.Exit(2)
	}
	if (*purge || *seed) && expectSet {
		fmt.Fprintln(os.Stderr, "error: -expect has no effect with -purge or -seed; do not pass it")
		os.Exit(2)
	}
	if *check != "" && expectSet {
		fmt.Fprintln(os.Stderr, "error: -expect has no effect with -check; use -expect-state instead")
		os.Exit(2)
	}
	if *check == "" && expectStateSet {
		fmt.Fprintln(os.Stderr, "error: -expect-state only applies to -check")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	client := &http.Client{}

	switch {
	case *purge:
		if err := probe.Purge(ctx, client, *url); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(2)
		}
		fmt.Printf("OK: purged %s\n", *url)
		return

	case *seed:
		tok, err := probe.Seed(ctx, client, *url)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(2)
		}
		// Bare token on stdout: a chainsaw script step captures this into a
		// shell variable to pass to a later -check call.
		fmt.Println(tok)
		return

	case *check != "":
		if !expectStateSet {
			fmt.Fprintln(os.Stderr, `error: -check requires -expect-state ("cached" or "not-cached")`)
			os.Exit(2)
		}
		var want probe.State
		switch *expectState {
		case "cached":
			want = probe.Cached
		case "not-cached":
			want = probe.NotCached
		default:
			fmt.Fprintf(os.Stderr, "error: -expect-state must be \"cached\" or \"not-cached\", got %q\n", *expectState)
			os.Exit(2)
		}
		got, err := probe.Check(ctx, client, *url, *check)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(2)
		}
		if got != want {
			fmt.Printf("FAIL: %s expected %s, got %s\n", *url, want, got)
			os.Exit(1)
		}
		fmt.Printf("OK: %s is %s\n", *url, got)
		return

	default:
		var want probe.Outcome
		switch *expect {
		case "hit":
			want = probe.Hit
		case "miss":
			want = probe.Miss
		default:
			fmt.Fprintf(os.Stderr, "error: -expect must be \"hit\" or \"miss\", got %q\n", *expect)
			os.Exit(2)
		}

		got, err := probe.Detect(ctx, client, *url)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(2)
		}
		if got != want {
			fmt.Printf("FAIL: %s expected %s, got %s\n", *url, want, got)
			os.Exit(1)
		}
		fmt.Printf("OK: %s is a %s\n", *url, got)
	}
}
