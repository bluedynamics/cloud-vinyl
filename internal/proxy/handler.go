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
	// methodPurge is the HTTP method Varnish expects for a single-URL purge.
	methodPurge = "PURGE"
	// outcomeSuccess and outcomeError are the values of the "result" metric
	// label. Distinct from the "error" key of the JSON error body, which is a
	// wire format detail and deliberately not shared with these.
	outcomeSuccess = "success"
	outcomeError   = "error"
)

// banRESTRequest is the JSON body for POST /ban and for the agent /ban endpoint.
type banRESTRequest struct {
	Expression string `json:"expression"`
}

// xkeyRequest is the JSON body for POST /purge/xkey.
type xkeyRequest struct {
	Keys []string `json:"keys"`
}

// objectsPurgedCapable reports whether typ's pod responses can ever carry
// X-Vinyl-Purged at all. "purge" and "xkey" both hit varnishd directly and
// go through vcl_synth, which sets the header. "ban" is routed to the
// agent's POST /ban (see handleBAN) — a different service on a different
// port that never runs vcl_synth and never sets it, by design. Gating
// ObjectsPurgedUnknownTotal on this keeps that counter a signal for "this
// pod should have told us and didn't," rather than counting every ordinary
// ban response as if something were broken.
func objectsPurgedCapable(typ string) bool {
	return typ == "purge" || typ == "xkey"
}

// recordInvalidation records invalidation + broadcast + partial-failure metrics.
func (s *Server) recordInvalidation(namespace, cacheName, typ string, start time.Time, res BroadcastResult) {
	if s.metrics == nil {
		return
	}
	outcome := outcomeSuccess
	if res.Succeeded == 0 {
		outcome = outcomeError
	}
	s.metrics.InvalidationTotal.WithLabelValues(cacheName, namespace, typ, outcome).Inc()
	s.metrics.InvalidationDuration.Observe(time.Since(start).Seconds())
	// Only add when known: res.ObjectsPurged is nil when no pod reported a
	// parseable count, and a counter has no way to represent "unknown" —
	// adding 0 in that case would look identical to a confirmed empty
	// purge. Leaving the metric flat is exactly the signal #103 wants: a
	// total that never advances while purges keep being issued is visible
	// in a way "always zero because we never asked" was not.
	if res.ObjectsPurged != nil {
		s.metrics.ObjectsPurgedTotal.WithLabelValues(cacheName, namespace, typ).Add(float64(*res.ObjectsPurged))
	}
	for _, pr := range res.Results {
		r := outcomeSuccess
		if pr.Status < 200 || pr.Status >= 300 {
			r = outcomeError
		}
		s.metrics.BroadcastTotal.WithLabelValues(pr.Pod, r).Inc()
		// A pod that answered 2xx but reported no parseable count is a
		// different fact from one that reported a known 0 — the sum above
		// cannot tell the two apart (it just adds a little less), so a
		// regression that drops the header on a subset of pods (e.g. a
		// partial VCL rollout, or the broadcast-path shape #101 was) would
		// otherwise be invisible: the total still climbs, just slower.
		// Track it on its own counter instead. Restricted to types that can
		// ever carry X-Vinyl-Purged in the first place (see
		// objectsPurgedCapable) so BAN — which never sets it, by design,
		// not by regression — doesn't turn this into permanent noise.
		if r == outcomeSuccess && pr.ObjectsPurged == nil && objectsPurgedCapable(typ) {
			s.metrics.ObjectsPurgedUnknownTotal.WithLabelValues(cacheName, namespace, typ).Inc()
		}
	}
	if res.Succeeded > 0 && res.Succeeded < res.Total {
		s.metrics.PartialFailureTotal.WithLabelValues(cacheName, namespace).Inc()
	}
}

// handlePurge broadcasts a PURGE request to all Varnish pod IPs on varnishPort.
func (s *Server) handlePurge(w http.ResponseWriter, r *http.Request, namespace, cacheName string, pods []string) {
	start := time.Now()
	podAddrs := withPort(pods, varnishPort)
	req := BroadcastRequest{
		Method:  methodPurge,
		Path:    r.URL.RequestURI(),
		Host:    r.Host,
		Headers: cloneHeaders(r.Header),
	}

	ctx, cancel := context.WithTimeout(r.Context(), broadcastTimeout)
	defer cancel()

	result := s.broadcaster.Broadcast(ctx, podAddrs, req)
	s.recordInvalidation(namespace, cacheName, "purge", start, result)
	WriteResult(w, result)
}

// handleBAN handles BAN requests (both BAN method and POST /ban).
// It validates the ban expression and broadcasts it to the agent API on each pod.
func (s *Server) handleBAN(w http.ResponseWriter, r *http.Request, namespace, cacheName string, pods []string) {
	start := time.Now()
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
	headers := map[string]string{"Content-Type": "application/json"}
	if token := s.tokenProvider.GetToken(namespace); token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	req := BroadcastRequest{
		Method:  http.MethodPost,
		Path:    "/ban",
		Host:    r.Host,
		Headers: headers,
		Body:    bodyBytes,
	}

	ctx, cancel := context.WithTimeout(r.Context(), broadcastTimeout)
	defer cancel()

	result := s.broadcaster.Broadcast(ctx, podAddrs, req)
	s.recordInvalidation(namespace, cacheName, "ban", start, result)
	WriteResult(w, result)
}

// handleXkey broadcasts PURGE requests with X-Xkey-Purge header for each key.
func (s *Server) handleXkey(w http.ResponseWriter, r *http.Request, namespace, cacheName string, pods []string) {
	start := time.Now()
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
			Method: methodPurge,
			Path:   "/",
			Host:   r.Host,
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
		Status:        status,
		Total:         total,
		Succeeded:     totalSucceeded,
		ObjectsPurged: aggregateObjectsPurged(allResults),
		Results:       allResults,
	}
	s.recordInvalidation(namespace, cacheName, "xkey", start, result)
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
//
// msg can carry request-controlled data (r.Host and r.URL.Path both end up
// here), so it goes through the JSON encoder rather than fmt %q. %q is Go
// quoting, not JSON quoting: on invalid UTF-8 it emits \xNN escapes, which are
// not valid JSON. nosniff keeps a browser from content-sniffing the response
// into HTML despite the explicit Content-Type.
func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
