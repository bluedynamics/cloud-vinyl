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

func main() {
	url := flag.String("url", "", "URL to probe (required)")
	expect := flag.String("expect", "hit", `expected outcome: "hit" or "miss" (ignored with -purge)`)
	timeout := flag.Duration("timeout", 30*time.Second, "overall deadline")
	purge := flag.Bool("purge", false, "issue an HTTP PURGE for -url instead of detecting hit/miss")
	flag.Parse()

	expectSet := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "expect" {
			expectSet = true
		}
	})

	if *url == "" {
		fmt.Fprintln(os.Stderr, "error: -url is required")
		os.Exit(2)
	}
	if *purge && expectSet {
		fmt.Fprintln(os.Stderr, "error: -expect has no effect with -purge; do not pass both")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	client := &http.Client{}

	if *purge {
		if err := probe.Purge(ctx, client, *url); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(2)
		}
		fmt.Printf("OK: purged %s\n", *url)
		return
	}

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
