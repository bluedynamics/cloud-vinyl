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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	v1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
)

// reconcileEndpointSlice creates/updates an EndpointSlice in vc.Namespace pointing to
// the operator's own pod IP. This makes the operator reachable via the invalidation
// service so that PURGE/BAN requests forwarded by Varnish arrive at the operator.
//
// No OwnerReference is set — the EndpointSlice is cleaned up by the finalizer.
func (r *VinylCacheReconciler) reconcileEndpointSlice(ctx context.Context, vc *v1alpha1.VinylCache) error {
	invalidationSvcName := vc.Name + "-invalidation"
	esName := vc.Name + "-invalidation-operator"

	es := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      esName,
			Namespace: vc.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, es, func() error {
		// No OwnerReference — cleaned up by finalizer.
		es.Labels = map[string]string{
			"kubernetes.io/service-name": invalidationSvcName,
			labelVinylCacheName:          vc.Name,
		}

		port := int32(invalidationPort)
		proto := corev1.ProtocolTCP
		portName := "invalidation"
		addrType := discoveryv1.AddressTypeIPv4

		ready := true
		es.AddressType = addrType
		es.Ports = []discoveryv1.EndpointPort{
			{
				Name:     &portName,
				Port:     &port,
				Protocol: &proto,
			},
		}

		// Build endpoints from the operator's own pod IP.
		// If OperatorIP is not set (e.g. in tests), skip adding endpoints.
		if r.OperatorIP != "" {
			es.Endpoints = []discoveryv1.Endpoint{
				{
					Addresses: []string{r.OperatorIP},
					Conditions: discoveryv1.EndpointConditions{
						Ready: &ready,
					},
				},
			}
		} else {
			es.Endpoints = []discoveryv1.Endpoint{}
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("reconciling invalidation EndpointSlice: %w", err)
	}
	return nil
}
