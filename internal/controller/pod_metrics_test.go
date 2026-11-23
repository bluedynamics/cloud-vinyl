/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	vinylv1 "github.com/bluedynamics/cloud-vinyl/api/v1"
	"github.com/bluedynamics/cloud-vinyl/internal/generator"
	"github.com/bluedynamics/cloud-vinyl/internal/monitoring"
)

// #190: VinylCacheAllPodsUnreachable compares these two gauges.
func TestRecordPodMetrics_SetsReadyAndDesiredPods(t *testing.T) {
	m := monitoring.NewMetrics(prometheus.NewRegistry())
	r := &VinylCacheReconciler{Metrics: m}
	vc := makeVC()
	vc.Spec.Replicas = 3

	r.recordPodMetrics(vc, podObservation{ready: makePeers(1), reachable: makePeers(2)})

	assert.InDelta(t, 1, testutil.ToFloat64(m.CacheReadyPods.WithLabelValues("test-cache", "default")), 0)
	assert.InDelta(t, 3, testutil.ToFloat64(m.CacheDesiredPods.WithLabelValues("test-cache", "default")), 0)
}

func TestRecordPodMetrics_NilMetricsIsSafe(t *testing.T) {
	r := &VinylCacheReconciler{}
	r.recordPodMetrics(makeVC(), podObservation{})
}

// A deleted cache must not leave its gauges behind: a last value of zero
// ready pods would keep VinylCacheAllPodsUnreachable firing until the
// operator restarts.
func TestHandleDeletion_ForgetsCacheGauges(t *testing.T) {
	sch := newScheme(t)
	vc := makeVC()
	vc.Finalizers = []string{finalizerName}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(vc).Build()
	m := monitoring.NewMetrics(prometheus.NewRegistry())
	r := &VinylCacheReconciler{Client: cli, Scheme: sch, Metrics: m}
	m.CacheReadyPods.WithLabelValues("test-cache", "default").Set(0)
	m.CacheDesiredPods.WithLabelValues("test-cache", "default").Set(2)
	m.VCLVersionsLoaded.WithLabelValues("test-cache", "default").Set(1)

	require.NoError(t, r.handleDeletion(context.Background(), vc))

	assert.Equal(t, 0, testutil.CollectAndCount(m.CacheReadyPods))
	assert.Equal(t, 0, testutil.CollectAndCount(m.CacheDesiredPods))
	assert.Equal(t, 0, testutil.CollectAndCount(m.VCLVersionsLoaded))
}

// The gauges must follow the pods even when the push fails on every pod,
// which is exactly when VinylCacheAllPodsUnreachable has to fire. Reconcile
// returns early after a failed push, before updateStatus.
func TestReconcile_SetsPodGaugesWhenPushFails(t *testing.T) {
	sch := newScheme(t)
	vc := &vinylv1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "ns1"},
		Spec: vinylv1.VinylCacheSpec{
			Replicas: 2, Image: "vinyl:test",
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
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(vc, pod, svc).
		WithStatusSubresource(vc).Build()
	m := monitoring.NewMetrics(prometheus.NewRegistry())
	r := &VinylCacheReconciler{
		Client: cli, Scheme: sch, Generator: generator.New(), Catalog: testCatalog(t), Metrics: m,
		AgentClient: &mockAgentClient{pushErr: errors.New("vcl.load failed")},
	}

	_, err := r.Reconcile(context.Background(),
		ctrl.Request{NamespacedName: client.ObjectKey{Name: "c1", Namespace: "ns1"}})
	require.NoError(t, err)

	assert.InDelta(t, 0, testutil.ToFloat64(m.CacheReadyPods.WithLabelValues("c1", "ns1")), 0)
	assert.InDelta(t, 2, testutil.ToFloat64(m.CacheDesiredPods.WithLabelValues("c1", "ns1")), 0)
	assert.InDelta(t, 1, testutil.ToFloat64(m.VCLPushTotal.WithLabelValues("c1", "ns1", "error")), 0,
		"the push must have been attempted and failed")
}
