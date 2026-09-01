package probe

import (
	"context"
	"encoding/json"
	"fmt"
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
func Purge(ctx context.Context, c *http.Client, url, host string) (*int, error) {
	req, err := http.NewRequestWithContext(ctx, purgeMethod, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building purge request: %w", err)
	}
	if host != "" {
		req.Host = host
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
