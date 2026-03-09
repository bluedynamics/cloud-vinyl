package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStaticRouter(t *testing.T) {
	routes := map[string][2]string{
		"my-cache-invalidation.production":                   {"production", "my-cache"},
		"my-cache-invalidation.production.svc.cluster.local": {"production", "my-cache"},
		"other-cache-invalidation.staging.svc.cluster.local": {"staging", "other-cache"},
	}
	r := NewStaticRouter(routes)

	t.Run("known short host", func(t *testing.T) {
		ns, name, ok := r.Lookup("my-cache-invalidation.production")
		require.True(t, ok)
		assert.Equal(t, "production", ns)
		assert.Equal(t, "my-cache", name)
	})

	t.Run("known FQDN host", func(t *testing.T) {
		ns, name, ok := r.Lookup("my-cache-invalidation.production.svc.cluster.local")
		require.True(t, ok)
		assert.Equal(t, "production", ns)
		assert.Equal(t, "my-cache", name)
	})

	t.Run("known FQDN with port", func(t *testing.T) {
		ns, name, ok := r.Lookup("my-cache-invalidation.production.svc.cluster.local:8090")
		require.True(t, ok)
		assert.Equal(t, "production", ns)
		assert.Equal(t, "my-cache", name)
	})

	t.Run("unknown host returns false", func(t *testing.T) {
		_, _, ok := r.Lookup("nonexistent.example.com")
		assert.False(t, ok)
	})
}

func TestRegisteredRouter(t *testing.T) {
	r := NewRegisteredRouter()

	t.Run("empty router returns false", func(t *testing.T) {
		_, _, ok := r.Lookup("my-cache-invalidation.production")
		assert.False(t, ok)
	})

	r.Register("production", "my-cache")

	t.Run("short form after Register", func(t *testing.T) {
		ns, name, ok := r.Lookup("my-cache-invalidation.production")
		require.True(t, ok)
		assert.Equal(t, "production", ns)
		assert.Equal(t, "my-cache", name)
	})

	t.Run("FQDN form after Register", func(t *testing.T) {
		ns, name, ok := r.Lookup("my-cache-invalidation.production.svc.cluster.local")
		require.True(t, ok)
		assert.Equal(t, "production", ns)
		assert.Equal(t, "my-cache", name)
	})

	t.Run("FQDN with port after Register", func(t *testing.T) {
		ns, name, ok := r.Lookup("my-cache-invalidation.production.svc.cluster.local:8090")
		require.True(t, ok)
		assert.Equal(t, "production", ns)
		assert.Equal(t, "my-cache", name)
	})

	t.Run("after Unregister returns false", func(t *testing.T) {
		r.Unregister("production", "my-cache")
		_, _, ok := r.Lookup("my-cache-invalidation.production")
		assert.False(t, ok)
		_, _, ok = r.Lookup("my-cache-invalidation.production.svc.cluster.local")
		assert.False(t, ok)
	})

	t.Run("multiple caches coexist", func(t *testing.T) {
		r2 := NewRegisteredRouter()
		r2.Register("ns1", "cache-a")
		r2.Register("ns2", "cache-b")

		ns, name, ok := r2.Lookup("cache-a-invalidation.ns1")
		require.True(t, ok)
		assert.Equal(t, "ns1", ns)
		assert.Equal(t, "cache-a", name)

		ns, name, ok = r2.Lookup("cache-b-invalidation.ns2")
		require.True(t, ok)
		assert.Equal(t, "ns2", ns)
		assert.Equal(t, "cache-b", name)
	})
}
