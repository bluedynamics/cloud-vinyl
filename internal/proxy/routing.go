package proxy

import (
	"strings"
	"sync"
)

// Router resolves a Host header to a VinylCache namespace and name.
type Router interface {
	Lookup(host string) (namespace, cacheName string, ok bool)
}

// routeEntry holds the resolved namespace and cacheName for a host.
type routeEntry struct {
	namespace string
	cacheName string
}

// StaticRouter is a simple, immutable Router backed by a map. Useful for tests.
type StaticRouter struct {
	routes map[string]routeEntry
}

// NewStaticRouter creates a StaticRouter from a map of host → {namespace, cacheName}.
func NewStaticRouter(routes map[string][2]string) *StaticRouter {
	m := make(map[string]routeEntry, len(routes))
	for host, ns := range routes {
		m[host] = routeEntry{namespace: ns[0], cacheName: ns[1]}
	}
	return &StaticRouter{routes: m}
}

// Lookup returns the namespace and cacheName for the given host.
func (r *StaticRouter) Lookup(host string) (string, string, bool) {
	// Strip optional port suffix.
	if idx := strings.LastIndex(host, ":"); idx >= 0 {
		host = host[:idx]
	}
	e, ok := r.routes[host]
	return e.namespace, e.cacheName, ok
}

// RegisteredRouter maintains a thread-safe map of host → {namespace, cacheName}.
// It is updated externally by the controller when VinylCache objects are
// created, updated, or deleted.
//
// Host header formats supported:
//   - <cache-name>-invalidation.<namespace>.svc.cluster.local  (FQDN)
//   - <cache-name>-invalidation.<namespace>                    (short)
type RegisteredRouter struct {
	mu     sync.RWMutex
	routes map[string]routeEntry
}

// NewRegisteredRouter creates an empty RegisteredRouter.
func NewRegisteredRouter() *RegisteredRouter {
	return &RegisteredRouter{
		routes: make(map[string]routeEntry),
	}
}

// Register adds or updates the routing entry for the given namespace/cacheName.
// It registers both the short and FQDN host forms.
func (r *RegisteredRouter) Register(namespace, cacheName string) {
	shortHost := cacheName + "-invalidation." + namespace
	fqdnHost := shortHost + ".svc.cluster.local"

	entry := routeEntry{namespace: namespace, cacheName: cacheName}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.routes[shortHost] = entry
	r.routes[fqdnHost] = entry
}

// Unregister removes routing entries for the given namespace/cacheName.
func (r *RegisteredRouter) Unregister(namespace, cacheName string) {
	shortHost := cacheName + "-invalidation." + namespace
	fqdnHost := shortHost + ".svc.cluster.local"

	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.routes, shortHost)
	delete(r.routes, fqdnHost)
}

// Lookup returns the namespace and cacheName for the given host.
func (r *RegisteredRouter) Lookup(host string) (string, string, bool) {
	// Strip optional port suffix.
	if idx := strings.LastIndex(host, ":"); idx >= 0 {
		host = host[:idx]
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.routes[host]
	return e.namespace, e.cacheName, ok
}
