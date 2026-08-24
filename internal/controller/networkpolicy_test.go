package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
)

func netpolVC(exp *v1alpha1.ExporterSpec) *v1alpha1.VinylCache {
	return &v1alpha1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "my-cache", Namespace: "app"},
		Spec: v1alpha1.VinylCacheSpec{
			Monitoring: v1alpha1.MonitoringSpec{Exporter: exp},
		},
	}
}

func getExporterNetpol(t *testing.T, r *VinylCacheReconciler, vc *v1alpha1.VinylCache) (*networkingv1.NetworkPolicy, error) {
	t.Helper()
	np := &networkingv1.NetworkPolicy{}
	err := r.Get(context.Background(), types.NamespacedName{Name: vc.Name + "-exporter", Namespace: vc.Namespace}, np)
	return np, err
}

func TestReconcileExporterNetworkPolicy_OpensPortWhenEnabled(t *testing.T) {
	sch := newScheme(t)
	vc := netpolVC(&v1alpha1.ExporterSpec{Enabled: true})
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(vc).Build()
	r := &VinylCacheReconciler{Client: cli, Scheme: sch}

	require.NoError(t, r.reconcileExporterNetworkPolicy(context.Background(), vc))

	np, err := getExporterNetpol(t, r, vc)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"app": "my-cache"}, np.Spec.PodSelector.MatchLabels)
	require.Len(t, np.Spec.Ingress, 1)
	// Empty From => allow from all sources (Prometheus in any namespace).
	assert.Empty(t, np.Spec.Ingress[0].From)
	require.Len(t, np.Spec.Ingress[0].Ports, 1)
	assert.Equal(t, int(exporterPort), np.Spec.Ingress[0].Ports[0].Port.IntValue())
}

func TestReconcileExporterNetworkPolicy_CustomPort(t *testing.T) {
	sch := newScheme(t)
	vc := netpolVC(&v1alpha1.ExporterSpec{Enabled: true, Port: 19131})
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(vc).Build()
	r := &VinylCacheReconciler{Client: cli, Scheme: sch}

	require.NoError(t, r.reconcileExporterNetworkPolicy(context.Background(), vc))

	np, err := getExporterNetpol(t, r, vc)
	require.NoError(t, err)
	require.Len(t, np.Spec.Ingress, 1)
	require.Len(t, np.Spec.Ingress[0].Ports, 1)
	assert.Equal(t, 19131, np.Spec.Ingress[0].Ports[0].Port.IntValue())
}

func TestReconcileExporterNetworkPolicy_AbsentWhenDisabled(t *testing.T) {
	sch := newScheme(t)
	vc := netpolVC(nil)
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(vc).Build()
	r := &VinylCacheReconciler{Client: cli, Scheme: sch}

	require.NoError(t, r.reconcileExporterNetworkPolicy(context.Background(), vc))

	_, err := getExporterNetpol(t, r, vc)
	assert.True(t, apierrors.IsNotFound(err), "no exporter NetworkPolicy when exporter is disabled")
}

func TestReconcileExporterNetworkPolicy_RemovedWhenToggledOff(t *testing.T) {
	sch := newScheme(t)
	vc := netpolVC(&v1alpha1.ExporterSpec{Enabled: true})
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(vc).Build()
	r := &VinylCacheReconciler{Client: cli, Scheme: sch}

	require.NoError(t, r.reconcileExporterNetworkPolicy(context.Background(), vc))
	_, err := getExporterNetpol(t, r, vc)
	require.NoError(t, err)

	// Toggle the exporter off and reconcile again.
	vc.Spec.Monitoring.Exporter.Enabled = false
	require.NoError(t, r.reconcileExporterNetworkPolicy(context.Background(), vc))

	_, err = getExporterNetpol(t, r, vc)
	assert.True(t, apierrors.IsNotFound(err), "stale exporter NetworkPolicy must be removed when disabled")
}

// --- agent / invalidation policies: operator reachability (issue #58) ---

func getNetpol(t *testing.T, r *VinylCacheReconciler, vc *v1alpha1.VinylCache, suffix string) *networkingv1.NetworkPolicy {
	t.Helper()
	np := &networkingv1.NetworkPolicy{}
	require.NoError(t, r.Get(context.Background(),
		types.NamespacedName{Name: vc.Name + suffix, Namespace: vc.Namespace}, np))
	return np
}

func netpolReconciler(t *testing.T, operatorIP string) (*VinylCacheReconciler, *v1alpha1.VinylCache) {
	t.Helper()
	sch := newScheme(t)
	vc := netpolVC(nil)
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(vc).Build()
	return &VinylCacheReconciler{Client: cli, Scheme: sch, OperatorIP: operatorIP}, vc
}

