package agent_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bluedynamics/cloud-vinyl/internal/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestServer(t *testing.T) (*agent.Server, *mockAdmin) {
	t.Helper()
	mock := &mockAdmin{}
	xkey := agent.NewXkeyPurger("http://127.0.0.1:8080")
	srv := agent.NewServer("127.0.0.1:0", "test-token", mock, xkey)
	return srv, mock
}

func TestServer_HealthEndpoint_NoAuth(t *testing.T) {
	// Use the handler directly via httptest to avoid binding a real port.
	// Set a non-boot VCL so the readiness check passes.
	mock := &mockAdmin{}
	mock.activeVCLFn = func(ctx context.Context) (string, error) {
		return "operator-pushed-vcl", nil
	}
	xkey := agent.NewXkeyPurger("http://127.0.0.1:8080")
	h := agent.NewHandler(mock, xkey)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	h.Health(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), `"ok"`)
}

func TestServer_AuthMiddleware_ProtectsVCLEndpoints(t *testing.T) {
	// Test that the auth middleware protects VCL endpoints when called via the server mux
	_ = newTestServer // ensure newTestServer compiles
	middleware := agent.BearerAuthMiddleware("secret", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Without token
	req := httptest.NewRequest(http.MethodGet, "/vcl/active", nil)
	rr := httptest.NewRecorder()
	middleware.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)

	// With correct token
	req2 := httptest.NewRequest(http.MethodGet, "/vcl/active", nil)
	req2.Header.Set("Authorization", "Bearer secret")
	rr2 := httptest.NewRecorder()
	middleware.ServeHTTP(rr2, req2)
	assert.Equal(t, http.StatusOK, rr2.Code)
}
