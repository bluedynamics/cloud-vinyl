/*
Copyright 2026. Licensed under the Apache License, Version 2.0.
*/

package controller

import (
	"context"
	"fmt"
	"sort"

	discoveryv1 "k8s.io/api/discovery/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
	"github.com/bluedynamics/cloud-vinyl/internal/generator"
)

// resolveBackendEndpoints returns, for each spec.backend, the list of Ready and
// non-Terminating per-pod endpoints discovered via EndpointSlice.
//
// Returning an empty slice for a backend is valid — the generator will emit
// a director with no add_backend() calls, which Varnish flags as "no healthy
// backends" until the next endpoint change triggers a reconcile. Callers must
// not treat missing endpoints as an error.
func (r *VinylCacheReconciler) resolveBackendEndpoints(
	ctx context.Context,
	vc *v1alpha1.VinylCache,
) (map[string][]generator.Endpoint, error) {
	out := make(map[string][]generator.Endpoint, len(vc.Spec.Backends))
	for _, b := range vc.Spec.Backends {
		eps, err := r.listBackendEndpoints(ctx, vc.Namespace, b)
		if err != nil {
			return nil, fmt.Errorf("backend %q: %w", b.Name, err)
		}
		out[b.Name] = eps
	}
	return out, nil
}

func (r *VinylCacheReconciler) listBackendEndpoints(
	ctx context.Context,
	namespace string,
	b v1alpha1.BackendSpec,
) ([]generator.Endpoint, error) {
	list := &discoveryv1.EndpointSliceList{}
	if err := r.List(ctx, list,
		client.InNamespace(namespace),
		client.MatchingLabels{discoveryv1.LabelServiceName: b.ServiceRef.Name},
	); err != nil {
		return nil, fmt.Errorf("listing EndpointSlices for service %s: %w", b.ServiceRef.Name, err)
	}

	var endpoints []generator.Endpoint
	for _, slice := range list.Items {
		port := pickPort(slice.Ports, b)
		if port == 0 {
			continue
		}
		for _, ep := range slice.Endpoints {
			if !endpointReady(ep) {
				continue
			}
			for _, addr := range ep.Addresses {
				endpoints = append(endpoints, generator.Endpoint{IP: addr, Port: port})
			}
		}
	}
	sort.Slice(endpoints, func(i, j int) bool { return endpoints[i].IP < endpoints[j].IP })
	return endpoints, nil
}

// endpointReady returns true only when Ready=true and Terminating is not true.
// A nil Ready pointer is treated as ready (pre-1.22 fallback).
func endpointReady(ep discoveryv1.Endpoint) bool {
	if ep.Conditions.Terminating != nil && *ep.Conditions.Terminating {
		return false
	}
	if ep.Conditions.Ready == nil {
		return true
	}
	return *ep.Conditions.Ready
}

// pickPort selects the port for a backend: spec.backends[].port overrides the
// slice port; otherwise the first port with a non-nil Port value is used.
// For multi-port services set spec.backends[].port explicitly to avoid
// depending on Service port ordering.
func pickPort(ports []discoveryv1.EndpointPort, b v1alpha1.BackendSpec) int {
	if b.Port > 0 {
		return int(b.Port)
	}
	for _, p := range ports {
		if p.Port != nil {
			return int(*p.Port)
		}
	}
	return 0
}