// ipBlockCIDRs returns the CIDRs of all ipBlock peers across every ingress rule.
func ipBlockCIDRs(np *networkingv1.NetworkPolicy) []string {
	var out []string
	for _, rule := range np.Spec.Ingress {
		for _, peer := range rule.From {
			if peer.IPBlock != nil {
				out = append(out, peer.IPBlock.CIDR)
			}
		}
	}
	return out
}

// hasOperatorNamespaceSelector reports whether the label-based peer is still present.
func hasOperatorNamespaceSelector(np *networkingv1.NetworkPolicy) bool {
	for _, rule := range np.Spec.Ingress {
		for _, peer := range rule.From {
			if peer.NamespaceSelector == nil {
				continue
			}
			if peer.NamespaceSelector.MatchLabels["vinyl.bluedynamics.eu/operator-namespace"] == "true" {
				return true
			}
		}
	}
	return false
}

func TestReconcileAgentNetworkPolicy_AllowsOperatorPodIP(t *testing.T) {
	r, vc := netpolReconciler(t, "10.42.0.7")

	require.NoError(t, r.reconcileAgentNetworkPolicy(context.Background(), vc))

	np := getNetpol(t, r, vc, "-agent")
	assert.Contains(t, ipBlockCIDRs(np), "10.42.0.7/32",
		"operator must reach the agent without a manually applied namespace label")
	assert.True(t, hasOperatorNamespaceSelector(np),
		"label-based peer stays for backwards compatibility")

	// Port 9090 must be the only opened port.
	for _, rule := range np.Spec.Ingress {
		require.Len(t, rule.Ports, 1)
		assert.Equal(t, int(agentPort), rule.Ports[0].Port.IntValue())
	}
}

func TestReconcileInvalidationNetworkPolicy_AllowsOperatorPodIP(t *testing.T) {
	r, vc := netpolReconciler(t, "10.42.0.7")

	require.NoError(t, r.reconcileInvalidationNetworkPolicy(context.Background(), vc))

	np := getNetpol(t, r, vc, "-invalidation")
	assert.Contains(t, ipBlockCIDRs(np), "10.42.0.7/32",
		"PURGE/BAN forwarding must reach Varnish without a namespace label")
	assert.True(t, hasOperatorNamespaceSelector(np))

	for _, rule := range np.Spec.Ingress {
		require.Len(t, rule.Ports, 1)
		assert.Equal(t, int(varnishPort), rule.Ports[0].Port.IntValue())
	}
}

func TestReconcileAgentNetworkPolicy_IPv6OperatorIP(t *testing.T) {
	r, vc := netpolReconciler(t, "fd00::7")

	require.NoError(t, r.reconcileAgentNetworkPolicy(context.Background(), vc))

	assert.Contains(t, ipBlockCIDRs(getNetpol(t, r, vc, "-agent")), "fd00::7/128")
}

func TestReconcileAgentNetworkPolicy_NoOperatorIP(t *testing.T) {
	r, vc := netpolReconciler(t, "")

	require.NoError(t, r.reconcileAgentNetworkPolicy(context.Background(), vc))

	np := getNetpol(t, r, vc, "-agent")
	assert.Empty(t, ipBlockCIDRs(np), "no POD_IP means no ipBlock peer")
	assert.True(t, hasOperatorNamespaceSelector(np), "label fallback must still work")
}

func TestReconcileAgentNetworkPolicy_InvalidOperatorIP(t *testing.T) {
	r, vc := netpolReconciler(t, "not-an-ip")

	require.NoError(t, r.reconcileAgentNetworkPolicy(context.Background(), vc))

	np := getNetpol(t, r, vc, "-agent")
	assert.Empty(t, ipBlockCIDRs(np), "an unparseable POD_IP must not produce an invalid CIDR")
	assert.True(t, hasOperatorNamespaceSelector(np))
}

func TestReconcileAgentNetworkPolicy_OperatorIPChangeReplacesStaleCIDR(t *testing.T) {
	r, vc := netpolReconciler(t, "10.42.0.7")
	require.NoError(t, r.reconcileAgentNetworkPolicy(context.Background(), vc))

	// Operator pod restarted with a new IP.
	r.OperatorIP = "10.42.0.9"
	require.NoError(t, r.reconcileAgentNetworkPolicy(context.Background(), vc))

	cidrs := ipBlockCIDRs(getNetpol(t, r, vc, "-agent"))
	assert.Contains(t, cidrs, "10.42.0.9/32")
	assert.NotContains(t, cidrs, "10.42.0.7/32", "stale operator IP must not linger")
}
