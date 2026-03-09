package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// Handler holds all HTTP handlers for the agent.
type Handler struct {
	admin      AdminClient
	xkeyPurger *XkeyPurger
}

// NewHandler creates a new Handler.
func NewHandler(admin AdminClient, xkey *XkeyPurger) *Handler {
	return &Handler{admin: admin, xkeyPurger: xkey}
}

type pushVCLRequest struct {
	Name string `json:"name"`
	VCL  string `json:"vcl"`
}

type validateVCLRequest struct {
	VCL string `json:"vcl"`
}

type banRequest struct {
	Expression string `json:"expression"`
}

type xkeyPurgeRequest struct {
	Keys []string `json:"keys"`
	Soft bool     `json:"soft,omitempty"`
}

type statusResponse struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
	Line    int    `json:"line,omitempty"`
}

type activeVCLResponse struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type xkeyPurgeResponse struct {
	Status string `json:"status"`
	Purged int    `json:"purged"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

// PushVCL handles POST /vcl/push
func (h *Handler) PushVCL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req pushVCLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, statusResponse{Status: "error", Message: "invalid JSON"})
		return
	}
	if req.Name == "" || req.VCL == "" {
		writeJSON(w, http.StatusBadRequest, statusResponse{Status: "error", Message: "name and vcl are required"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	if err := h.admin.PushVCL(ctx, req.Name, req.VCL); err != nil {
		writeJSON(w, http.StatusInternalServerError, statusResponse{Status: "error", Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, statusResponse{Status: "ok"})
}

// ValidateVCL handles POST /vcl/validate
func (h *Handler) ValidateVCL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req validateVCLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, statusResponse{Status: "error", Message: "invalid JSON"})
		return
	}
	if req.VCL == "" {
		writeJSON(w, http.StatusBadRequest, statusResponse{Status: "error", Message: "vcl is required"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	result, err := h.admin.ValidateVCL(ctx, "validate_tmp", req.VCL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, statusResponse{Status: "error", Message: err.Error()})
		return
	}
	if !result.Valid {
		writeJSON(w, http.StatusBadRequest, statusResponse{Status: "error", Message: result.Message, Line: result.Line})
		return
	}
	writeJSON(w, http.StatusOK, statusResponse{Status: "ok"})
}

// ActiveVCL handles GET /vcl/active
func (h *Handler) ActiveVCL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	name, err := h.admin.ActiveVCL(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, statusResponse{Status: "error", Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, activeVCLResponse{Name: name, Status: "active"})
}

// Ban handles POST /ban
func (h *Handler) Ban(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req banRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, statusResponse{Status: "error", Message: "invalid JSON"})
		return
	}
	if req.Expression == "" {
		writeJSON(w, http.StatusBadRequest, statusResponse{Status: "error", Message: "expression is required"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	if err := h.admin.Ban(ctx, req.Expression); err != nil {
		writeJSON(w, http.StatusInternalServerError, statusResponse{Status: "error", Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, statusResponse{Status: "ok"})
}

// PurgeXkey handles POST /purge/xkey
func (h *Handler) PurgeXkey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req xkeyPurgeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, statusResponse{Status: "error", Message: "invalid JSON"})
		return
	}
	if len(req.Keys) == 0 {
		writeJSON(w, http.StatusBadRequest, statusResponse{Status: "error", Message: "keys array is required"})
		return
	}

	purged, err := h.xkeyPurger.Purge(r.Context(), req.Keys, req.Soft)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, statusResponse{Status: "error", Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, xkeyPurgeResponse{Status: "ok", Purged: purged})
}

// Health handles GET /health (no auth required)
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	_, err := h.admin.ActiveVCL(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "error", "varnish": "not responding"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "varnish": "running"})
}
