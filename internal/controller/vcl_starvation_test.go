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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/bluedynamics/cloud-vinyl/internal/generator"
)

// Tests for docs/superpowers/specs/2026-08-25-reconcile-starvation-design.md.
//
// The controller runs a single reconcile worker, so every second spent inside a
// reconcile is a second in which nothing else for this controller happens,
// deletion included. These tests pin the two bounds that keep that time small.

// hangingAgent never answers ActiveVCLName until its context is cancelled. The
// generous cap keeps a regression to a slow failing test rather than a hung one.
type hangingAgent struct {
	mu      sync.Mutex
	queried []string
}

func (a *hangingAgent) PushVCL(_ context.Context, _, _, _, _ string) error { return nil }

func (a *hangingAgent) ActiveVCLName(ctx context.Context, _, podIP string) (string, error) {
	a.mu.Lock()
	a.queried = append(a.queried, podIP)
	a.mu.Unlock()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(2 * time.Second):
		return "", errors.New("mock cap reached: no per-call timeout was applied")
	}
}

// E1: a pod that does not answer must cost the observation phase its own short
// budget, not the 30s of the shared agent client. Three such pods are queried
// sequentially, so without a per-call bound this is three full timeouts.
func TestObserveVCLNames_UnresponsivePodIsBoundedByItsOwnTimeout(t *testing.T) {
	agent := &hangingAgent{}
	r := makeReconcilerWithMock(agent)
	r.ObserveTimeout = 20 * time.Millisecond

	pods := []generator.PeerBackend{
		peer("c_0", "10.0.0.1"), peer("c_1", "10.0.0.2"), peer("c_2", "10.0.0.3"),
	}

	start := time.Now()
	got := r.observeVCLNames(context.Background(), makeVC(), pods)
	elapsed := time.Since(start)

	assert.Less(t, elapsed, time.Second,
		"three unresponsive pods must not hold the reconcile worker; got %s", elapsed)

	for _, p := range pods {
		assert.Equal(t, "", got[p.IP],
			"an unanswered query must leave the pod with an unknown VCL")
	}

	// And an unknown VCL must make it a push target, which is what makes the
	// short timeout safe: nothing is skipped, it is merely pushed again.
	needing := podsNeedingVCL(pods, got, "ns-cache-deadbeef")
	assert.Len(t, needing, 3)
}

// countingAgent records how often each pod was pushed and can fail selected pods.
type countingAgent struct {
	mu      sync.Mutex
	pushes  map[string]int
	failFor map[string]bool
}

func newCountingAgent(failFor ...string) *countingAgent {
	a := &countingAgent{pushes: map[string]int{}, failFor: map[string]bool{}}
	for _, ip := range failFor {
		a.failFor[ip] = true
	}
	return a
}

func (a *countingAgent) PushVCL(_ context.Context, _, podIP, _, _ string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pushes[podIP]++
	if a.failFor[podIP] {
		return errors.New("connection refused")
	}
	return nil
}

func (a *countingAgent) ActiveVCLName(_ context.Context, _, _ string) (string, error) {
	return "boot", nil
}

func (a *countingAgent) count(ip string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pushes[ip]
}

// E2: one attempt per pod per reconcile. Retrying is the reconcile loop's job.
// A failed push leaves the pod without the desired VCL, so podsNeedingVCL picks
// it up again next time round, and the workqueue rate-limits that for us.
func TestPushVCL_MakesOneAttemptPerPod(t *testing.T) {
	agent := newCountingAgent("10.0.0.1", "10.0.0.2")
	r := makeReconcilerWithMock(agent)

	vc := makeVC()
	//nolint:staticcheck // deliberately exercising the deprecated field to prove it is inert
	vc.Spec.Retry.MaxAttempts = 3
	vc.Spec.Retry.BackoffBase = metav1.Duration{Duration: 5 * time.Second}

	start := time.Now()
	_, err := r.pushVCL(context.Background(), vc, &generator.Result{VCL: "vcl 4.1;"},
		[]generator.PeerBackend{peer("c_0", "10.0.0.1"), peer("c_1", "10.0.0.2")})
	elapsed := time.Since(start)

	require.Error(t, err, "all pods failed, so the push reports an error")
	assert.Equal(t, 1, agent.count("10.0.0.1"))
	assert.Equal(t, 1, agent.count("10.0.0.2"))
	assert.Less(t, elapsed, time.Second,
		"a failed push must not sleep inside the reconcile; got %s", elapsed)
}

// E3: maxAttempts no longer changes anything. Kept as a guard so the field
// cannot quietly come back to life while it is documented as inert.
func TestPushVCL_MaxAttemptsIsInert(t *testing.T) {
	agent := newCountingAgent("10.0.0.1")
	r := makeReconcilerWithMock(agent)

	vc := makeVC()
	//nolint:staticcheck // deliberately exercising the deprecated field to prove it is inert
	vc.Spec.Retry.MaxAttempts = 7

	_, _ = r.pushVCL(context.Background(), vc, &generator.Result{VCL: "vcl 4.1;"},
		[]generator.PeerBackend{peer("c_0", "10.0.0.1")})

	assert.Equal(t, 1, agent.count("10.0.0.1"),
		"maxAttempts is documented as having no effect")
}

// A partial failure must report only the pods that actually took the VCL.
// The caller records those as carrying it, and recording a pod that refused
// would make the status claim convergence that did not happen. This matters
// more once the inner retry loop is gone, because a single attempt fails more
// readily than three.
func TestPushVCL_PartialFailureReportsOnlySucceededPods(t *testing.T) {
	agent := newCountingAgent("10.0.0.2")
	r := makeReconcilerWithMock(agent)

	pushed, err := r.pushVCL(context.Background(), makeVC(), &generator.Result{VCL: "vcl 4.1;"},
		[]generator.PeerBackend{peer("c_0", "10.0.0.1"), peer("c_1", "10.0.0.2")})

	require.NoError(t, err, "a partial failure is not an error: some pods converged")
	require.Len(t, pushed, 1)
	assert.Equal(t, "10.0.0.1", pushed[0].IP,
		"only the pod that accepted the VCL may be reported as pushed")
}
