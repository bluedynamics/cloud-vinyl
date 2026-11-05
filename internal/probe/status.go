package probe

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// statusPollInterval is how long WaitStatus waits between two requests.
const statusPollInterval = 500 * time.Millisecond

// WaitStatus issues GETs to url until one answers with the status want, or ctx
// ends. It returns the last status seen (0 if no request ever got an
// answer). An eventual state, such as a route turning from 503 to 200 once
// endpoints return, needs polling to be asserted without a flaky sleep.
//
// host, when non-empty, overrides the HTTP Host header; see fetch.
func WaitStatus(ctx context.Context, c *http.Client, url, host string, want int) (int, error) {
	last := 0
	var lastErr error
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return last, err
		}
		if host != "" {
			req.Host = host
		}
		resp, err := c.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			last, lastErr = resp.StatusCode, nil
			if last == want {
				return last, nil
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return last, fmt.Errorf("no answer with status %d before the deadline: %w", want, lastErr)
			}
			return last, nil
		case <-time.After(statusPollInterval):
		}
	}
}
