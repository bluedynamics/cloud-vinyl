package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	vinylv1 "github.com/bluedynamics/cloud-vinyl/api/v1"
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
