/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// podIPFromURL extracts a fake "podIP" usable by the client from a test server URL.
// We replace the real agent URL construction by using the server's listener address.
// The client builds URLs as http://<podIP>:9090/…, so we wrap the server and
// intercept via a custom transport instead.
func TestHTTPAgentClient_PushVCL_Success(t *testing.T) {
	var received struct {
		Name string `json:"name"`
		VCL  string `json:"vcl"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/vcl/push" || r.Method != http.MethodPost {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &HTTPAgentClient{
		HTTPClient: &http.Client{
			Transport: rewriteTransport{base: server.Client().Transport, serverURL: server.URL},
		},
		Token: "tok",
	}

	err := client.PushVCL(context.Background(), "10.0.0.1", "myvcl", "vcl 4.1;")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if received.Name != "myvcl" {
		t.Errorf("expected name %q, got %q", "myvcl", received.Name)
	}
	if received.VCL != "vcl 4.1;" {
		t.Errorf("expected vcl %q, got %q", "vcl 4.1;", received.VCL)
	}
}

func TestHTTPAgentClient_ActiveVCLHash_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/vcl/active" || r.Method != http.MethodGet {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"hash": "deadbeef"})
	}))
	defer server.Close()

	client := &HTTPAgentClient{
		HTTPClient: &http.Client{
			Transport: rewriteTransport{base: server.Client().Transport, serverURL: server.URL},
		},
		Token: "tok",
	}

	hash, err := client.ActiveVCLHash(context.Background(), "10.0.0.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hash != "deadbeef" {
		t.Errorf("expected hash %q, got %q", "deadbeef", hash)
	}
}

func TestHTTPAgentClient_PushVCL_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer server.Close()

	client := &HTTPAgentClient{
		HTTPClient: &http.Client{
			Transport: rewriteTransport{base: server.Client().Transport, serverURL: server.URL},
		},
		Token: "tok",
	}

	err := client.PushVCL(context.Background(), "10.0.0.1", "myvcl", "vcl 4.1;")
	if err == nil {
		t.Fatal("expected error on 500 response, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected error to contain '500', got: %v", err)
	}
}

// rewriteTransport redirects all requests to the test server, regardless of host/port.
type rewriteTransport struct {
	base      http.RoundTripper
	serverURL string
}

func (rt rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Replace scheme+host with the test server's URL.
	req2 := req.Clone(req.Context())
	req2.URL.Scheme = "http"
	// server.URL is e.g. "http://127.0.0.1:PORT" — strip the scheme.
	hostPort := strings.TrimPrefix(rt.serverURL, "http://")
	req2.URL.Host = hostPort
	if rt.base == nil {
		return http.DefaultTransport.RoundTrip(req2)
	}
	return rt.base.RoundTrip(req2)
}
