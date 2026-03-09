package agent

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// XkeyPurger sends xkey purge requests to varnish via localhost HTTP.
// xkey is a VCL function, not accessible via admin port (§3.4, §6.4).
type XkeyPurger struct {
	varnishAddr string // e.g. "http://127.0.0.1:8080"
	client      *http.Client
}

// NewXkeyPurger creates a new XkeyPurger.
func NewXkeyPurger(varnishAddr string) *XkeyPurger {
	return &XkeyPurger{
		varnishAddr: varnishAddr,
		client:      &http.Client{Timeout: 10 * time.Second},
	}
}

// Purge purges objects by xkey. Returns number of purged objects.
func (x *XkeyPurger) Purge(ctx context.Context, keys []string, soft bool) (int, error) {
	purged := 0
	for _, key := range keys {
		req, err := http.NewRequestWithContext(ctx, "PURGE", x.varnishAddr+"/", nil)
		if err != nil {
			return purged, fmt.Errorf("create purge request: %w", err)
		}
		if soft {
			req.Header.Set("X-Xkey-Softpurge", key)
		} else {
			req.Header.Set("X-Xkey-Purge", key)
		}
		resp, err := x.client.Do(req)
		if err != nil {
			return purged, fmt.Errorf("purge xkey %q: %w", key, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent {
			purged++
		}
	}
	return purged, nil
}
