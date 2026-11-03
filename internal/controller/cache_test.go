package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
		&appsv1.StatefulSet{}, &corev1.Service{}, &corev1.ConfigMap{},
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
