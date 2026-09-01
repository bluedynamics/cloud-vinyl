package probe

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// Seed issues a single GET to url carrying a fresh probe token, deliberately
// populating the cache, and returns that token so a later Check call knows
// what to look for.
//
// It is single-request by design, same reasoning as Check: Detect needs two
// requests to compare against each other, but Seed only needs to put one
// object in cache.
//
// host, when non-empty, overrides the HTTP Host header sent — see fetch's
// doc comment for why this matters: the cache key includes Host, so seeding
// several pods by their own distinct DNS names (to force which pod handles
// each request) puts each object under a different key unless the caller
// pins Host to one shared value across all of them.
func Seed(ctx context.Context, c *http.Client, url, host string) (string, error) {
	tok, err := token()
	if err != nil {
		return "", err
	}
	if _, err := fetch(ctx, c, url, tok, host); err != nil {
		return "", fmt.Errorf("seeding: %w", err)
	}
	return tok, nil
}

// State is whether a previously seeded object is still being served from
// cache.
type State int

const (
	// NotCached means the seeded object is gone: the response to this check
	// carried the check's own fresh token, meaning the backend was hit again.
	NotCached State = iota
	// Cached means the seeded object is still being served: the response
	// still carried the seed token.
	Cached
)

func (s State) String() string {
	if s == Cached {
		return "cached"
	}
	return "not-cached"
}

// Check issues a single GET to url carrying a fresh probe token and reports
// whether the object seeded with seed is still the one being served.
//
// It is single-request by design: Detect uses two requests because it needs
// to compare a fresh miss against a possible hit, but a second request here
// would itself repopulate the cache, turning the measurement into a mutation
// of the very state it is trying to observe.
//
// host overrides the HTTP Host header sent, same as Seed's; pass the same
// value used to seed the object being checked, or the cache key looked up
// here will not be the one that was written.
func Check(ctx context.Context, c *http.Client, url, seed, host string) (State, error) {
	tok, err := token()
	if err != nil {
		return NotCached, err
	}
	body, err := fetch(ctx, c, url, tok, host)
	if err != nil {
		return NotCached, fmt.Errorf("check request: %w", err)
	}

	switch {
	case strings.Contains(body, seed):
		return Cached, nil
	case strings.Contains(body, tok):
		return NotCached, nil
	default:
		return NotCached, fmt.Errorf("backend echoed neither the seed token nor this request's token; cannot tell cached from not-cached")
	}
}
