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

	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	v1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
)

// ensureFinalizer adds the vinyl finalizer to the VinylCache object if not present.
func (r *VinylCacheReconciler) ensureFinalizer(ctx context.Context, vc *v1alpha1.VinylCache) error {
	if controllerutil.ContainsFinalizer(vc, finalizerName) {
		return nil
	}
	controllerutil.AddFinalizer(vc, finalizerName)
	if err := r.Update(ctx, vc); err != nil {
		return fmt.Errorf("adding finalizer: %w", err)
	}
	return nil
}

// handleDeletion removes resources not covered by OwnerReferences (cross-namespace
// or explicitly excluded) and then removes the finalizer so Kubernetes can delete the object.
func (r *VinylCacheReconciler) handleDeletion(ctx context.Context, vc *v1alpha1.VinylCache) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(vc, finalizerName) {
		return ctrl.Result{}, nil
	}

	// Delete EndpointSlices in vc.Namespace that belong to the invalidation service.
	if err := r.deleteInvalidationEndpointSlices(ctx, vc); err != nil {
		return ctrl.Result{}, err
	}

	// Clean up proxy routing and pod map.
	if r.ProxyRouter != nil {
		r.ProxyRouter.Unregister(vc.Namespace, vc.Name)
	}
	if r.ProxyPodMap != nil {
		r.ProxyPodMap.Delete(vc.Namespace, vc.Name)
	}

	// Remove finalizer — OwnerRef-controlled resources (StatefulSet, headless service,
	// traffic service, secret) will be garbage-collected by Kubernetes automatically.
	controllerutil.RemoveFinalizer(vc, finalizerName)
	if err := r.Update(ctx, vc); err != nil {
		return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// deleteInvalidationEndpointSlices removes EndpointSlices in vc.Namespace that were
// created for the invalidation service. These have no OwnerReference because the
// invalidation service is managed by the operator (same namespace, but no owner reference
// to allow finalizer-based cleanup).
func (r *VinylCacheReconciler) deleteInvalidationEndpointSlices(ctx context.Context, vc *v1alpha1.VinylCache) error {
	invalidationSvcName := vc.Name + "-invalidation"

	// List EndpointSlices labeled with the invalidation service name.
	esList := &discoveryv1.EndpointSliceList{}
	selector := labels.Set{
		"kubernetes.io/service-name": invalidationSvcName,
		labelVinylCacheName:          vc.Name,
	}
	if err := r.List(ctx, esList,
		client.InNamespace(vc.Namespace),
		client.MatchingLabels(selector),
	); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("listing invalidation EndpointSlices: %w", err)
	}

	for i := range esList.Items {
		es := &esList.Items[i]
		// Skip if already being deleted.
		if !es.DeletionTimestamp.IsZero() {
			continue
		}
		if err := r.Delete(ctx, es, &client.DeleteOptions{
			Preconditions: &metav1.Preconditions{ResourceVersion: &es.ResourceVersion},
		}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting EndpointSlice %s: %w", es.Name, err)
		}
	}
	return nil
}
