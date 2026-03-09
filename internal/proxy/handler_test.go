package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockBroadcaster records calls and returns a preset result.
type MockBroadcaster struct {
	LastReq  BroadcastRequest
	LastPods []string
	Result   BroadcastResult
}

func (m *MockBroadcaster) Broadcast(_ context.Context, pods []string, req BroadcastRequest) BroadcastResult {
	m.LastPods = pods
	m.LastReq = req
	return m.Result
}

// newTestServer builds a Server wired to a StaticRouter, a fixed PodMap, and
// the given MockBroadcaster.
func newTestServer(mb *MockBroadcaster) *Server {
	router := NewStaticRouter(map[string][2]string{
		"my-cache-invalidation.production": {"production", "my-cache"},
	})

	pm := NewPodMap()
	pm.Update("production", "my-cache", []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"})

	return NewServer(":8090", router, pm, mb)
}

func okResult() BroadcastResult {
	return BroadcastResult{
		Status:    "ok",
		Total:     3,
		Succeeded: 3,
		Results: []PodResult{
			{Pod: "10.0.0.1:8080", Status: 200},
			{Pod: "10.0.0.2:8080", Status: 200},
			{Pod: "10.0.0.3:8080", Status: 200},
		},
	}
}

// ---------- PURGE ----------

func TestHandlePurge(t *testing.T) {
	mb := &MockBroadcaster{Result: okResult()}
	srv := newTestServer(mb)

	req := httptest.NewRequest("PURGE", "/product/123", nil)
	req.Host = "my-cache-invalidation.production"
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "PURGE", mb.LastReq.Method)
	assert.Equal(t, "/product/123", mb.LastReq.Path)
}

// ---------- BAN via method ----------

func TestHandleBANMethod(t *testing.T) {
	mb := &MockBroadcaster{Result: okResult()}
	srv := newTestServer(mb)

	req := httptest.NewRequest("BAN", "/", nil)
	req.Host = "my-cache-invalidation.production"
	req.Header.Set("X-Ban-Expression", "obj.http.X-Url ~ ^/product/")
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "/ban", mb.LastReq.Path)

	// Check that the expression was forwarded in the JSON body.
	var body banRESTRequest
	require.NoError(t, json.Unmarshal(mb.LastReq.Body, &body))
	assert.Equal(t, "obj.http.X-Url ~ ^/product/", body.Expression)
}

