package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	vinylv1 "github.com/bluedynamics/cloud-vinyl/api/v1"
)

// byObject returns the ByObject entry for obj's type.
func byObject(t *testing.T, opts cache.Options, obj client.Object) (cache.ByObject, bool) {
	t.Helper()
	for k, v := range opts.ByObject {
		if typeName(k) == typeName(obj) {
			return v, true
		}
	}
	return cache.ByObject{}, false
}

func typeName(obj client.Object) string {
	switch obj.(type) {
	case *appsv1.StatefulSet:
		return "StatefulSet"
	case *corev1.Service:
		return "Service"
	case *corev1.ConfigMap:
		return "ConfigMap"
	case *corev1.Pod:
		return "Pod"
	case *corev1.Secret:
		return "Secret"
	case *networkingv1.NetworkPolicy:
		return "NetworkPolicy"
	case *discoveryv1.EndpointSlice:
		return "EndpointSlice"
	}
	return "unknown"
}

func TestCacheOptions_OwnedTypesSelectOnCacheNameLabel(t *testing.T) {
	opts := CacheOptions(nil)
	for _, obj := range []client.Object{
		&corev1.Service{}, &corev1.ConfigMap{},
		&corev1.Pod{}, &networkingv1.NetworkPolicy{},
	} {
		bo, ok := byObject(t, opts, obj)
		require.True(t, ok, "%s must be scoped", typeName(obj))
		require.NotNil(t, bo.Label, typeName(obj))
		assert.True(t, bo.Label.Matches(labels.Set{labelVinylCacheName: "www"}), typeName(obj))
		assert.True(t, bo.Label.Matches(labels.Set{labelVinylCacheName: ""}),
			"%s: any value of the label is ours", typeName(obj))
		assert.False(t, bo.Label.Matches(labels.Set{"app": "www"}), typeName(obj))
	}
}

// The agent Secret is shared by every VinylCache in a namespace and carries
// no cache-name label. Scoping it like the owned types would hide it, and
// reconcileSecret would try to create it on every reconcile.
func TestCacheOptions_SecretSelectsOnManagedBy(t *testing.T) {
	bo, ok := byObject(t, CacheOptions(nil), &corev1.Secret{})
	require.True(t, ok)
	assert.True(t, bo.Label.Matches(labels.Set{labelManagedBy: managedByValue}))
	assert.False(t, bo.Label.Matches(labels.Set{labelVinylCacheName: "www"}))
	assert.False(t, bo.Label.Matches(labels.Set{labelManagedBy: "helm"}))
}

// StatefulSets created before 1.1 carry no label on the object itself, so
// scoping them would hide them and CreateOrUpdate would fail with
// AlreadyExists on every reconcile.
func TestCacheOptions_StatefulSetsNotLabelScoped(t *testing.T) {
	_, ok := byObject(t, CacheOptions(nil), &appsv1.StatefulSet{})
	assert.False(t, ok)
}

func TestReconcileStatefulSet_LabelsTheStatefulSet(t *testing.T) {
	ss := getStatefulSet(t, exporterBaseVC())
	assert.Equal(t, "my-cache", ss.Labels[labelVinylCacheName])
}

// Every object the operator creates must pass its type's cache selector, or
// the operator cannot see what it created. This is the check that would
// have caught StatefulSets being scoped although they carried no label.
func TestCacheOptions_EveryCreatedObjectPassesItsSelector(t *testing.T) {
	sch := newScheme(t)
	cli := fake.NewClientBuilder().WithScheme(sch).Build()
	r := &VinylCacheReconciler{Client: cli, Scheme: sch}
	vc := exporterBaseVC()
	vc.Spec.Monitoring.Exporter = &vinylv1.ExporterSpec{Enabled: true}
	vc.Spec.Tracing.Enabled = true
	ctx := context.Background()
	require.NoError(t, r.reconcileSecret(ctx, vc))
	require.NoError(t, r.reconcileConfigMap(ctx, vc))
	require.NoError(t, r.reconcileStatefulSet(ctx, vc))
	require.NoError(t, r.reconcileServices(ctx, vc))
	require.NoError(t, r.reconcileEndpointSlice(ctx, vc))
	require.NoError(t, r.reconcileNetworkPolicies(ctx, vc))

	opts := CacheOptions(nil)
	lists := []client.ObjectList{
		&appsv1.StatefulSetList{}, &corev1.ServiceList{}, &corev1.ConfigMapList{},
		&corev1.SecretList{}, &networkingv1.NetworkPolicyList{}, &discoveryv1.EndpointSliceList{},
	}
	seen := 0
	for _, list := range lists {
		require.NoError(t, cli.List(ctx, list))
		items, err := meta.ExtractList(list)
		require.NoError(t, err)
		for _, item := range items {
			obj := item.(client.Object)
			seen++
			bo, scoped := byObject(t, opts, obj)
			if !scoped {
				continue
			}
			assert.True(t, bo.Label.Matches(labels.Set(obj.GetLabels())),
				"%s %s is created without the label its cache selector requires", typeName(obj), obj.GetName())
		}
	}
	assert.GreaterOrEqual(t, seen, 8, "the reconcile functions created the objects under test")

	// Pods are created by the StatefulSet controller from the template.
	ssList := &appsv1.StatefulSetList{}
	require.NoError(t, cli.List(ctx, ssList))
	require.Len(t, ssList.Items, 1)
	podScope, ok := byObject(t, opts, &corev1.Pod{})
	require.True(t, ok)
	assert.True(t, podScope.Label.Matches(labels.Set(ssList.Items[0].Spec.Template.Labels)),
		"cache pods are created without the label the Pod selector requires")
}

// EndpointSlices of the users' backend Services carry no label of ours.
func TestCacheOptions_EndpointSlicesNotLabelScoped(t *testing.T) {
	_, ok := byObject(t, CacheOptions(nil), &discoveryv1.EndpointSlice{})
	assert.False(t, ok)
}

func TestCacheOptions_WatchNamespaces(t *testing.T) {
	assert.Nil(t, CacheOptions(nil).DefaultNamespaces, "no flag: cluster-wide")

	opts := CacheOptions([]string{"shop", "blog"})
	assert.Len(t, opts.DefaultNamespaces, 2)
	assert.Contains(t, opts.DefaultNamespaces, "shop")
	assert.Contains(t, opts.DefaultNamespaces, "blog")
}

func TestCacheOptions_StripsManagedFields(t *testing.T) {
	assert.NotNil(t, CacheOptions(nil).DefaultTransform)
}

func TestSplitNamespaces(t *testing.T) {
	assert.Nil(t, SplitNamespaces(""))
	assert.Nil(t, SplitNamespaces(" , ,"))
	assert.Equal(t, []string{"shop"}, SplitNamespaces("shop"))
	assert.Equal(t, []string{"shop", "blog"}, SplitNamespaces(" shop, blog ,"))
}
