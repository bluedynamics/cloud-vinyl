package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bluedynamics/cloud-vinyl/internal/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockAdmin struct {
	pushVCLFn     func(ctx context.Context, name, vcl string) error
	validateVCLFn func(ctx context.Context, name, vcl string) (agent.ValidationResult, error)
	activeVCLFn   func(ctx context.Context) (string, error)
	banFn         func(ctx context.Context, expression string) error
	discardVCLFn  func(ctx context.Context, name string) error
}

func (m *mockAdmin) PushVCL(ctx context.Context, name, vcl string) error {
	if m.pushVCLFn != nil {
		return m.pushVCLFn(ctx, name, vcl)
	}
	return nil
}

func (m *mockAdmin) ValidateVCL(ctx context.Context, name, vcl string) (agent.ValidationResult, error) {
	if m.validateVCLFn != nil {
		return m.validateVCLFn(ctx, name, vcl)
	}
	return agent.ValidationResult{Valid: true}, nil
}

func (m *mockAdmin) ActiveVCL(ctx context.Context) (string, error) {
	if m.activeVCLFn != nil {
		return m.activeVCLFn(ctx)
	}
	return "boot", nil
}

func (m *mockAdmin) Ban(ctx context.Context, expression string) error {
	if m.banFn != nil {
		return m.banFn(ctx, expression)
	}
	return nil
}

func (m *mockAdmin) DiscardVCL(ctx context.Context, name string) error {
	if m.discardVCLFn != nil {
		return m.discardVCLFn(ctx, name)
	}
	return nil
}

func newTestHandler() (*agent.Handler, *mockAdmin) {
	mock := &mockAdmin{}
	xkey := agent.NewXkeyPurger("http://127.0.0.1:8080")
	return agent.NewHandler(mock, xkey), mock
}

