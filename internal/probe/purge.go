package probe

import (
	"context"
	"fmt"
	"net/http"
)

// purgeMethod is the non-standard HTTP method Varnish (and most HTTP caches)
// recognize as "invalidate this URL". It has no constant in net/http.
const purgeMethod = "PURGE"

// Purge issues an HTTP PURGE for url. Varnish answers 200 for a purge that
// matched and 404 for one that found nothing; both mean the request was
// understood, so only a transport error or an unexpected status is an error.
func Purge(ctx context.Context, c *http.Client, url string) error {
	req, err := http.NewRequestWithContext(ctx, purgeMethod, url, nil)
	if err != nil {
		return fmt.Errorf("building purge request: %w", err)
	}

	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("purge request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusNotFound:
		return nil
	default:
		return fmt.Errorf("purge %s: unexpected status %d %s", url, resp.StatusCode, http.StatusText(resp.StatusCode))
	}
}
