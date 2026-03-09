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
)

const (
	// agentPort is the port on which the vinyl-agent HTTP API listens.
	agentPort = 9090
	// varnishPort is the main HTTP port on which Varnish accepts traffic.
	varnishPort = 8080
)

// AgentClient abstracts the vinyl-agent HTTP API.
type AgentClient interface {
	// PushVCL pushes a named VCL program to the agent running on podIP.
	PushVCL(ctx context.Context, podIP string, name, vcl string) error

	// ActiveVCLHash returns the SHA-256 hash of the VCL currently active on podIP.
	ActiveVCLHash(ctx context.Context, podIP string) (string, error)
}

// HTTPAgentClient implements AgentClient using the vinyl-agent HTTP API.
//
// POST http://<podIP>:9090/vcl/push   {"name": name, "vcl": vcl}
// GET  http://<podIP>:9090/vcl/active → {"hash": "..."}
type HTTPAgentClient struct {
	HTTPClient *http.Client
	Token      string
}

type pushVCLRequest struct {
	Name string `json:"name"`
	VCL  string `json:"vcl"`
}

type activeVCLResponse struct {
	Hash string `json:"hash"`
}

// PushVCL sends a VCL push request to the agent on the given pod IP.
func (c *HTTPAgentClient) PushVCL(ctx context.Context, podIP string, name, vcl string) error {
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
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

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
func (c *HTTPAgentClient) ActiveVCLHash(ctx context.Context, podIP string) (string, error) {
	url := fmt.Sprintf("http://%s:%d/vcl/active", podIP, agentPort)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("creating active-vcl request: %w", err)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

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
