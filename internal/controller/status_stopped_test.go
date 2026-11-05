package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	vinylv1 "github.com/bluedynamics/cloud-vinyl/api/v1"
	"github.com/bluedynamics/cloud-vinyl/internal/generator"
)

func TestCalculatePhase_ScaledToZero_StoppedBeatsDegraded(t *testing.T) {
	vc := &vinylv1.VinylCache{}
	vc.Status.Conditions = []metav1.Condition{
		{Type: vinylv1.ConditionReady, Status: metav1.ConditionTrue, Reason: reasonScaledToZero},
		{Type: vinylv1.ConditionVCLSynced, Status: metav1.ConditionFalse},
		{Type: vinylv1.ConditionBackendsAvailable, Status: metav1.ConditionFalse},
	}
	assert.Equal(t, vinylv1.PhaseStopped, calculatePhase(vc))
}

func TestUpdateStatus_ScaledToZero(t *testing.T) {
	for _, missing := range [][]string{nil, {"plone"}} {
		r, vc := statusFixture(t)
		vc.Spec.Replicas = 0

		r.updateStatus(context.Background(), vc, &generator.Result{Hash: "h"},
			podObservation{backendsWithoutEndpoints: missing}, 0)

		assert.Equal(t, vinylv1.PhaseStopped, vc.Status.Phase)
		for _, typ := range []string{vinylv1.ConditionReady, vinylv1.ConditionVCLSynced, vinylv1.ConditionProgressing} {
			c := conditionOf(vc, typ)
			require.NotNil(t, c, typ)
			assert.Equal(t, "ScaledToZero", c.Reason, typ)
		}
		ready := conditionOf(vc, vinylv1.ConditionReady)
		assert.Equal(t, metav1.ConditionTrue, ready.Status)
		assert.Equal(t, "scaled to zero replicas", ready.Message)
		assert.Equal(t, metav1.ConditionTrue, conditionOf(vc, vinylv1.ConditionVCLSynced).Status)
		assert.Equal(t, metav1.ConditionFalse, conditionOf(vc, vinylv1.ConditionProgressing).Status)

		// BackendsAvailable stays truthful.
		ba := conditionOf(vc, vinylv1.ConditionBackendsAvailable)
		require.NotNil(t, ba)
		if len(missing) > 0 {
			assert.Equal(t, metav1.ConditionFalse, ba.Status)
		} else {
			assert.Equal(t, metav1.ConditionTrue, ba.Status)
		}
	}
}

func TestUpdateStatus_ScaleUpLeavesStopped(t *testing.T) {
	r, vc := statusFixture(t)
	vc.Spec.Replicas = 0
	r.updateStatus(context.Background(), vc, &generator.Result{Hash: "h"}, podObservation{}, 0)
	require.Equal(t, vinylv1.PhaseStopped, vc.Status.Phase)

	// Scaled up, pod not ready yet.
	vc.Spec.Replicas = 1
	r.updateStatus(context.Background(), vc, &generator.Result{Hash: "h"}, podObservation{}, 0)
	assert.Equal(t, vinylv1.PhasePending, vc.Status.Phase)
	ready := conditionOf(vc, vinylv1.ConditionReady)
	assert.Equal(t, metav1.ConditionFalse, ready.Status)
	assert.Equal(t, "NotAllReady", ready.Reason)
	assert.Equal(t, "WaitingForReplicas", conditionOf(vc, vinylv1.ConditionProgressing).Reason)
	assert.Equal(t, "VCLPushed", conditionOf(vc, vinylv1.ConditionVCLSynced).Reason)

	// Pod ready.
	peers := []generator.PeerBackend{peer("cache_0", "10.0.0.1")}
	r.updateStatus(context.Background(), vc, &generator.Result{Hash: "h"},
		podObservation{reachable: peers, ready: peers}, 1)
	assert.Equal(t, vinylv1.PhaseReady, vc.Status.Phase)
}
