package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	vinylv1 "github.com/bluedynamics/cloud-vinyl/api/v1"
	"github.com/bluedynamics/cloud-vinyl/internal/monitoring"
)

func TestReconcileMonitoring_SkipsWhenCRDAbsent(t *testing.T) {
	sch := newScheme(t)
	vc := &vinylv1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "ns1"},
		Spec: vinylv1.VinylCacheSpec{
			Monitoring: vinylv1.MonitoringSpec{
				ServiceMonitor:  &vinylv1.ServiceMonitorSpec{Enabled: true},
				PrometheusRules: &vinylv1.PrometheusRulesSpec{Enabled: true},
			},
		},
	}
	// The fake client has no RESTMapper for monitoring.coreos.com → must skip, no error.
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(vc).Build()
	r := &VinylCacheReconciler{Client: cli, Scheme: sch}
	require.NoError(t, r.reconcileMonitoring(context.Background(), vc))
}

func legacyRule(owner *vinylv1.VinylCache) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(prometheusRuleGVK)
	u.SetNamespace("ns1")
	u.SetName(monitoring.LegacyPrometheusRuleName)
	if owner != nil {
		u.SetOwnerReferences([]metav1.OwnerReference{*metav1.NewControllerRef(owner,
			vinylv1.GroupVersion.WithKind("VinylCache"))})
	}
	return u
}

// Before #143 every cache's rule was named cloud-vinyl-alerts; the cache that
// controls it removes it once its own <name>-alerts rule exists.
func TestDeleteLegacyPrometheusRule(t *testing.T) {
	sch := newScheme(t)
	mine := &vinylv1.VinylCache{ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "ns1", UID: "uid-c1"}}
	other := &vinylv1.VinylCache{ObjectMeta: metav1.ObjectMeta{Name: "c2", Namespace: "ns1", UID: "uid-c2"}}

	cases := []struct {
		name    string
		owner   *vinylv1.VinylCache
		deleted bool
	}{
		{"controlled by this cache", mine, true},
		{"controlled by another cache", other, false},
		{"not controlled by any cache", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(legacyRule(tc.owner)).Build()
			r := &VinylCacheReconciler{Client: cli, Scheme: sch}
			require.NoError(t, r.deleteLegacyPrometheusRule(context.Background(), mine))

			got := legacyRule(nil)
			err := cli.Get(context.Background(), client.ObjectKeyFromObject(got), got)
			if tc.deleted {
				require.True(t, apierrors.IsNotFound(err), "legacy rule must be gone, got %v", err)
			} else {
				require.NoError(t, err, "a rule this cache does not control must stay")
			}
		})
	}

	t.Run("absent", func(t *testing.T) {
		cli := fake.NewClientBuilder().WithScheme(sch).Build()
		r := &VinylCacheReconciler{Client: cli, Scheme: sch}
		require.NoError(t, r.deleteLegacyPrometheusRule(context.Background(), mine))
	})
}
