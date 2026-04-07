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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// fakeK8sClientWithToken creates a fake K8s client with a pre-populated agent Secret.
func fakeK8sClientWithToken(namespace, token string) *fake.ClientBuilder {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agentSecretName,
			Namespace: namespace,
		},
		Data: map[string][]byte{
			"agent-token":    []byte(token),
			"varnish-secret": []byte("varnish-test-secret"),
		},
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret)
}

func TestHTTPAgentClient_PushVCL_Success(t *testing.T) {
	var received struct {
		Name string `json:"name"`
		VCL  string `json:"vcl"`
	}
	var receivedAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/vcl/push" || r.Method != http.MethodPost {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		receivedAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	k8sClient := fakeK8sClientWithToken("test-ns", "test-token-123").Build()
	client := &HTTPAgentClient{
		HTTPClient: &http.Client{
			Transport: rewriteTransport{base: server.Client().Transport, serverURL: server.URL},
		},
		K8sClient: k8sClient,
	}

	err := client.PushVCL(context.Background(), "test-ns", "10.0.0.1", "myvcl", "vcl 4.1;")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if received.Name != "myvcl" {
		t.Errorf("expected name %q, got %q", "myvcl", received.Name)
	}
	if received.VCL != "vcl 4.1;" {
		t.Errorf("expected vcl %q, got %q", "vcl 4.1;", received.VCL)
	}
	if receivedAuth != "Bearer test-token-123" {
		t.Errorf("expected auth %q, got %q", "Bearer test-token-123", receivedAuth)
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

	k8sClient := fakeK8sClientWithToken("test-ns", "tok").Build()
	client := &HTTPAgentClient{
		HTTPClient: &http.Client{
			Transport: rewriteTransport{base: server.Client().Transport, serverURL: server.URL},
		},
		K8sClient: k8sClient,
	}

	hash, err := client.ActiveVCLHash(context.Background(), "test-ns", "10.0.0.1")
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

	k8sClient := fakeK8sClientWithToken("test-ns", "tok").Build()
	client := &HTTPAgentClient{
		HTTPClient: &http.Client{
			Transport: rewriteTransport{base: server.Client().Transport, serverURL: server.URL},
		},
		K8sClient: k8sClient,
	}

	err := client.PushVCL(context.Background(), "test-ns", "10.0.0.1", "myvcl", "vcl 4.1;")
	if err == nil {
		t.Fatal("expected error on 500 response, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected error to contain '500', got: %v", err)
	}
}

func TestHTTPAgentClient_PushVCL_MissingSecret(t *testing.T) {
	// No Secret created — should fail with a clear error.
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	client := &HTTPAgentClient{
		HTTPClient: &http.Client{},
		K8sClient:  k8sClient,
	}

	err := client.PushVCL(context.Background(), "missing-ns", "10.0.0.1", "myvcl", "vcl 4.1;")
	if err == nil {
		t.Fatal("expected error when Secret is missing, got nil")
	}
	if !strings.Contains(err.Error(), "agent secret") {
		t.Errorf("expected error about agent secret, got: %v", err)
	}
}

func TestHTTPAgentClient_DifferentNamespaces(t *testing.T) {
	// Two namespaces with different tokens — verify correct token is used per namespace.
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	secret1 := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: agentSecretName, Namespace: "ns-a"},
		Data:       map[string][]byte{"agent-token": []byte("token-a")},
	}
	secret2 := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: agentSecretName, Namespace: "ns-b"},
		Data:       map[string][]byte{"agent-token": []byte("token-b")},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret1, secret2).Build()

	var receivedTokens []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedTokens = append(receivedTokens, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &HTTPAgentClient{
		HTTPClient: &http.Client{
			Transport: rewriteTransport{base: server.Client().Transport, serverURL: server.URL},
		},
		K8sClient: k8sClient,
	}

	_ = client.PushVCL(context.Background(), "ns-a", "10.0.0.1", "v1", "vcl 4.1;")
	_ = client.PushVCL(context.Background(), "ns-b", "10.0.0.2", "v1", "vcl 4.1;")

	if len(receivedTokens) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(receivedTokens))
	}
	if receivedTokens[0] != "Bearer token-a" {
		t.Errorf("ns-a: expected %q, got %q", "Bearer token-a", receivedTokens[0])
	}
	if receivedTokens[1] != "Bearer token-b" {
		t.Errorf("ns-b: expected %q, got %q", "Bearer token-b", receivedTokens[1])
	}
}

// rewriteTransport redirects all requests to the test server, regardless of host/port.
type rewriteTransport struct {
	base      http.RoundTripper
	serverURL string
}

func (rt rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req2 := req.Clone(req.Context())
	req2.URL.Scheme = "http"
	hostPort := strings.TrimPrefix(rt.serverURL, "http://")
	req2.URL.Host = hostPort
	if rt.base == nil {
		return http.DefaultTransport.RoundTrip(req2)
	}
	return rt.base.RoundTrip(req2)
}
