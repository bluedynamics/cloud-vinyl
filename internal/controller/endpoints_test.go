package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
)

func ptrBool(b bool) *bool       { return &b }
func ptrInt32(i int32) *int32    { return &i }
func ptrString(s string) *string { return &s }

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, appsv1.AddToScheme(s))
	require.NoError(t, discoveryv1.AddToScheme(s))
	require.NoError(t, v1alpha1.AddToScheme(s))
	return s
}

func TestResolveBackendEndpoints_MultipleReadyPods(t *testing.T) {
	sch := newScheme(t)
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "plone-svc", Namespace: "app"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 8080}}},
	}
	es := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "plone-svc-abc",
			Namespace: "app",
			Labels:    map[string]string{"kubernetes.io/service-name": "plone-svc"},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Ports: []discoveryv1.EndpointPort{
			{Name: ptrString("http"), Port: ptrInt32(8080)},
		},
		Endpoints: []discoveryv1.Endpoint{
			{Addresses: []string{"10.0.0.1"}, Conditions: discoveryv1.EndpointConditions{Ready: ptrBool(true)}},
			{Addresses: []string{"10.0.0.2"}, Conditions: discoveryv1.EndpointConditions{Ready: ptrBool(true)}},
			{Addresses: []string{"10.0.0.3"}, Conditions: discoveryv1.EndpointConditions{Ready: ptrBool(false)}},
			{Addresses: []string{"10.0.0.4"}, Conditions: discoveryv1.EndpointConditions{Ready: ptrBool(true), Terminating: ptrBool(true)}},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(svc, es).Build()
	r := &VinylCacheReconciler{Client: cli, Scheme: sch}
	vc := &v1alpha1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "cache", Namespace: "app"},
		Spec: v1alpha1.VinylCacheSpec{
			Backends: []v1alpha1.BackendSpec{{
				Name: "plone", ServiceRef: v1alpha1.ServiceRef{Name: "plone-svc"},
			}},
		},
	}
	out, err := r.resolveBackendEndpoints(context.Background(), vc)
	require.NoError(t, err)
	require.Len(t, out["plone"], 2, "only Ready=true && Terminating=false endpoints")
	ips := []string{out["plone"][0].IP, out["plone"][1].IP}
	assert.ElementsMatch(t, []string{"10.0.0.1", "10.0.0.2"}, ips)
	assert.Equal(t, 8080, out["plone"][0].Port)
}

func TestResolveBackendEndpoints_NoEndpointsReturnsEmpty(t *testing.T) {
	sch := newScheme(t)
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "plone-svc", Namespace: "app"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 8080}}},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(svc).Build()
	r := &VinylCacheReconciler{Client: cli, Scheme: sch}
	vc := &v1alpha1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "cache", Namespace: "app"},
		Spec: v1alpha1.VinylCacheSpec{
			Backends: []v1alpha1.BackendSpec{{
				Name: "plone", ServiceRef: v1alpha1.ServiceRef{Name: "plone-svc"},
			}},
		},
	}
	out, err := r.resolveBackendEndpoints(context.Background(), vc)
	require.NoError(t, err)
	assert.Empty(t, out["plone"])
}

func TestResolveBackendEndpoints_PortOverride(t *testing.T) {
	sch := newScheme(t)
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "plone-svc", Namespace: "app"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 8080}}},
	}
	es := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "plone-svc-a",
			Namespace: "app",
			Labels:    map[string]string{"kubernetes.io/service-name": "plone-svc"},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Ports:       []discoveryv1.EndpointPort{{Name: ptrString("http"), Port: ptrInt32(8080)}},
		Endpoints: []discoveryv1.Endpoint{
			{Addresses: []string{"10.0.0.1"}, Conditions: discoveryv1.EndpointConditions{Ready: ptrBool(true)}},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(svc, es).Build()
	r := &VinylCacheReconciler{Client: cli, Scheme: sch}
	vc := &v1alpha1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "cache", Namespace: "app"},
		Spec: v1alpha1.VinylCacheSpec{
			Backends: []v1alpha1.BackendSpec{{
				Name: "plone", Port: 9999,
				ServiceRef: v1alpha1.ServiceRef{Name: "plone-svc"},
			}},
		},
	}
	out, err := r.resolveBackendEndpoints(context.Background(), vc)
	require.NoError(t, err)
	require.Len(t, out["plone"], 1)
	assert.Equal(t, 9999, out["plone"][0].Port,
		"spec.backends[].port must override EndpointSlice port")
}

