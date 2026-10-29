package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	vinylv1 "github.com/bluedynamics/cloud-vinyl/api/v1"
	"github.com/bluedynamics/cloud-vinyl/internal/generator"
	"github.com/bluedynamics/cloud-vinyl/internal/proxy"
)

// spec.invalidation.purge.hosts must reach the proxy, which forwards a purge
// without X-Vinyl-Purge-Host with these hosts (#134); removing them must
// clear them.
func TestUpdateProxy_PublishesPurgeHosts(t *testing.T) {
	router, pods := proxy.NewRegisteredRouter(), proxy.NewPodMap()
	r := &VinylCacheReconciler{ProxyRouter: router, ProxyPodMap: pods}
	vc := &vinylv1.VinylCache{ObjectMeta: metav1.ObjectMeta{Name: "my-cache", Namespace: "app"}}
	vc.Spec.Invalidation.Purge = &vinylv1.PurgeSpec{Enabled: true, Hosts: []string{"example.org", "www.example.org"}}

	r.updateProxy(vc, []generator.PeerBackend{{Name: "my_cache_0", IP: "10.0.0.1"}})

	ns, name, ok := router.Lookup("my-cache-invalidation.app")
	assert.True(t, ok)
	assert.Equal(t, [2]string{"app", "my-cache"}, [2]string{ns, name})
	assert.Equal(t, []string{"example.org", "www.example.org"}, router.PurgeHosts("app", "my-cache"))
	assert.Equal(t, []string{"10.0.0.1"}, pods.GetPodIPs("app", "my-cache"))

	vc.Spec.Invalidation.Purge.Hosts = nil
	r.updateProxy(vc, nil)
	assert.Empty(t, router.PurgeHosts("app", "my-cache"))
}
