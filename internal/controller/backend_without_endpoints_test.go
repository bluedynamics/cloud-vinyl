package controller

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	vinylv1 "github.com/bluedynamics/cloud-vinyl/api/v1"
	"github.com/bluedynamics/cloud-vinyl/internal/generator"
)

func TestCalculatePhase_BackendsUnavailable_ReturnsDegraded(t *testing.T) {
	vc := &vinylv1.VinylCache{}
	vc.Status.ReadyPeers = 3
	vc.Status.TotalPeers = 3
	vc.Status.Conditions = []metav1.Condition{
		{Type: vinylv1.ConditionVCLSynced, Status: metav1.ConditionTrue},
		{Type: vinylv1.ConditionBackendsAvailable, Status: metav1.ConditionFalse},
	}
	assert.Equal(t, vinylv1.PhaseDegraded, calculatePhase(vc))
}

func conditionOf(vc *vinylv1.VinylCache, typ string) *metav1.Condition {
	for i := range vc.Status.Conditions {
		if vc.Status.Conditions[i].Type == typ {
			return &vc.Status.Conditions[i]
		}
	}
	return nil
}

func TestUpdateStatus_BackendWithoutEndpoints_DegradedButReady(t *testing.T) {
	r, vc := statusFixture(t)
	peers := []generator.PeerBackend{peer("cache_0", "10.0.0.1")}
	vc.Spec.Replicas = 1

	r.updateStatus(context.Background(), vc, &generator.Result{Hash: "h"},
		podObservation{reachable: peers, ready: peers, backendsWithoutEndpoints: []string{"plone", "thumbor"}}, 1)

	ba := conditionOf(vc, vinylv1.ConditionBackendsAvailable)
	require.NotNil(t, ba)
	assert.Equal(t, metav1.ConditionFalse, ba.Status)
	assert.Equal(t, "BackendWithoutEndpoints", ba.Reason)
	assert.Contains(t, ba.Message, "plone, thumbor")
	// The cache is serving, so it stays Ready.
	ready := conditionOf(vc, vinylv1.ConditionReady)
	require.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionTrue, ready.Status)
	assert.Equal(t, vinylv1.PhaseDegraded, vc.Status.Phase)

	// Endpoints are back: both flip again.
	r.updateStatus(context.Background(), vc, &generator.Result{Hash: "h"},
		podObservation{reachable: peers, ready: peers}, 0)
	ba = conditionOf(vc, vinylv1.ConditionBackendsAvailable)
	assert.Equal(t, metav1.ConditionTrue, ba.Status)
	assert.Equal(t, vinylv1.PhaseReady, vc.Status.Phase)
}

func TestBackendsWithoutEndpoints_KeepsSpecOrder(t *testing.T) {
	vc := &vinylv1.VinylCache{}
	vc.Spec.Backends = []vinylv1.BackendSpec{{Name: "z"}, {Name: "a"}, {Name: "m"}}
	got := backendsWithoutEndpoints(vc, map[string][]generator.Endpoint{
		"z": {}, "a": {{IP: "1.1.1.1", Port: 80}}, "m": nil,
	})
	assert.Equal(t, []string{"z", "m"}, got)
}

// A backend without endpoints used to end the reconcile before the push, so
// the pod never left the bootstrap VCL and never became Ready (#171).
func TestReconcile_BackendWithoutEndpoints_StillPushesVCL(t *testing.T) {
	sch := newScheme(t)
	vc := &vinylv1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "ns1"},
		Spec: vinylv1.VinylCacheSpec{
			Replicas: 1, Image: "vinyl:test",
			Backends: []vinylv1.BackendSpec{{Name: "plone", ServiceRef: vinylv1.ServiceRef{Name: "plone-svc"}}},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "c1-0", Namespace: "ns1", Labels: map[string]string{labelApp: "c1"}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.9"},
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "plone-svc", Namespace: "ns1"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 8080}}},
	}
	es := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name: "plone-svc-x", Namespace: "ns1",
			Labels: map[string]string{discoveryv1.LabelServiceName: "plone-svc"},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
	}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(vc, pod, svc, es).
		WithStatusSubresource(vc).Build()
	agent := &mockAgentClient{}
	r := &VinylCacheReconciler{Client: cli, Scheme: sch, Generator: generator.New(), AgentClient: agent}

	res, err := r.Reconcile(context.Background(),
		ctrl.Request{NamespacedName: client.ObjectKey{Name: "c1", Namespace: "ns1"}})
	require.NoError(t, err)

	assert.Equal(t, 1, agent.pushCalled, "the VCL must be pushed although plone has no endpoints")
	assert.NotEqual(t, 2*time.Second, res.RequeueAfter, "no 2s endpoint requeue")

	got := &vinylv1.VinylCache{}
	require.NoError(t, cli.Get(context.Background(), client.ObjectKey{Name: "c1", Namespace: "ns1"}, got))
	ba := conditionOf(got, vinylv1.ConditionBackendsAvailable)
	require.NotNil(t, ba)
	assert.Equal(t, metav1.ConditionFalse, ba.Status)
	assert.Contains(t, ba.Message, "plone")
}
