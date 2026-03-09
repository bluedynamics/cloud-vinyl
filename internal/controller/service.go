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
	"maps"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	v1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
)

const invalidationPort = 8090

// reconcileServices creates or updates the three services for a VinylCache:
//  1. Headless service (cluster-internal pod-to-pod communication)
//  2. Traffic service (main cache ingress)
//  3. Invalidation service (PURGE/BAN proxy, no selector — operator manages EndpointSlice)
func (r *VinylCacheReconciler) reconcileServices(ctx context.Context, vc *v1alpha1.VinylCache) error {
	if err := r.reconcileHeadlessService(ctx, vc); err != nil {
		return err
	}
	if err := r.reconcileTrafficService(ctx, vc); err != nil {
		return err
	}
	if err := r.reconcileInvalidationService(ctx, vc); err != nil {
		return err
	}
	return nil
}

// reconcileHeadlessService creates/updates the headless service used by the StatefulSet.
func (r *VinylCacheReconciler) reconcileHeadlessService(ctx context.Context, vc *v1alpha1.VinylCache) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vc.Name,
			Namespace: vc.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		if err := ctrl.SetControllerReference(vc, svc, r.Scheme); err != nil {
			return err
		}
		svc.Labels = map[string]string{
			"app":               vc.Name,
			labelVinylCacheName: vc.Name,
		}
		svc.Spec = corev1.ServiceSpec{
			ClusterIP: "None",
			Selector:  map[string]string{"app": vc.Name},
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       varnishPort,
					TargetPort: intstr.FromInt32(varnishPort),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("reconciling headless Service: %w", err)
	}
	return nil
}

// reconcileTrafficService creates/updates the main traffic service.
func (r *VinylCacheReconciler) reconcileTrafficService(ctx context.Context, vc *v1alpha1.VinylCache) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vc.Name + "-traffic",
			Namespace: vc.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		if err := ctrl.SetControllerReference(vc, svc, r.Scheme); err != nil {
			return err
		}

		svcType := corev1.ServiceTypeClusterIP
		if vc.Spec.Service.Type != "" {
			svcType = corev1.ServiceType(vc.Spec.Service.Type)
		}

		svc.Labels = map[string]string{
			"app":               vc.Name,
			labelVinylCacheName: vc.Name,
		}
		// Merge user-defined service annotations.
		if len(vc.Spec.Service.Annotations) > 0 {
			if svc.Annotations == nil {
				svc.Annotations = make(map[string]string)
			}
			maps.Copy(svc.Annotations, vc.Spec.Service.Annotations)
		}

		svc.Spec = corev1.ServiceSpec{
			Type:     svcType,
			Selector: map[string]string{"app": vc.Name},
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       varnishPort,
					TargetPort: intstr.FromInt32(varnishPort),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("reconciling traffic Service: %w", err)
	}
	return nil
}

// reconcileInvalidationService creates/updates the invalidation service.
// This service has no selector — the operator manages its EndpointSlice directly.
// No OwnerReference is set because cleanup is handled via the finalizer.
func (r *VinylCacheReconciler) reconcileInvalidationService(ctx context.Context, vc *v1alpha1.VinylCache) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vc.Name + "-invalidation",
			Namespace: vc.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		// No OwnerReference — cleaned up by finalizer.
		svc.Labels = map[string]string{
			"app":               vc.Name,
			labelVinylCacheName: vc.Name,
		}
		svc.Spec = corev1.ServiceSpec{
			// No Selector — operator manages the EndpointSlice.
			Ports: []corev1.ServicePort{
				{
					Name:       "invalidation",
					Port:       invalidationPort,
					TargetPort: intstr.FromInt32(invalidationPort),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("reconciling invalidation Service: %w", err)
	}
	return nil
}
