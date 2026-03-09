package agent_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bluedynamics/cloud-vinyl/internal/agent"
	"github.com/stretchr/testify/assert"
)

func TestBearerAuth_MissingToken_Returns401(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := agent.BearerAuthMiddleware("secret-token", next)
	req := httptest.NewRequest(http.MethodGet, "/vcl/active", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestBearerAuth_WrongToken_Returns401(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := agent.BearerAuthMiddleware("correct-token", next)
	req := httptest.NewRequest(http.MethodGet, "/vcl/active", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestBearerAuth_CorrectToken_PassesThrough(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	handler := agent.BearerAuthMiddleware("correct-token", next)
	req := httptest.NewRequest(http.MethodGet, "/vcl/active", nil)
	req.Header.Set("Authorization", "Bearer correct-token")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.True(t, called)
}

func TestBearerAuth_SkipPath_NoAuthRequired(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	handler := agent.BearerAuthMiddleware("secret", next, "/health", "/metrics")
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	// No Authorization header
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.True(t, called)
}
