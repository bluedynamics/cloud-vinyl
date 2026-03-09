package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// BroadcastRequest describes the request to fan out to all pods.
type BroadcastRequest struct {
	Method  string
	Path    string
	Headers map[string]string
	Body    []byte
}

// BroadcastResult aggregates the outcomes from all pod calls.
type BroadcastResult struct {
	Status    string      `json:"status"` // "ok" | "partial" | "failed"
	Total     int         `json:"total"`
	Succeeded int         `json:"succeeded"`
	Results   []PodResult `json:"results"`
}

// PodResult holds the result of a single pod call.
type PodResult struct {
	Pod    string `json:"pod"`
	Status int    `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Broadcaster sends a request to a set of pods in parallel and aggregates results.
type Broadcaster interface {
	Broadcast(ctx context.Context, pods []string, req BroadcastRequest) BroadcastResult
}

// HTTPBroadcaster implements Broadcaster using plain HTTP calls.
type HTTPBroadcaster struct {
	client  *http.Client
	timeout time.Duration
}

// NewHTTPBroadcaster creates a new HTTPBroadcaster.
// timeout is the per-pod deadline.
func NewHTTPBroadcaster(timeout time.Duration) *HTTPBroadcaster {
	return &HTTPBroadcaster{
		client:  &http.Client{Timeout: timeout},
		timeout: timeout,
	}
}

// Broadcast fans the request out to every pod in parallel, collects results,
// then returns an aggregated BroadcastResult.
func (b *HTTPBroadcaster) Broadcast(ctx context.Context, pods []string, req BroadcastRequest) BroadcastResult {
	results := make([]PodResult, len(pods))
	var wg sync.WaitGroup

	for i, pod := range pods {
		wg.Go(func() {
			results[i] = b.callPod(ctx, pod, req)
		})
	}
	wg.Wait()

	succeeded := 0
	for _, r := range results {
		if r.Error == "" && r.Status >= 200 && r.Status < 300 {
			succeeded++
		}
	}

	status := statusString(len(pods), succeeded)
	return BroadcastResult{
		Status:    status,
		Total:     len(pods),
		Succeeded: succeeded,
		Results:   results,
	}
}

func (b *HTTPBroadcaster) callPod(ctx context.Context, pod string, req BroadcastRequest) PodResult {
	url := fmt.Sprintf("http://%s%s", pod, req.Path)

	var bodyReader io.Reader
	if len(req.Body) > 0 {
		bodyReader = bytes.NewReader(req.Body)
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.Method, url, bodyReader)
	if err != nil {
		return PodResult{Pod: pod, Error: fmt.Sprintf("build request: %s", err)}
	}

	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := b.client.Do(httpReq)
	if err != nil {
		return PodResult{Pod: pod, Error: err.Error()}
	}
	defer resp.Body.Close()
	// Drain body to allow connection reuse.
	io.Copy(io.Discard, resp.Body) //nolint:errcheck

	return PodResult{Pod: pod, Status: resp.StatusCode}
}

// statusString maps (total, succeeded) to a result status string.
func statusString(total, succeeded int) string {
	switch {
	case total == 0 || succeeded == total:
		return "ok"
	case succeeded == 0:
		return "failed"
	default:
		return "partial"
	}
}

// WriteResult marshals a BroadcastResult to w as JSON and sets the HTTP status.
func WriteResult(w http.ResponseWriter, result BroadcastResult) {
	code := httpStatusCode(result)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(result) //nolint:errcheck
}

// httpStatusCode converts a BroadcastResult status to an HTTP status code.
func httpStatusCode(result BroadcastResult) int {
	switch result.Status {
	case "ok":
		return http.StatusOK
	case "partial":
		return http.StatusMultiStatus // 207
	default:
		return http.StatusServiceUnavailable // 503
	}
}