func TestHandleBANMethod_MissingHeader(t *testing.T) {
	mb := &MockBroadcaster{Result: okResult()}
	srv := newTestServer(mb)

	req := httptest.NewRequest("BAN", "/", nil)
	req.Host = "my-cache-invalidation.production"
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleBANMethod_InvalidExpression(t *testing.T) {
	mb := &MockBroadcaster{Result: okResult()}
	srv := newTestServer(mb)

	req := httptest.NewRequest("BAN", "/", nil)
	req.Host = "my-cache-invalidation.production"
	req.Header.Set("X-Ban-Expression", "req.url ~ ^/product/")
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// ---------- BAN via REST ----------

func TestHandleBANREST(t *testing.T) {
	mb := &MockBroadcaster{Result: okResult()}
	srv := newTestServer(mb)

	body := `{"expression":"obj.http.X-Url ~ ^/product/"}`
	req := httptest.NewRequest(http.MethodPost, "/ban", strings.NewReader(body))
	req.Host = "my-cache-invalidation.production"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "/ban", mb.LastReq.Path)
}

func TestHandleBANREST_InvalidJSON(t *testing.T) {
	mb := &MockBroadcaster{Result: okResult()}
	srv := newTestServer(mb)

	req := httptest.NewRequest(http.MethodPost, "/ban", strings.NewReader("not-json"))
	req.Host = "my-cache-invalidation.production"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleBANREST_InvalidExpression(t *testing.T) {
	mb := &MockBroadcaster{Result: okResult()}
	srv := newTestServer(mb)

	body := `{"expression":"req.url ~ ^/product/"}`
	req := httptest.NewRequest(http.MethodPost, "/ban", strings.NewReader(body))
	req.Host = "my-cache-invalidation.production"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// ---------- xkey ----------

func TestHandleXkey(t *testing.T) {
	mb := &MockBroadcaster{Result: okResult()}
	srv := newTestServer(mb)

	body := `{"keys":["article-123","category-news"]}`
	req := httptest.NewRequest(http.MethodPost, "/purge/xkey", strings.NewReader(body))
	req.Host = "my-cache-invalidation.production"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	// Two keys × 3 pods = 6 total. MockBroadcaster returns 3 succeeded per call,
	// so 2 calls → 6 succeeded → status "ok".
	assert.Equal(t, http.StatusOK, rec.Code)

	var result BroadcastResult
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&result))
	assert.Equal(t, "ok", result.Status)
}

func TestHandleXkey_EmptyKeys(t *testing.T) {
	mb := &MockBroadcaster{Result: okResult()}
	srv := newTestServer(mb)

	body := `{"keys":[]}`
	req := httptest.NewRequest(http.MethodPost, "/purge/xkey", strings.NewReader(body))
	req.Host = "my-cache-invalidation.production"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// ---------- routing / middleware ----------

func TestUnknownHost(t *testing.T) {
	mb := &MockBroadcaster{Result: okResult()}
	srv := newTestServer(mb)

	req := httptest.NewRequest("PURGE", "/product/123", nil)
	req.Host = "nonexistent.example.com"
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestUnknownMethod(t *testing.T) {
	mb := &MockBroadcaster{Result: okResult()}
	srv := newTestServer(mb)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "my-cache-invalidation.production"
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestACLDenied(t *testing.T) {
	mb := &MockBroadcaster{Result: okResult()}
	srv := newTestServer(mb)

	acl, err := NewACL([]string{"10.0.0.0/24"})
	require.NoError(t, err)
	srv.SetACL("production/my-cache", acl)

	req := httptest.NewRequest("PURGE", "/product/123", nil)
	req.Host = "my-cache-invalidation.production"
	// httptest.NewRequest sets RemoteAddr to "192.0.2.1:1234"
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestRateLimitExceeded(t *testing.T) {
	mb := &MockBroadcaster{Result: okResult()}
	srv := newTestServer(mb)

	// 1 request per minute, burst 1.
	srv.SetRateLimiter(NewTokenBucketRateLimiter(1, 1))

	doReq := func() int {
		req := httptest.NewRequest("PURGE", "/product/123", nil)
		req.Host = "my-cache-invalidation.production"
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code
	}

	assert.Equal(t, http.StatusOK, doReq())
	assert.Equal(t, http.StatusTooManyRequests, doReq())
}

func TestNoPods(t *testing.T) {
	mb := &MockBroadcaster{Result: okResult()}

	router := NewStaticRouter(map[string][2]string{
		"my-cache-invalidation.production": {"production", "my-cache"},
	})
	// PodMap deliberately empty.
	pm := NewPodMap()
	srv := NewServer(":8090", router, pm, mb)

	req := httptest.NewRequest("PURGE", "/product/123", nil)
	req.Host = "my-cache-invalidation.production"
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

// ---------- JSON response shape ----------

func TestJSONResponseShape(t *testing.T) {
	mb := &MockBroadcaster{Result: BroadcastResult{
		Status:    "ok",
		Total:     1,
		Succeeded: 1,
		Results:   []PodResult{{Pod: "10.0.0.1:8080", Status: 200}},
	}}
	srv := newTestServer(mb)

	// Adjust PodMap to a single pod so the Result makes sense.
	pm := NewPodMap()
	pm.Update("production", "my-cache", []string{"10.0.0.1"})
	srv.podMap = pm

	req := httptest.NewRequest("PURGE", "/product/123", nil)
	req.Host = "my-cache-invalidation.production"
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var result BroadcastResult
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&result))
	assert.Equal(t, "ok", result.Status)
	assert.Equal(t, 1, result.Total)
	assert.Equal(t, 1, result.Succeeded)
	require.Len(t, result.Results, 1)
	assert.Equal(t, "10.0.0.1:8080", result.Results[0].Pod)
	assert.Equal(t, 200, result.Results[0].Status)
}

// ---------- partial / failed HTTP codes ----------

func TestHTTPStatusPartial(t *testing.T) {
	mb := &MockBroadcaster{Result: BroadcastResult{
		Status:    "partial",
		Total:     3,
		Succeeded: 1,
	}}
	srv := newTestServer(mb)

	req := httptest.NewRequest("PURGE", "/", nil)
	req.Host = "my-cache-invalidation.production"
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusMultiStatus, rec.Code)
}

func TestHTTPStatusFailed(t *testing.T) {
	mb := &MockBroadcaster{Result: BroadcastResult{
		Status:    "failed",
		Total:     3,
		Succeeded: 0,
	}}
	srv := newTestServer(mb)

	req := httptest.NewRequest("PURGE", "/", nil)
	req.Host = "my-cache-invalidation.production"
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

// ---------- BAN uses agent port ----------

func TestBANUsesAgentPort(t *testing.T) {
	mb := &MockBroadcaster{Result: okResult()}
	srv := newTestServer(mb)

	req := httptest.NewRequest("BAN", "/", nil)
	req.Host = "my-cache-invalidation.production"
	req.Header.Set("X-Ban-Expression", "obj.http.X-Tag ~ article")
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	// All pod addresses should have the agent port.
	for _, pod := range mb.LastPods {
		assert.True(t, strings.HasSuffix(pod, ":"+agentPort),
			"expected pod %q to end with agent port %s", pod, agentPort)
	}
}

// ---------- withPort helper ----------

func TestWithPort(t *testing.T) {
	ips := []string{"10.0.0.1", "10.0.0.2:9090"}
	got := withPort(ips, "8080")
	assert.Equal(t, []string{"10.0.0.1:8080", "10.0.0.2:9090"}, got)
}

// ---------- BAN body forwarded correctly ----------

func TestBANBodyForwarded(t *testing.T) {
	mb := &MockBroadcaster{Result: okResult()}
	srv := newTestServer(mb)

	body := bytes.NewBufferString(`{"expression":"obj.http.X-Cache-Tag ~ news"}`)
	req := httptest.NewRequest(http.MethodPost, "/ban", body)
	req.Host = "my-cache-invalidation.production"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var forwarded banRESTRequest
	require.NoError(t, json.Unmarshal(mb.LastReq.Body, &forwarded))
	assert.Equal(t, "obj.http.X-Cache-Tag ~ news", forwarded.Expression)
}
