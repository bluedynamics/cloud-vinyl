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
	err := r.Client.Get(context.Background(), types.NamespacedName{Name: vc.Name + "-exporter", Namespace: vc.Namespace}, np)
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
