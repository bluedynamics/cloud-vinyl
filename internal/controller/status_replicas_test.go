package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/bluedynamics/cloud-vinyl/internal/generator"
)

// The replica counts and the selector feed the Replicas printer column and
// the scale subresource (statuspath/selectorpath). They come from the owned
// StatefulSet (#180).
func TestUpdateStatus_CopiesReplicaCountsFromStatefulSet(t *testing.T) {
	r, vc := statusFixture(t)
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: vc.Name, Namespace: vc.Namespace},
		Spec: appsv1.StatefulSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{labelApp: vc.Name}},
		},
		Status: appsv1.StatefulSetStatus{
			Replicas: 3, ReadyReplicas: 2, UpdatedReplicas: 3, AvailableReplicas: 2,
		},
	}
	require.NoError(t, r.Create(context.Background(), sts))

	r.updateStatus(context.Background(), vc, &generator.Result{Hash: "h"}, podObservation{}, 0)

	assert.Equal(t, int32(3), vc.Status.Replicas)
	assert.Equal(t, int32(2), vc.Status.ReadyReplicas)
	assert.Equal(t, int32(3), vc.Status.UpdatedReplicas)
	assert.Equal(t, int32(2), vc.Status.AvailableReplicas)
	assert.Equal(t, "app=cache", vc.Status.Selector)
}

// Without a StatefulSet (not created yet, or held back by RuntimeResolved)
// the counts are 0 and the selector is still set, so /scale always has one.
func TestUpdateStatus_NoStatefulSet_ZeroCountsSelectorSet(t *testing.T) {
	r, vc := statusFixture(t)
	vc.Status.Replicas = 5
	vc.Status.ReadyReplicas = 5

	r.updateStatus(context.Background(), vc, &generator.Result{Hash: "h"}, podObservation{}, 0)

	assert.Equal(t, int32(0), vc.Status.Replicas)
	assert.Equal(t, int32(0), vc.Status.ReadyReplicas)
	assert.Equal(t, "app=cache", vc.Status.Selector)
}
