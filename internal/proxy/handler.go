package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	varnishPort      = "8080" // Varnish HTTP port for PURGE/xkey
	agentPort        = "9090" // vinyl-agent API port for BAN
	broadcastTimeout = 10 * time.Second
)

// banRESTRequest is the JSON body for POST /ban and for the agent /ban endpoint.
type banRESTRequest struct {
	Expression string `json:"expression"`
}

// xkeyRequest is the JSON body for POST /purge/xkey.
type xkeyRequest struct {
	Keys []string `json:"keys"`
}

// handlePurge broadcasts a PURGE request to all Varnish pod IPs on varnishPort.
func (s *Server) handlePurge(w http.ResponseWriter, r *http.Request, pods []string) {
	podAddrs := withPort(pods, varnishPort)
	req := BroadcastRequest{
		Method:  "PURGE",
		Path:    r.URL.RequestURI(),
		Headers: cloneHeaders(r.Header),
	}

	ctx, cancel := context.WithTimeout(r.Context(), broadcastTimeout)
	defer cancel()

	result := s.broadcaster.Broadcast(ctx, podAddrs, req)
	WriteResult(w, result)
}

// handleBAN handles BAN requests (both BAN method and POST /ban).
// It validates the ban expression and broadcasts it to the agent API on each pod.
func (s *Server) handleBAN(w http.ResponseWriter, r *http.Request, pods []string) {
	var expression string

	switch r.Method {
	case "BAN":
		expression = r.Header.Get("X-Ban-Expression")
		if expression == "" {
			writeJSONError(w, http.StatusBadRequest, "X-Ban-Expression header is required")
			return
		}
	case http.MethodPost:
		var body banRESTRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		expression = body.Expression
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if expression == "" {
		writeJSONError(w, http.StatusBadRequest, "ban expression must not be empty")
		return
	}

	if err := ValidateBanExpression(expression); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid ban expression: %s", err))
		return
	}

	podAddrs := withPort(pods, agentPort)
	bodyBytes, _ := json.Marshal(banRESTRequest{Expression: expression})
	req := BroadcastRequest{
		Method:  http.MethodPost,
		Path:    "/ban",
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    bodyBytes,
	}

	ctx, cancel := context.WithTimeout(r.Context(), broadcastTimeout)
	defer cancel()

	result := s.broadcaster.Broadcast(ctx, podAddrs, req)
	WriteResult(w, result)
}

// handleXkey broadcasts PURGE requests with X-Xkey-Purge header for each key.
func (s *Server) handleXkey(w http.ResponseWriter, r *http.Request, pods []string) {
	var body xkeyRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(body.Keys) == 0 {
		writeJSONError(w, http.StatusBadRequest, "keys array is required and must not be empty")
		return
	}

	podAddrs := withPort(pods, varnishPort)

	ctx, cancel := context.WithTimeout(r.Context(), broadcastTimeout)
	defer cancel()

	// Broadcast one PURGE per xkey, accumulating all results.
	var allResults []PodResult
	totalSucceeded := 0
	for _, key := range body.Keys {
		req := BroadcastRequest{
			Method: "PURGE",
			Path:   "/",
			Headers: map[string]string{
				"X-Xkey-Purge": key,
			},
		}
		res := s.broadcaster.Broadcast(ctx, podAddrs, req)
		allResults = append(allResults, res.Results...)
		totalSucceeded += res.Succeeded
	}

	total := len(pods) * len(body.Keys)
	status := statusString(total, totalSucceeded)
	result := BroadcastResult{
		Status:    status,
		Total:     total,
		Succeeded: totalSucceeded,
		Results:   allResults,
	}
	WriteResult(w, result)
}

// withPort appends ":port" to each IP that doesn't already have a port.
func withPort(ips []string, port string) []string {
	out := make([]string, len(ips))
	for i, ip := range ips {
		if strings.Contains(ip, ":") {
			out[i] = ip
		} else {
			out[i] = ip + ":" + port
		}
	}
	return out
}

// cloneHeaders copies header values from an http.Header to a plain map.
func cloneHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, vs := range h {
		out[k] = strings.Join(vs, ", ")
	}
	return out
}

// writeJSONError writes a plain JSON error response.
func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = fmt.Fprintf(w, `{"error":%q}`, msg)
}
