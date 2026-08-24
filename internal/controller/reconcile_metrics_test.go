package controller

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
	"github.com/bluedynamics/cloud-vinyl/internal/generator"
	"github.com/bluedynamics/cloud-vinyl/internal/monitoring"
)

func TestReconcile_RecordsReconcileMetric(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := monitoring.NewMetrics(reg)

	sch := newScheme(t)
	vc := &v1alpha1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "ns1"},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(vc).
		WithStatusSubresource(vc).Build()
	r := &VinylCacheReconciler{Client: cli, Scheme: sch, Generator: generator.New(), Metrics: m}

	_, _ = r.Reconcile(context.Background(),
		ctrl.Request{NamespacedName: client.ObjectKey{Name: "c1", Namespace: "ns1"}})

	got := testutil.ToFloat64(m.ReconcileTotal.WithLabelValues("c1", "ns1", "error")) +
		testutil.ToFloat64(m.ReconcileTotal.WithLabelValues("c1", "ns1", "success"))
	assert.Equal(t, float64(1), got, "exactly one reconcile should be counted")
}

func TestUpdateStatus_SetsVCLVersionsGauge(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := monitoring.NewMetrics(reg)
	sch := newScheme(t)
	vc := &v1alpha1.VinylCache{ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "ns1"}}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(vc).WithStatusSubresource(vc).Build()
	r := &VinylCacheReconciler{Client: cli, Scheme: sch, Metrics: m}

	obs := podObservation{
		reachable: []generator.PeerBackend{peer("c1_0", "10.0.0.1")},
		ready:     []generator.PeerBackend{peer("c1_0", "10.0.0.1")},
		vclNames:  map[string]string{"10.0.0.1": "abcdef1234"},
	}
	r.updateStatus(context.Background(), vc, &generator.Result{Hash: "abcdef1234"}, obs, 1)

	// The gauge used to be hardcoded to 1 regardless of reality. It now counts
	// the distinct VCL hashes observed across the pods, which is what the
	// VinylCacheVCLDrift alert has always claimed to measure. One converged pod
	// means one version.
	assert.Equal(t, float64(1), testutil.ToFloat64(m.VCLVersionsLoaded.WithLabelValues("c1", "ns1")))
}

func TestUpdateStatus_VCLVersionsGauge_NoPodsMeansNoVersions(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := monitoring.NewMetrics(reg)
	sch := newScheme(t)
	vc := &v1alpha1.VinylCache{ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "ns1"}}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(vc).WithStatusSubresource(vc).Build()
	r := &VinylCacheReconciler{Client: cli, Scheme: sch, Metrics: m}

	r.updateStatus(context.Background(), vc, &generator.Result{Hash: "abcdef1234"}, podObservation{}, 0)

	assert.Equal(t, float64(0), testutil.ToFloat64(m.VCLVersionsLoaded.WithLabelValues("c1", "ns1")),
		"no reachable pods means no VCL is loaded anywhere")
}
