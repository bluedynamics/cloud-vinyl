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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// agentPort is the port on which the vinyl-agent HTTP API listens.
	agentPort = 9090
	// varnishPort is the main HTTP port on which Varnish accepts traffic.
	varnishPort = 8080
	// agentSecretName is the fixed Secret name used in every namespace.
	agentSecretName = "cloud-vinyl-agent-token" //nolint:gosec // Secret name, not a credential
)

// AgentClient abstracts the vinyl-agent HTTP API.
type AgentClient interface {
	// PushVCL pushes a named VCL program to the agent running on podIP.
	// The namespace is used to resolve the per-namespace agent token.
	PushVCL(ctx context.Context, namespace, podIP, name, vcl string) error

	// ActiveVCLHash returns the SHA-256 hash of the VCL currently active on podIP.
	ActiveVCLHash(ctx context.Context, namespace, podIP string) (string, error)
}

// HTTPAgentClient implements AgentClient using the vinyl-agent HTTP API.
// It reads the agent token per-namespace from Kubernetes Secrets.
type HTTPAgentClient struct {
	HTTPClient *http.Client
	K8sClient  client.Reader
}

type pushVCLRequest struct {
	Name string `json:"name"`
	VCL  string `json:"vcl"`
}

type activeVCLResponse struct {
	Hash string `json:"hash"`
}

// readToken reads the agent-token from the per-namespace Secret.
func (c *HTTPAgentClient) readToken(ctx context.Context, namespace string) (string, error) {
	secret := &corev1.Secret{}
	key := types.NamespacedName{Name: agentSecretName, Namespace: namespace}
	if err := c.K8sClient.Get(ctx, key, secret); err != nil {
		return "", fmt.Errorf("reading agent secret %s/%s: %w", namespace, agentSecretName, err)
	}
	token, ok := secret.Data["agent-token"]
	if !ok {
		return "", fmt.Errorf("agent secret %s/%s missing 'agent-token' key", namespace, agentSecretName)
	}
	return string(token), nil
}

// PushVCL sends a VCL push request to the agent on the given pod IP.
func (c *HTTPAgentClient) PushVCL(ctx context.Context, namespace, podIP, name, vcl string) error {
	token, err := c.readToken(ctx, namespace)
	if err != nil {
		return err
	}

	body, err := json.Marshal(pushVCLRequest{Name: name, VCL: vcl})
	if err != nil {
		return fmt.Errorf("marshaling push request: %w", err)
	}

	url := fmt.Sprintf("http://%s:%d/vcl/push", podIP, agentPort)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating push request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending push request to %s: %w", podIP, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("agent push returned HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// ActiveVCLHash returns the hash of the currently active VCL on the given pod.
func (c *HTTPAgentClient) ActiveVCLHash(ctx context.Context, namespace, podIP string) (string, error) {
	token, err := c.readToken(ctx, namespace)
	if err != nil {
		return "", err
	}

	url := fmt.Sprintf("http://%s:%d/vcl/active", podIP, agentPort)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("creating active-vcl request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching active VCL hash from %s: %w", podIP, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("agent active-vcl returned HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var result activeVCLResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decoding active-vcl response from %s: %w", podIP, err)
	}

	return result.Hash, nil
}
