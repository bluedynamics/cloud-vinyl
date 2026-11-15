package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// purgeMethod is the non-standard HTTP method Varnish (and most HTTP caches)
// recognize as "invalidate this URL". It has no constant in net/http.
const purgeMethod = "PURGE"

// purgeResponse is the subset of the invalidation proxy's JSON body (see
// internal/proxy.BroadcastResult) this package reads. It is a deliberate,
// separate copy of the shape rather than an import of that type:
// internal/proxy pulls in k8s.io/* transitively, and cmd/vinylprobe must
// not (hack/check-e2e-boundary.sh enforces this).
type purgeResponse struct {
	// ObjectsPurged mirrors internal/proxy.BroadcastResult.ObjectsPurged:
	// nil means the operator reported no parseable count. That must stay
	// distinct from a known, confirmed zero — #103 went to some trouble to
	// keep the two apart, and collapsing them here would throw that away.
	ObjectsPurged *int `json:"objectsPurged"`
}

// Purge issues an HTTP PURGE for url and reports how many objects the
// response said were removed. Varnish (and the invalidation proxy fronting
// it) answers 200 for a purge that matched, 404 for one that found nothing,
// and 207 for a broadcast that partially failed; all three mean the request
// was understood, so only a transport error or an unexpected status is an
// error here.
//
// The returned count is nil when the response body carried no parseable
// "objectsPurged" field — e.g. a PURGE sent directly to a Varnish pod
// rather than through the invalidation proxy, whose response is Varnish's
// own plain-text synth body, not JSON; or an older operator predating
// #103. nil means unknown and must never be read as a confirmed zero — the
// same distinction internal/proxy.BroadcastResult.ObjectsPurged makes.
//
// host, when non-empty, overrides the HTTP Host header sent — see fetch's
// doc comment in cache.go for why: a broadcast PURGE reaches every pod with
// one Host, so it only invalidates objects that were cached under that same
// Host. Pass whatever Host the matching Seed/Check calls used.
//
// purgeHost, when non-empty, is sent as X-Vinyl-Purge-Host: the site host(s)
// the invalidation proxy forwards the purge with. Through the proxy, Host is
// the invalidation host, so the objects' own Host has to travel separately
// (#134).
func Purge(ctx context.Context, c *http.Client, url, host, purgeHost string) (*int, error) {
	req, err := http.NewRequestWithContext(ctx, purgeMethod, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building purge request: %w", err)
	}
	if host != "" {
		req.Host = host
	}
	if purgeHost != "" {
		req.Header.Set("X-Vinyl-Purge-Host", purgeHost)
	}

	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("purge request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusMultiStatus, http.StatusNotFound:
		var body purgeResponse
		// A non-JSON or empty body is not an error here, just an unknown
		// count: see the nil case documented above.
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return nil, nil
		}
		return body.ObjectsPurged, nil
	default:
		return nil, fmt.Errorf("purge %s: unexpected status %d %s", url, resp.StatusCode, http.StatusText(resp.StatusCode))
	}
}

// xkeyPurgeRequest is the JSON body of the invalidation proxy's
// POST /purge/xkey. internal/proxy's own type is unexported, so
// purge_boundary_test.go pins this shape by sending it to the real handler.
type xkeyPurgeRequest struct {
	Keys []string `json:"keys"`
}

// maxErrorBody bounds how much of an unexpected response body PurgeXkey
// quotes in its error.
const maxErrorBody = 1024

// PurgeXkey asks the invalidation proxy at url (its /purge/xkey endpoint) to
// purge every object tagged with one of keys, and reports how many objects
// the response said were removed. The count is nil when the body carried no
// parseable "objectsPurged" field, never a fabricated 0 (see Purge).
//
// Only HTTP 200 is success, stricter than Purge, which also accepts 207 and
// 404. Varnish's xkey branch answers 200 even when nothing matched, so the
// proxy answers 200 exactly when every pod did, i.e. when its "status" is
// "ok" (httpStatusCode in internal/proxy/broadcast.go). A 207 means a pod
// failed, and the counts of the healthy pods could otherwise satisfy an
// expected count of 0. Every other status is an error that quotes the start
// of the body, which names the failing pods.
func PurgeXkey(ctx context.Context, c *http.Client, url string, keys []string) (*int, error) {
	body, err := json.Marshal(xkeyPurgeRequest{Keys: keys})
	if err != nil {
		return nil, fmt.Errorf("encoding xkey purge request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building xkey purge request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("xkey purge request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return nil, fmt.Errorf("xkey purge %s: unexpected status %d %s: %s",
			url, resp.StatusCode, http.StatusText(resp.StatusCode), bytes.TrimSpace(b))
	}
	var parsed purgeResponse
	// A non-JSON or empty body is an unknown count, not an error (see Purge).
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, nil
	}
	return parsed.ObjectsPurged, nil
}
