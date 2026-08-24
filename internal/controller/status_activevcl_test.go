package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
	"github.com/bluedynamics/cloud-vinyl/internal/generator"
)

// TestUpdateStatus_NoPushedPods_DoesNotClaimActiveVCL guards the second
// deadlock found in #77.
//
// The reconciler decides whether to push by comparing the freshly generated
// VCL hash against Status.ActiveVCL.Hash. If the status records a hash after a
// reconcile that pushed to nobody, the next reconcile concludes the VCL is
// already applied and never pushes. The pod then never receives VCL, never
// becomes Ready, and nothing else moves.
//
// This is exactly what happened in CI: one reconcile at pod-creation time when
// no pod was Running yet, then silence.
func TestUpdateStatus_NoPushedPods_DoesNotClaimActiveVCL(t *testing.T) {
	r, vc := statusFixture(t)

	// Nothing reachable yet, so pushVCL reached zero pods.
	r.updateStatus(context.Background(), vc, &generator.Result{Hash: "abc123"}, nil, 0)

	assert.Nil(t, vc.Status.ActiveVCL,
		"status must not claim a VCL is active when it was pushed to no pod")
}

func TestUpdateStatus_PushedPods_RecordsActiveVCL(t *testing.T) {
	r, vc := statusFixture(t)
	peers := []generator.PeerBackend{{Name: "cache_0", IP: "10.0.0.1", Port: 80}}

	r.updateStatus(context.Background(), vc, &generator.Result{Hash: "abc123"}, peers, 1)

	if assert.NotNil(t, vc.Status.ActiveVCL) {
		assert.Equal(t, "abc123", vc.Status.ActiveVCL.Hash)
	}
}

// A pod can be pushed to before it is Ready, which is the whole point of the
// reachable/ready split. The status must record the VCL as active in that
// window, otherwise the reconciler pushes the same VCL on every pass.
func TestUpdateStatus_PushedButNotYetReady_RecordsActiveVCL(t *testing.T) {
	r, vc := statusFixture(t)

	// pushed to 1 pod, but no Ready peers yet
	r.updateStatus(context.Background(), vc, &generator.Result{Hash: "abc123"}, nil, 1)

	if assert.NotNil(t, vc.Status.ActiveVCL) {
		assert.Equal(t, "abc123", vc.Status.ActiveVCL.Hash)
	}
	assert.Equal(t, int32(0), vc.Status.ReadyPeers)
}

// statusFixture builds a reconciler whose status writes land in a fake client,
// which updateStatus needs at the end of its run.
func statusFixture(t *testing.T) (*VinylCacheReconciler, *v1alpha1.VinylCache) {
	t.Helper()
	sch := newScheme(t)
	vc := &v1alpha1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "cache", Namespace: "app"},
		Spec:       v1alpha1.VinylCacheSpec{Replicas: 1},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(vc).WithStatusSubresource(vc).Build()
	return &VinylCacheReconciler{Client: cli, Scheme: sch}, vc
}
