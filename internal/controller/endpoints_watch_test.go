package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
)

func TestEndpointSliceToVinylCache_MapsByServiceName(t *testing.T) {
	sch := newScheme(t)
	vcA := &v1alpha1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "cache-a", Namespace: "app"},
		Spec: v1alpha1.VinylCacheSpec{
			Backends: []v1alpha1.BackendSpec{
				{Name: "plone", ServiceRef: v1alpha1.ServiceRef{Name: "plone-svc"}},
				{Name: "api", ServiceRef: v1alpha1.ServiceRef{Name: "api-svc"}},
			},
		},
	}
	vcB := &v1alpha1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "cache-b", Namespace: "app"},
		Spec: v1alpha1.VinylCacheSpec{
			Backends: []v1alpha1.BackendSpec{
				{Name: "plone", ServiceRef: v1alpha1.ServiceRef{Name: "plone-svc"}},
			},
		},
	}
	vcOther := &v1alpha1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "cache-other", Namespace: "other"},
		Spec: v1alpha1.VinylCacheSpec{
			Backends: []v1alpha1.BackendSpec{
				{Name: "plone", ServiceRef: v1alpha1.ServiceRef{Name: "plone-svc"}},
			},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(vcA, vcB, vcOther).Build()
	r := &VinylCacheReconciler{Client: cli, Scheme: sch}

	es := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "plone-svc-xyz",
			Namespace: "app",
			Labels:    map[string]string{discoveryv1.LabelServiceName: "plone-svc"},
		},
	}
	reqs := r.endpointSliceToVinylCache(context.Background(), es)
	require.Len(t, reqs, 2, "only VinylCaches in the same namespace referencing the Service")
	names := []string{reqs[0].Name, reqs[1].Name}
	assert.ElementsMatch(t, []string{"cache-a", "cache-b"}, names)
	for _, req := range reqs {
		assert.Equal(t, "app", req.Namespace)
	}
}

func TestEndpointSliceToVinylCache_NoLabelReturnsNil(t *testing.T) {
	sch := newScheme(t)
	cli := fake.NewClientBuilder().WithScheme(sch).Build()
	r := &VinylCacheReconciler{Client: cli, Scheme: sch}
	es := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "orphan", Namespace: "app"},
	}
	reqs := r.endpointSliceToVinylCache(context.Background(), es)
	assert.Empty(t, reqs)
}

func TestEndpointSliceToVinylCache_WrongTypeReturnsNil(t *testing.T) {
	sch := newScheme(t)
	cli := fake.NewClientBuilder().WithScheme(sch).Build()
	r := &VinylCacheReconciler{Client: cli, Scheme: sch}
	reqs := r.endpointSliceToVinylCache(context.Background(), &v1alpha1.VinylCache{})
	assert.Nil(t, reqs, "non-EndpointSlice object should yield nil")
}
