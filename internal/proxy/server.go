package proxy

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/bluedynamics/cloud-vinyl/internal/monitoring"
)

// unknownHostLogInterval limits the warning for rejected unknown hosts to one
// per interval; the host is client-controlled and could flood the log.
const unknownHostLogInterval = time.Minute

// hostSuggester is implemented by routers that can name the hosts a client
// probably meant (RegisteredRouter).
type hostSuggester interface {
	Suggest(host string) []string
}

// TokenProvider returns the agent Bearer token for a given namespace.
type TokenProvider interface {
	GetToken(namespace string) string
}

// NoopTokenProvider returns empty tokens (no auth).
type NoopTokenProvider struct{}

// GetToken always returns an empty string (no auth).
func (n *NoopTokenProvider) GetToken(_ string) string { return "" }

// Server is the Purge/BAN broadcast proxy HTTP server, listening on port 8090.
type Server struct {
	addr          string
	router        Router
	podMap        PodIPProvider
	broadcaster   Broadcaster
	tokenProvider TokenProvider
	acl           map[string]*ACL // per-cache ACLs, keyed by "namespace/cacheName"
	rateLimiter   RateLimiter
	metrics       *monitoring.Metrics // nil-safe

	unknownHostMu         sync.Mutex
	unknownHostLoggedAt   time.Time
	unknownHostSuppressed int
}

// SetMetrics installs a nil-safe metrics recorder.
func (s *Server) SetMetrics(m *monitoring.Metrics) { s.metrics = m }

// NewServer creates a new Server with the given dependencies.
// acl and rateLimiter may be nil; nil acl allows all sources, nil rateLimiter
// disables rate limiting.
func NewServer(addr string, router Router, pods PodIPProvider, b Broadcaster, tp TokenProvider) *Server {
	if tp == nil {
		tp = &NoopTokenProvider{}
	}
	return &Server{
		addr:          addr,
		router:        router,
		podMap:        pods,
		broadcaster:   b,
		tokenProvider: tp,
		acl:           make(map[string]*ACL),
		rateLimiter:   &NoopRateLimiter{},
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
		s.rejectUnknownHost(w, r.Host)
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
	case r.Method == methodPurge:
		s.handlePurge(w, r, namespace, cacheName, pods)
	case r.Method == "BAN":
		s.handleBAN(w, r, namespace, cacheName, pods)
	case r.Method == http.MethodPost && r.URL.Path == "/ban":
		s.handleBAN(w, r, namespace, cacheName, pods)
	case r.Method == http.MethodPost && r.URL.Path == "/purge/xkey":
		s.handleXkey(w, r, namespace, cacheName, pods)
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

// rejectUnknownHost answers a request whose Host names no registered cache.
// It counts and logs the rejection, since clients such as plone.cachepurging
// do not report failed purges, and names the namespaced host when the Host is
// a cache's bare Service name (#132).
func (s *Server) rejectUnknownHost(w http.ResponseWriter, host string) {
	if s.metrics != nil {
		s.metrics.InvalidationUnknownHostTotal.Inc()
	}
	var hints []string
	if sg, ok := s.router.(hostSuggester); ok {
		hints = sg.Suggest(host)
	}
	s.logUnknownHost(host, hints)

	msg := "unknown cache: " + host
	if len(hints) > 0 {
		quoted := make([]string, len(hints))
		for i, h := range hints {
			quoted[i] = fmt.Sprintf("%q", h)
		}
		msg += fmt.Sprintf(" (the Host must include the namespace, use %s)", strings.Join(quoted, " or "))
	}
	writeJSONError(w, http.StatusNotFound, msg)
}

// logUnknownHost warns at most once per unknownHostLogInterval and reports
// how many rejections it skipped in between.
func (s *Server) logUnknownHost(host string, hints []string) {
	s.unknownHostMu.Lock()
	now := time.Now()
	if now.Sub(s.unknownHostLoggedAt) < unknownHostLogInterval {
		s.unknownHostSuppressed++
		s.unknownHostMu.Unlock()
		return
	}
	suppressed := s.unknownHostSuppressed
	s.unknownHostLoggedAt, s.unknownHostSuppressed = now, 0
	s.unknownHostMu.Unlock()

	logf.Log.WithName("invalidation-proxy").Info(
		"rejected invalidation request: Host names no registered cache",
		"host", host, "useHost", hints, "suppressedSinceLastLog", suppressed)
}
