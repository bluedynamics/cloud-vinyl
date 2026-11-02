package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	vinylv1 "github.com/bluedynamics/cloud-vinyl/api/v1"
)

func TestPodAffinity_BuiltinDefaultIsPreferredOnHostname(t *testing.T) {
	got := podAffinity(exporterBaseVC(), AntiAffinityDefaults{})
	require.NotNil(t, got)
	require.NotNil(t, got.PodAntiAffinity)
	assert.Empty(t, got.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution)
	require.Len(t, got.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution, 1)
	pref := got.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0]
	assert.Equal(t, int32(100), pref.Weight)
	assert.Equal(t, corev1.LabelHostname, pref.PodAffinityTerm.TopologyKey)
	assert.Equal(t, map[string]string{labelVinylCacheName: "my-cache"},
		pref.PodAffinityTerm.LabelSelector.MatchLabels)
	assert.Nil(t, got.NodeAffinity)
	assert.Nil(t, got.PodAffinity)
}

// The rule must not depend on the replica count: gating it on replicas > 1
// would change the pod template on a scale from 1 to 2 and restart pod-0.
func TestPodAffinity_SetForSingleReplica(t *testing.T) {
	vc := exporterBaseVC()
	require.Equal(t, int32(1), vc.Spec.Replicas)
	one := podAffinity(vc, AntiAffinityDefaults{})
	vc.Spec.Replicas = 3
	assert.Equal(t, one, podAffinity(vc, AntiAffinityDefaults{}))
}

func TestPodAffinity_OperatorDefaultRequiredOnZone(t *testing.T) {
	got := podAffinity(exporterBaseVC(), AntiAffinityDefaults{
		Type:        vinylv1.PodAntiAffinityRequired,
		TopologyKey: corev1.LabelTopologyZone,
	})
	require.NotNil(t, got.PodAntiAffinity)
	assert.Empty(t, got.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution)
	require.Len(t, got.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution, 1)
	assert.Equal(t, corev1.LabelTopologyZone,
		got.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution[0].TopologyKey)
}

func TestPodAffinity_OperatorDefaultNone(t *testing.T) {
	assert.Nil(t, podAffinity(exporterBaseVC(), AntiAffinityDefaults{Type: vinylv1.PodAntiAffinityNone}))
}

// Fields the VinylCache sets override the operator default one by one; the
// others keep the operator's value.
func TestPodAffinity_SpecOverridesPerField(t *testing.T) {
	vc := exporterBaseVC()
	vc.Spec.Pod.PodAntiAffinity = &vinylv1.PodAntiAffinitySpec{Weight: 20}
	got := podAffinity(vc, AntiAffinityDefaults{TopologyKey: corev1.LabelTopologyZone, Weight: 50})
	require.Len(t, got.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution, 1)
	pref := got.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0]
	assert.Equal(t, int32(20), pref.Weight, "weight from the VinylCache")
	assert.Equal(t, corev1.LabelTopologyZone, pref.PodAffinityTerm.TopologyKey, "topology key from the operator")
}

func TestPodAffinity_SpecNoneOverridesOperatorRequired(t *testing.T) {
	vc := exporterBaseVC()
	vc.Spec.Pod.PodAntiAffinity = &vinylv1.PodAntiAffinitySpec{Type: vinylv1.PodAntiAffinityNone}
	assert.Nil(t, podAffinity(vc, AntiAffinityDefaults{Type: vinylv1.PodAntiAffinityRequired}))
}

// A user nodeAffinity (pinning the cache to a node pool, say) must not drop
// the spreading, and must come through unchanged.
func TestPodAffinity_KeepsUserNodeAffinityAndAddsDefault(t *testing.T) {
	vc := exporterBaseVC()
	node := &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{
				{Key: "pool", Operator: corev1.NodeSelectorOpIn, Values: []string{"cache"}},
			}}},
		},
	}
	vc.Spec.Pod.Affinity = &corev1.Affinity{NodeAffinity: node}
	got := podAffinity(vc, AntiAffinityDefaults{})
	assert.Equal(t, node, got.NodeAffinity)
	assert.NotNil(t, got.PodAntiAffinity)
	assert.Nil(t, vc.Spec.Pod.Affinity.PodAntiAffinity, "the VinylCache spec must not be modified")
}

func TestPodAffinity_UserPodAntiAffinityWins(t *testing.T) {
	vc := exporterBaseVC()
	user := &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{TopologyKey: "rack"}},
	}}
	vc.Spec.Pod.Affinity = user
	assert.Same(t, user, podAffinity(vc, AntiAffinityDefaults{Type: vinylv1.PodAntiAffinityPreferred}))
}

func TestAntiAffinityDefaults_Validate(t *testing.T) {
	assert.NoError(t, AntiAffinityDefaults{}.Validate())
	assert.NoError(t, BuiltinAntiAffinityDefaults.Validate())
	assert.Error(t, AntiAffinityDefaults{Type: "soft"}.Validate())
	assert.Error(t, AntiAffinityDefaults{Weight: 101}.Validate())
}

func TestReconcileStatefulSet_DefaultPodAntiAffinity(t *testing.T) {
	ss := getStatefulSet(t, exporterBaseVC())
	aff := ss.Spec.Template.Spec.Affinity
	require.NotNil(t, aff)
	require.NotNil(t, aff.PodAntiAffinity)
	require.Len(t, aff.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution, 1)
	sel := aff.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0].PodAffinityTerm.LabelSelector
	assert.Equal(t, ss.Spec.Template.Labels[labelVinylCacheName], sel.MatchLabels[labelVinylCacheName],
		"the selector must match the cache's own pods")
}
