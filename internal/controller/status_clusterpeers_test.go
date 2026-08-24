package controller

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"

	"github.com/bluedynamics/cloud-vinyl/internal/monitoring"

	v1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
	"github.com/bluedynamics/cloud-vinyl/internal/generator"
)

// TestUpdateStatus_ClusterPeersCoverReachablePods pins decision E3 of the
// convergence spec. ClusterPeerStatus has always carried a Ready flag and a
// per-pod hash, but the controller only ever appended Ready pods and hardcoded
// Ready: true, which made both fields carry no information.
func TestUpdateStatus_ClusterPeersCoverReachablePods(t *testing.T) {
	r, vc := statusFixture(t)
	vc.Spec.Replicas = 2

	obs := podObservation{
		reachable: []generator.PeerBackend{peer("c_0", "10.0.0.1"), peer("c_1", "10.0.0.2")},
		ready:     []generator.PeerBackend{peer("c_0", "10.0.0.1")},
		vclNames:  map[string]string{"10.0.0.1": "want", "10.0.0.2": "bootstrap"},
	}
	r.updateStatus(context.Background(), vc, &generator.Result{Hash: "want"}, obs, 1)

	assert.Equal(t, []v1alpha1.ClusterPeerStatus{
		{PodName: "c_0", Ready: true, ActiveVCLHash: "want"},
		{PodName: "c_1", Ready: false, ActiveVCLHash: "bootstrap"},
	}, vc.Status.ClusterPeers)

	assert.Equal(t, int32(1), vc.Status.ReadyPeers, "ReadyPeers still counts only Ready pods")
}

// The recorded hash must be what the pod actually has, not what we wanted it to
// have. Recording the desired hash for every pod is what made the field useless
// and would make drift undetectable by construction.
func TestUpdateStatus_ClusterPeerHashIsObservedNotDesired(t *testing.T) {
	r, vc := statusFixture(t)

	obs := podObservation{
		reachable: []generator.PeerBackend{peer("c_0", "10.0.0.1")},
		vclNames:  map[string]string{"10.0.0.1": "something-else"},
	}
	r.updateStatus(context.Background(), vc, &generator.Result{Hash: "want"}, obs, 0)

	if assert.Len(t, vc.Status.ClusterPeers, 1) {
		assert.Equal(t, "something-else", vc.Status.ClusterPeers[0].ActiveVCLHash)
	}
}

// TestUpdateStatus_VCLVersionsGaugeCountsDistinctHashes gives the
// VinylCacheVCLDrift alert a data source. The alert fires on
// `vinyl_vcl_versions_loaded > 2`, but the gauge was hardcoded to 1, so it
// could never fire. Counting the distinct VCL hashes actually observed across
// the pods is exactly the quantity the alert describes.
func TestUpdateStatus_VCLVersionsGaugeCountsDistinctHashes(t *testing.T) {
	r, vc, m := statusFixtureWithMetrics(t)

	obs := podObservation{
		reachable: []generator.PeerBackend{
			peer("c_0", "10.0.0.1"), peer("c_1", "10.0.0.2"), peer("c_2", "10.0.0.3"),
		},
		vclNames: map[string]string{
			"10.0.0.1": "v1",
			"10.0.0.2": "v1",
			"10.0.0.3": "v2",
		},
	}
	r.updateStatus(context.Background(), vc, &generator.Result{Hash: "v2"}, obs, 1)

	assert.Equal(t, float64(2),
		testutil.ToFloat64(m.VCLVersionsLoaded.WithLabelValues("cache", "app")))
}

func TestUpdateStatus_VCLVersionsGauge_ConvergedCacheReportsOne(t *testing.T) {
	r, vc, m := statusFixtureWithMetrics(t)

	obs := podObservation{
		reachable: []generator.PeerBackend{peer("c_0", "10.0.0.1"), peer("c_1", "10.0.0.2")},
		vclNames:  map[string]string{"10.0.0.1": "v1", "10.0.0.2": "v1"},
	}
	r.updateStatus(context.Background(), vc, &generator.Result{Hash: "v1"}, obs, 0)

	assert.Equal(t, float64(1),
		testutil.ToFloat64(m.VCLVersionsLoaded.WithLabelValues("cache", "app")))
}

// A pod whose agent could not be reached reports no hash at all. That is
// absence of information, not a third VCL version, and must not trip the drift
// alert.
func TestUpdateStatus_VCLVersionsGauge_IgnoresUnreachablePods(t *testing.T) {
	r, vc, m := statusFixtureWithMetrics(t)

	obs := podObservation{
		reachable: []generator.PeerBackend{peer("c_0", "10.0.0.1"), peer("c_1", "10.0.0.2")},
		vclNames:  map[string]string{"10.0.0.1": "v1", "10.0.0.2": ""},
	}
	r.updateStatus(context.Background(), vc, &generator.Result{Hash: "v1"}, obs, 0)

	assert.Equal(t, float64(1),
		testutil.ToFloat64(m.VCLVersionsLoaded.WithLabelValues("cache", "app")))
}

func statusFixtureWithMetrics(t *testing.T) (*VinylCacheReconciler, *v1alpha1.VinylCache, *monitoring.Metrics) {
	t.Helper()
	r, vc := statusFixture(t)
	m := monitoring.NewMetrics(prometheus.NewRegistry())
	r.Metrics = m
	return r, vc, m
}
