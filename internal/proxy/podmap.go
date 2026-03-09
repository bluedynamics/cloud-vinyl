package proxy

import "sync"

// PodIPProvider is the interface for retrieving pod IPs for a VinylCache cluster.
type PodIPProvider interface {
	GetPodIPs(namespace, cacheName string) []string
}

// PodMap is a thread-safe in-memory map of pod IPs, updated by the controller
// when pods change. The map key is "namespace/cacheName".
type PodMap struct {
	mu   sync.RWMutex
	data map[string][]string
}

// NewPodMap creates a new, empty PodMap.
func NewPodMap() *PodMap {
	return &PodMap{
		data: make(map[string][]string),
	}
}

// GetPodIPs returns a copy of the pod IPs for the given namespace and cacheName.
// Returns nil if no entry exists.
func (pm *PodMap) GetPodIPs(namespace, cacheName string) []string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	key := namespace + "/" + cacheName
	ips, ok := pm.data[key]
	if !ok {
		return nil
	}
	// Return a copy to prevent mutation of internal state.
	result := make([]string, len(ips))
	copy(result, ips)
	return result
}

// Update sets the pod IPs for a given namespace and cacheName.
func (pm *PodMap) Update(namespace, cacheName string, ips []string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	key := namespace + "/" + cacheName
	stored := make([]string, len(ips))
	copy(stored, ips)
	pm.data[key] = stored
}

// Delete removes the entry for a given namespace and cacheName.
func (pm *PodMap) Delete(namespace, cacheName string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	key := namespace + "/" + cacheName
	delete(pm.data, key)
}
