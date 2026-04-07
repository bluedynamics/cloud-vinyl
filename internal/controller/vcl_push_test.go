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
	"errors"
	"slices"
	"sync"
	"testing"

	v1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
	"github.com/bluedynamics/cloud-vinyl/internal/generator"
)

// mockAgentClient is a test double for AgentClient.
type mockAgentClient struct {
	mu         sync.Mutex
	pushCalled int
	pushErr    error
	activeHash string
}

func (m *mockAgentClient) PushVCL(_ context.Context, _, _, _, _ string) error {
	m.mu.Lock()
	m.pushCalled++
	m.mu.Unlock()
	return m.pushErr
}

func (m *mockAgentClient) ActiveVCLHash(_ context.Context, _, _ string) (string, error) {
	return m.activeHash, nil
}

func makeReconcilerWithMock(mock AgentClient) *VinylCacheReconciler {
	return &VinylCacheReconciler{
		AgentClient: mock,
	}
}

func makeVC() *v1alpha1.VinylCache {
	vc := &v1alpha1.VinylCache{}
	vc.Namespace = "default"
	vc.Name = "test-cache"
	return vc
}

func makeResult() *generator.Result {
	return &generator.Result{VCL: "vcl 4.1; backend default { .host = \"127.0.0.1\"; }", Hash: "abc123"}
}

func makePeers(n int) []generator.PeerBackend {
	peers := make([]generator.PeerBackend, n)
	for i := range peers {
		peers[i] = generator.PeerBackend{Name: "pod_" + string(rune('0'+i)), IP: "10.0.0." + string(rune('1'+i)), Port: 8080}
	}
	return peers
}

func TestPushVCL_AllPodsSuccess(t *testing.T) {
	mock := &mockAgentClient{pushErr: nil}
	r := makeReconcilerWithMock(mock)

	peers := makePeers(3)
	err := r.pushVCL(context.Background(), makeVC(), makeResult(), peers)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if mock.pushCalled != 3 {
		t.Errorf("expected 3 push calls, got %d", mock.pushCalled)
	}
}

func TestPushVCL_PartialFailure(t *testing.T) {
	customMock := &countingMock{failOn: []int{0}, total: 2}
	r := makeReconcilerWithMock(customMock)

	peers := makePeers(2)
	err := r.pushVCL(context.Background(), makeVC(), makeResult(), peers)
	// Partial failure: not all pods failed, so no error returned.
	if err != nil {
		t.Fatalf("expected nil error on partial failure, got: %v", err)
	}
}

func TestPushVCL_AllPodsFailure_ReturnsError(t *testing.T) {
	mock := &mockAgentClient{pushErr: errors.New("connection refused")}
	r := makeReconcilerWithMock(mock)

	peers := makePeers(2)
	err := r.pushVCL(context.Background(), makeVC(), makeResult(), peers)
	if err == nil {
		t.Fatal("expected error when all pods fail, got nil")
	}
}

// countingMock fails on push calls whose index is in failOn.
type countingMock struct {
	mu     sync.Mutex
	failOn []int
	total  int
	called int
}

func (c *countingMock) PushVCL(_ context.Context, _, _, _, _ string) error {
	c.mu.Lock()
	idx := c.called
	c.called++
	c.mu.Unlock()
	if slices.Contains(c.failOn, idx) {
		return errors.New("mock push error")
	}
	return nil
}

func (c *countingMock) ActiveVCLHash(_ context.Context, _, _ string) (string, error) {
	return "", nil
}
