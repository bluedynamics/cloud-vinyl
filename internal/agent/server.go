package agent

import (
	"context"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Server is the vinyl-agent HTTP server.
type Server struct {
	httpServer *http.Server
	handler    *Handler
	token      string
}

// NewServer creates a new agent server.
func NewServer(addr, token string, admin AdminClient, xkey *XkeyPurger) *Server {
	h := NewHandler(admin, xkey)
	s := &Server{handler: h, token: token}

	mux := http.NewServeMux()

	// Unauthenticated endpoints
	mux.HandleFunc("/livez", Livez)
	mux.HandleFunc("/health", h.Health)
	mux.Handle("/metrics", promhttp.Handler())

	// Authenticated endpoints wrapped in auth middleware
	authedMux := http.NewServeMux()
	authedMux.HandleFunc("/vcl/push", h.PushVCL)
	authedMux.HandleFunc("/vcl/validate", h.ValidateVCL)
	authedMux.HandleFunc("/vcl/active", h.ActiveVCL)
	authedMux.HandleFunc("/ban", h.Ban)
	authedMux.HandleFunc("/purge/xkey", h.PurgeXkey)

	mux.Handle("/vcl/", BearerAuthMiddleware(token, authedMux))
	mux.Handle("/ban", BearerAuthMiddleware(token, authedMux))
	mux.Handle("/purge/", BearerAuthMiddleware(token, authedMux))

	s.httpServer = &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 60 * time.Second,
	}
	return s
}

// Start starts the HTTP server.
func (s *Server) Start() error {
	return s.httpServer.ListenAndServe()
}

// Serve serves on an already bound listener. Binding separately from serving
// lets the caller log "listening" only once the socket really is bound.
func (s *Server) Serve(l net.Listener) error {
	return s.httpServer.Serve(l)
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// Livez answers 200 as soon as the agent's HTTP server runs, independent of
// varnishd and of the active VCL. The agent container's startup probe uses
// it, and the operator pushes VCL only to pods whose agent has started, so
// a rollout no longer meets "connection refused" (#136). /health is the
// readiness check and stays red until a VCL has been pushed, so it cannot
// gate the push itself.
func Livez(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}
