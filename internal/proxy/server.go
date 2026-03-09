package proxy

import (
	"context"
	"net/http"
	"time"
)

// Server is the Purge/BAN broadcast proxy HTTP server, listening on port 8090.
type Server struct {
	addr        string
	router      Router
	podMap      PodIPProvider
	broadcaster Broadcaster
	acl         map[string]*ACL // per-cache ACLs, keyed by "namespace/cacheName"
	rateLimiter RateLimiter
}

// NewServer creates a new Server with the given dependencies.
// acl and rateLimiter may be nil; nil acl allows all sources, nil rateLimiter
// disables rate limiting.
func NewServer(addr string, router Router, pods PodIPProvider, b Broadcaster) *Server {
	return &Server{
		addr:        addr,
		router:      router,
		podMap:      pods,
		broadcaster: b,
		acl:         make(map[string]*ACL),
		rateLimiter: &NoopRateLimiter{},
	}
}

// SetACL registers a per-cache ACL. key is "namespace/cacheName".
func (s *Server) SetACL(key string, acl *ACL) {
	s.acl[key] = acl
}

// SetRateLimiter replaces the rate limiter.
func (s *Server) SetRateLimiter(rl RateLimiter) {
	s.rateLimiter = rl
}

// ServeHTTP implements http.Handler. It runs the middleware chain:
//  1. Route lookup via Host header
//  2. ACL check
//  3. Rate limit check
//  4. Dispatch to the appropriate handler
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 1. Resolve Host → namespace/cacheName.
	namespace, cacheName, ok := s.router.Lookup(r.Host)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "unknown cache: "+r.Host)
		return
	}

	// 2. ACL check.
	cacheKey := namespace + "/" + cacheName
	if acl, exists := s.acl[cacheKey]; exists {
		if !acl.Allows(r.RemoteAddr) {
			writeJSONError(w, http.StatusForbidden, "source IP not allowed")
			return
		}
	}

	// 3. Rate limit check.
	if !s.rateLimiter.Allow(cacheKey) {
		writeJSONError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}

	// 4. Resolve pod IPs.
	pods := s.podMap.GetPodIPs(namespace, cacheName)
	if len(pods) == 0 {
		writeJSONError(w, http.StatusServiceUnavailable, "no pods available for "+cacheKey)
		return
	}

	// 5. Dispatch.
	switch {
	case r.Method == "PURGE":
		s.handlePurge(w, r, pods)
	case r.Method == "BAN":
		s.handleBAN(w, r, pods)
	case r.Method == http.MethodPost && r.URL.Path == "/ban":
		s.handleBAN(w, r, pods)
	case r.Method == http.MethodPost && r.URL.Path == "/purge/xkey":
		s.handleXkey(w, r, pods)
	default:
		writeJSONError(w, http.StatusNotFound, "no route for "+r.Method+" "+r.URL.Path)
	}
}

// Start runs the HTTP server until ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	srv := &http.Server{
		Addr:         s.addr,
		Handler:      s,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}
