// Package probe detects cache behaviour over plain HTTP.
//
// It deliberately depends on nothing from k8s.io: the E2E boundary is that
// chainsaw owns Kubernetes state and this package owns HTTP. See
// docs/sources/explanation/testing-strategy.md.
package probe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// Outcome is whether a response came from cache.
type Outcome int

const (
	// Miss means the response was produced for this request.
	Miss Outcome = iota
	// Hit means the response was served from cache.
	Hit
)

func (o Outcome) String() string {
	if o == Hit {
		return "hit"
	}
	return "miss"
}

// probeHeader is echoed back by the backend, which is how a cached response
// gives itself away: it still carries the token of the request that filled it.
const probeHeader = "X-Probe"

// echoHeader asks the echo backend for response headers: with
// ECHO__ENABLE__HEADER=true (e2e/fixtures/backends/echo-service.yaml) it adds
// one response header per "name:value" pair, pairs separated by ",". Seed
// uses it to make the backend tag an object with an xkey header. Several keys
// therefore go into one pair, separated by a single space, which is also how
// vmod_xkey reads several keys from one xkey header.
const echoHeader = "X-Echo-Header"

// Detect issues two GETs to url carrying distinct probe tokens and reports
// whether the second was served from cache.
//
// The generated VCL sets no debug headers, and adding one would be a product
// change made for tests, so this infers the answer from the backend echo
// instead.
func Detect(ctx context.Context, c *http.Client, url string) (Outcome, error) {
	first, err := token()
	if err != nil {
		return Miss, err
	}
	second, err := token()
	if err != nil {
		return Miss, err
	}

	if _, err := fetch(ctx, c, url, first, "", nil); err != nil {
		return Miss, fmt.Errorf("first request: %w", err)
	}
	body, err := fetch(ctx, c, url, second, "", nil)
	if err != nil {
		return Miss, fmt.Errorf("second request: %w", err)
	}

	switch {
	case strings.Contains(body, first):
		return Hit, nil
	case strings.Contains(body, second):
		return Miss, nil
	default:
		return Miss, fmt.Errorf("backend did not echo the %s header; cannot tell hit from miss", probeHeader)
	}
}

// BodyCapture GETs url once and applies pattern (which must contain at
// least one capture group) to the response body, returning the first
// group of the first match. It exists for E2E assertions against backends
// that reflect request state into their response body (the echo server's
// lowercased-header JSON), e.g. reading back a VCL-minted traceparent.
// matched=false with a nil error means the body simply didn't match.
func BodyCapture(ctx context.Context, c *http.Client, url, host, pattern string) (string, bool, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", false, fmt.Errorf("compiling -body-capture pattern: %w", err)
	}
	if re.NumSubexp() < 1 {
		return "", false, fmt.Errorf("-body-capture pattern %q has no capture group", pattern)
	}
	tok, err := token()
	if err != nil {
		return "", false, err
	}
	body, err := fetch(ctx, c, url, tok, host, nil)
	if err != nil {
		return "", false, err
	}
	m := re.FindStringSubmatch(body)
	if m == nil {
		return "", false, nil
	}
	return m[1], true, nil
}

// fetch issues a single GET to url carrying tok in probeHeader.
//
// host, when non-empty, overrides the HTTP Host header sent — independent
// of the address url actually dials. This is not a hypothetical knob: Varnish
// hashes req.http.host into the cache key (vcl_hash.vcl.tmpl), so a request
// aimed at one pod's own StatefulSet DNS name (e.g. my-cache-0.my-cache, used
// to force which pod handles it — see cache-and-invalidate's seed step) gets
// a *different* Host, and therefore a different cache key, than the same
// path reached through any other name. Three pods addressed that way each
// cache their object under a different key, and no single request — a PURGE
// least of all — can ever address more than one of them at once. See
// e2e/tests/cache-and-invalidate/chainsaw-test.yaml for how this is used to
// make seeding and purging agree on one key across pods without losing
// per-pod addressing.
//
// hdr, when non-nil, adds request headers, e.g. echoHeader for Seed.
func fetch(ctx context.Context, c *http.Client, url, tok, host string, hdr http.Header) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set(probeHeader, tok)
	if host != "" {
		req.Host = host
	}

	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func token() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating probe token: %w", err)
	}
	return "probe-" + hex.EncodeToString(b[:]), nil
}