func TestPushVCL_Success(t *testing.T) {
	h, _ := newTestHandler()
	body := `{"name":"test_vcl","vcl":"vcl 4.1; default: return(pass);"}`
	req := httptest.NewRequest(http.MethodPost, "/vcl/push", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.PushVCL(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "ok", resp["status"])
}

func TestPushVCL_EmptyBody_Returns400(t *testing.T) {
	h, _ := newTestHandler()
	body := `{"name":"","vcl":""}`
	req := httptest.NewRequest(http.MethodPost, "/vcl/push", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	h.PushVCL(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestPushVCL_InvalidJSON_Returns400(t *testing.T) {
	h, _ := newTestHandler()
	req := httptest.NewRequest(http.MethodPost, "/vcl/push", bytes.NewBufferString("not json"))
	rr := httptest.NewRecorder()
	h.PushVCL(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestPushVCL_AdminError_Returns500(t *testing.T) {
	h, mock := newTestHandler()
	mock.pushVCLFn = func(ctx context.Context, name, vcl string) error {
		return assert.AnError
	}
	body := `{"name":"test_vcl","vcl":"vcl 4.1;"}`
	req := httptest.NewRequest(http.MethodPost, "/vcl/push", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	h.PushVCL(rr, req)
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

func TestPushVCL_WrongMethod_Returns405(t *testing.T) {
	h, _ := newTestHandler()
	req := httptest.NewRequest(http.MethodGet, "/vcl/push", nil)
	rr := httptest.NewRecorder()
	h.PushVCL(rr, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestBan_Success(t *testing.T) {
	h, _ := newTestHandler()
	body := `{"expression":"obj.http.X-Url ~ ^/product/"}`
	req := httptest.NewRequest(http.MethodPost, "/ban", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	h.Ban(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestBan_EmptyExpression_Returns400(t *testing.T) {
	h, _ := newTestHandler()
	body := `{"expression":""}`
	req := httptest.NewRequest(http.MethodPost, "/ban", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	h.Ban(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestBan_InvalidJSON_Returns400(t *testing.T) {
	h, _ := newTestHandler()
	req := httptest.NewRequest(http.MethodPost, "/ban", bytes.NewBufferString("not json"))
	rr := httptest.NewRecorder()
	h.Ban(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestBan_WrongMethod_Returns405(t *testing.T) {
	h, _ := newTestHandler()
	req := httptest.NewRequest(http.MethodGet, "/ban", nil)
	rr := httptest.NewRecorder()
	h.Ban(rr, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestActiveVCL_Success(t *testing.T) {
	h, mock := newTestHandler()
	mock.activeVCLFn = func(ctx context.Context) (string, error) {
		return "cloud_vinyl_v1", nil
	}
	req := httptest.NewRequest(http.MethodGet, "/vcl/active", nil)
	rr := httptest.NewRecorder()
	h.ActiveVCL(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "cloud_vinyl_v1", resp["name"])
	assert.Equal(t, "active", resp["status"])
}

func TestActiveVCL_WrongMethod_Returns405(t *testing.T) {
	h, _ := newTestHandler()
	req := httptest.NewRequest(http.MethodPost, "/vcl/active", nil)
	rr := httptest.NewRecorder()
	h.ActiveVCL(rr, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestValidateVCL_ValidSyntax_Returns200(t *testing.T) {
	h, _ := newTestHandler() // default mock returns Valid: true
	body := `{"vcl":"vcl 4.1;"}`
	req := httptest.NewRequest(http.MethodPost, "/vcl/validate", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	h.ValidateVCL(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestValidateVCL_InvalidSyntax_Returns400WithLine(t *testing.T) {
	h, mock := newTestHandler()
	mock.validateVCLFn = func(ctx context.Context, name, vcl string) (agent.ValidationResult, error) {
		return agent.ValidationResult{Valid: false, Message: "syntax error at Line 3:", Line: 3}, nil
	}
	body := `{"vcl":"invalid vcl here"}`
	req := httptest.NewRequest(http.MethodPost, "/vcl/validate", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	h.ValidateVCL(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "error", resp["status"])
	assert.Equal(t, float64(3), resp["line"])
}

func TestValidateVCL_EmptyVCL_Returns400(t *testing.T) {
	h, _ := newTestHandler()
	body := `{"vcl":""}`
	req := httptest.NewRequest(http.MethodPost, "/vcl/validate", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	h.ValidateVCL(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestValidateVCL_WrongMethod_Returns405(t *testing.T) {
	h, _ := newTestHandler()
	req := httptest.NewRequest(http.MethodGet, "/vcl/validate", nil)
	rr := httptest.NewRecorder()
	h.ValidateVCL(rr, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestHealth_VarnishRunning_Returns200(t *testing.T) {
	h, _ := newTestHandler() // default mock returns "boot", nil
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	h.Health(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "ok", resp["status"])
	assert.Equal(t, "running", resp["varnish"])
}

func TestHealth_VarnishDown_Returns503(t *testing.T) {
	h, mock := newTestHandler()
	mock.activeVCLFn = func(ctx context.Context) (string, error) {
		return "", assert.AnError
	}
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	h.Health(rr, req)
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestPurgeXkey_EmptyKeys_Returns400(t *testing.T) {
	h, _ := newTestHandler()
	body := `{"keys":[]}`
	req := httptest.NewRequest(http.MethodPost, "/purge/xkey", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	h.PurgeXkey(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestPurgeXkey_InvalidJSON_Returns400(t *testing.T) {
	h, _ := newTestHandler()
	req := httptest.NewRequest(http.MethodPost, "/purge/xkey", bytes.NewBufferString("not json"))
	rr := httptest.NewRecorder()
	h.PurgeXkey(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestPurgeXkey_WrongMethod_Returns405(t *testing.T) {
	h, _ := newTestHandler()
	req := httptest.NewRequest(http.MethodGet, "/purge/xkey", nil)
	rr := httptest.NewRecorder()
	h.PurgeXkey(rr, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}
