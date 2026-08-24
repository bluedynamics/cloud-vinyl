package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
)

// pod builds a StatefulSet member of "cache" in namespace "app".
func pod(name, ip string, phase corev1.PodPhase, ready bool) *corev1.Pod {
	cond := corev1.ConditionFalse
	if ready {
		cond = corev1.ConditionTrue
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "app",
			Labels:    map[string]string{"app": "cache"},
		},
		Status: corev1.PodStatus{
			Phase:      phase,
			PodIP:      ip,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: cond}},
		},
	}
}

func peersFixture(t *testing.T, pods ...*corev1.Pod) (*VinylCacheReconciler, *v1alpha1.VinylCache) {
	t.Helper()
	sch := newScheme(t)
	b := fake.NewClientBuilder().WithScheme(sch)
	for _, p := range pods {
		b = b.WithObjects(p)
	}
	r := &VinylCacheReconciler{Client: b.Build(), Scheme: sch}
	vc := &v1alpha1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "cache", Namespace: "app"},
	}
	return r, vc
}

// TestCollectPeers_PushTargetsIncludeNotReadyPods is the fix for the deadlock
// found in #77: a pod is NotReady precisely *because* it has no operator VCL
// yet, so gating the push on Ready means the operator never pushes and the pod
// never becomes Ready. The operator reaches the agent over the pod IP, where
// readiness is irrelevant, so reachability is the correct gate for push targets.
func TestCollectPeers_PushTargetsIncludeNotReadyPods(t *testing.T) {
	r, vc := peersFixture(t,
		pod("cache-0", "10.0.0.1", corev1.PodRunning, false),
		pod("cache-1", "10.0.0.2", corev1.PodRunning, true),
	)

	obs, err := r.collectPeers(context.Background(), vc)
	reachable := obs.reachable
	require.NoError(t, err)

	ips := make([]string, 0, len(reachable))
	for _, p := range reachable {
		ips = append(ips, p.IP)
	}
	assert.ElementsMatch(t, []string{"10.0.0.1", "10.0.0.2"}, ips,
		"a running pod with an IP is a valid push target even when NotReady")
}

// TestCollectPeers_ShardPeersOnlyReady keeps the other half honest: a pod that
// is not Ready must not be sharded to, so it must not appear as a peer backend
// in the generated VCL or in the proxy pod map.
func TestCollectPeers_ShardPeersOnlyReady(t *testing.T) {
	r, vc := peersFixture(t,
		pod("cache-0", "10.0.0.1", corev1.PodRunning, false),
		pod("cache-1", "10.0.0.2", corev1.PodRunning, true),
	)

	obs, err := r.collectPeers(context.Background(), vc)
	ready := obs.ready
	require.NoError(t, err)

	ips := make([]string, 0, len(ready))
	for _, p := range ready {
		ips = append(ips, p.IP)
	}
	assert.Equal(t, []string{"10.0.0.2"}, ips,
		"only Ready pods may receive sharded traffic")
}

func TestCollectPeers_SkipsPodsWithoutIP(t *testing.T) {
	r, vc := peersFixture(t,
		pod("cache-0", "", corev1.PodPending, false),
		pod("cache-1", "10.0.0.2", corev1.PodRunning, true),
	)

	obs, err := r.collectPeers(context.Background(), vc)
	reachable, ready := obs.reachable, obs.ready
	require.NoError(t, err)

	assert.Len(t, reachable, 1, "a pod without an IP cannot be reached")
	assert.Len(t, ready, 1)
	assert.Equal(t, "10.0.0.2", reachable[0].IP)
}

// A terminating or crashed pod still carries its last PodIP, but nothing is
// listening there. Pushing to it wastes the whole retry budget.
func TestCollectPeers_SkipsNonRunningPods(t *testing.T) {
	r, vc := peersFixture(t,
		pod("cache-0", "10.0.0.1", corev1.PodSucceeded, false),
		pod("cache-1", "10.0.0.2", corev1.PodFailed, false),
		pod("cache-2", "10.0.0.3", corev1.PodRunning, false),
	)

	obs, err := r.collectPeers(context.Background(), vc)
	reachable := obs.reachable
	require.NoError(t, err)

	require.Len(t, reachable, 1)
	assert.Equal(t, "10.0.0.3", reachable[0].IP)
}