func TestResolveBackendEndpoints_SlicePortUnusable_IsSkipped(t *testing.T) {
	sch := newScheme(t)
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "plone-svc", Namespace: "app"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 8080}}},
	}
	// Slice has an endpoint but no port defined — pickPort returns 0 → slice skipped.
	es := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "plone-svc-noports",
			Namespace: "app",
			Labels:    map[string]string{"kubernetes.io/service-name": "plone-svc"},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Ports:       nil, // no usable port
		Endpoints: []discoveryv1.Endpoint{
			{Addresses: []string{"10.0.0.1"}, Conditions: discoveryv1.EndpointConditions{Ready: ptrBool(true)}},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(svc, es).Build()
	r := &VinylCacheReconciler{Client: cli, Scheme: sch}
	vc := &v1alpha1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "cache", Namespace: "app"},
		Spec: v1alpha1.VinylCacheSpec{
			Backends: []v1alpha1.BackendSpec{{
				Name: "plone", ServiceRef: v1alpha1.ServiceRef{Name: "plone-svc"},
			}},
		},
	}
	out, err := r.resolveBackendEndpoints(context.Background(), vc)
	require.NoError(t, err)
	assert.Empty(t, out["plone"],
		"slice without usable port must be skipped, not crash")
}

func TestResolveBackendEndpoints_MultipleSlices_Aggregate(t *testing.T) {
	sch := newScheme(t)
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "plone-svc", Namespace: "app"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 8080}}},
	}
	slice := func(name string, ips ...string) *discoveryv1.EndpointSlice {
		eps := make([]discoveryv1.Endpoint, 0, len(ips))
		for _, ip := range ips {
			eps = append(eps, discoveryv1.Endpoint{
				Addresses:  []string{ip},
				Conditions: discoveryv1.EndpointConditions{Ready: ptrBool(true)},
			})
		}
		return &discoveryv1.EndpointSlice{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: "app",
				Labels: map[string]string{"kubernetes.io/service-name": "plone-svc"},
			},
			AddressType: discoveryv1.AddressTypeIPv4,
			Ports:       []discoveryv1.EndpointPort{{Name: ptrString("http"), Port: ptrInt32(8080)}},
			Endpoints:   eps,
		}
	}
	es1 := slice("plone-svc-aaa", "10.0.0.1", "10.0.0.2")
	es2 := slice("plone-svc-bbb", "10.0.0.3", "10.0.0.4", "10.0.0.5")
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(svc, es1, es2).Build()
	r := &VinylCacheReconciler{Client: cli, Scheme: sch}
	vc := &v1alpha1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "cache", Namespace: "app"},
		Spec: v1alpha1.VinylCacheSpec{
			Backends: []v1alpha1.BackendSpec{{
				Name: "plone", ServiceRef: v1alpha1.ServiceRef{Name: "plone-svc"},
			}},
		},
	}
	out, err := r.resolveBackendEndpoints(context.Background(), vc)
	require.NoError(t, err)
	require.Len(t, out["plone"], 5, "must aggregate endpoints across multiple EndpointSlices")
	ips := make([]string, 0, 5)
	for _, ep := range out["plone"] {
		ips = append(ips, ep.IP)
	}
	assert.ElementsMatch(t,
		[]string{"10.0.0.1", "10.0.0.2", "10.0.0.3", "10.0.0.4", "10.0.0.5"}, ips)
}

func TestResolveBackendEndpoints_ResultIsSortedByIP(t *testing.T) {
	sch := newScheme(t)
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "plone-svc", Namespace: "app"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 8080}}},
	}
	es := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name: "plone-svc-a", Namespace: "app",
			Labels: map[string]string{"kubernetes.io/service-name": "plone-svc"},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Ports:       []discoveryv1.EndpointPort{{Name: ptrString("http"), Port: ptrInt32(8080)}},
		Endpoints: []discoveryv1.Endpoint{
			// Deliberately unsorted.
			{Addresses: []string{"10.0.0.3"}, Conditions: discoveryv1.EndpointConditions{Ready: ptrBool(true)}},
			{Addresses: []string{"10.0.0.1"}, Conditions: discoveryv1.EndpointConditions{Ready: ptrBool(true)}},
			{Addresses: []string{"10.0.0.2"}, Conditions: discoveryv1.EndpointConditions{Ready: ptrBool(true)}},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(svc, es).Build()
	r := &VinylCacheReconciler{Client: cli, Scheme: sch}
	vc := &v1alpha1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "cache", Namespace: "app"},
		Spec: v1alpha1.VinylCacheSpec{
			Backends: []v1alpha1.BackendSpec{{
				Name: "plone", ServiceRef: v1alpha1.ServiceRef{Name: "plone-svc"},
			}},
		},
	}
	out, err := r.resolveBackendEndpoints(context.Background(), vc)
	require.NoError(t, err)
	require.Len(t, out["plone"], 3)
	assert.Equal(t, "10.0.0.1", out["plone"][0].IP)
	assert.Equal(t, "10.0.0.2", out["plone"][1].IP)
	assert.Equal(t, "10.0.0.3", out["plone"][2].IP)
}
