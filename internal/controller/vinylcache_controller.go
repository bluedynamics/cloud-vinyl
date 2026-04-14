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
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
	"github.com/bluedynamics/cloud-vinyl/internal/generator"
	"github.com/bluedynamics/cloud-vinyl/internal/proxy"
)

const (
	// finalizerName is the finalizer added to VinylCache objects to ensure
	// cross-namespace cleanup (e.g., invalidation EndpointSlices) on deletion.
	finalizerName = "vinyl.bluedynamics.eu/finalizer"
)

// VinylCacheReconciler reconciles a VinylCache object.
type VinylCacheReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	Generator   generator.Generator
	AgentClient AgentClient
	// OperatorIP is the operator pod's own IP address, used to populate the
	// invalidation EndpointSlice. Set from the POD_IP environment variable.
	OperatorIP string
	// Proxy integration (optional — nil when proxy is disabled).
	ProxyRouter *proxy.RegisteredRouter
	ProxyPodMap *proxy.PodMap
	debouncer   *debouncer // lazy-init in SetupWithManager
}

// +kubebuilder:rbac:groups=vinyl.bluedynamics.eu,resources=vinylcaches,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=vinyl.bluedynamics.eu,resources=vinylcaches/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=vinyl.bluedynamics.eu,resources=vinylcaches/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods;pods/status;services;endpoints;secrets;serviceaccounts;events,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=discovery.k8s.io,resources=endpointslices,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete

// Reconcile is the main reconciliation loop for VinylCache objects.
func (r *VinylCacheReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// 1. Load VinylCache.
	vc := &v1alpha1.VinylCache{}
	if err := r.Get(ctx, req.NamespacedName, vc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// 2. Deletion handling.
	if !vc.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, vc)
	}

	// 3. Ensure finalizer.
	if err := r.ensureFinalizer(ctx, vc); err != nil {
		return ctrl.Result{}, err
	}

	// 4. Pause annotation check.
	if vc.Annotations["vinyl.bluedynamics.eu/pause-vcl-push"] == "true" {
		log.Info("VCL push paused by annotation")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// 5. Reconcile StatefulSet.
	if err := r.reconcileStatefulSet(ctx, vc); err != nil {
		return ctrl.Result{}, err
	}

	// 6. Reconcile Services.
	if err := r.reconcileServices(ctx, vc); err != nil {
		return ctrl.Result{}, err
	}

	// 7. Reconcile EndpointSlice.
	if err := r.reconcileEndpointSlice(ctx, vc); err != nil {
		return ctrl.Result{}, err
	}

	// 8. Reconcile NetworkPolicies.
	if err := r.reconcileNetworkPolicies(ctx, vc); err != nil {
		return ctrl.Result{}, err
	}

	// 9. Reconcile Secret.
	if err := r.reconcileSecret(ctx, vc); err != nil {
		return ctrl.Result{}, err
	}

	// 9b. Reconcile bootstrap VCL ConfigMap.
	if err := r.reconcileConfigMap(ctx, vc); err != nil {
		return ctrl.Result{}, err
	}

	// 10. Debounce check.
	if remaining := r.debounceRemaining(vc); remaining > 0 {
		return ctrl.Result{RequeueAfter: remaining}, nil
	}

	// 11. Collect ready peers, generate VCL, push if changed.
	peers, err := r.collectReadyPeers(ctx, vc)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Update proxy routing and pod map.
	if r.ProxyRouter != nil {
		r.ProxyRouter.Register(vc.Namespace, vc.Name)
	}
	if r.ProxyPodMap != nil {
		var podIPs []string
		for _, p := range peers {
			podIPs = append(podIPs, p.IP)
		}
		r.ProxyPodMap.Update(vc.Namespace, vc.Name, podIPs)
	}

	activeHash := ""
	if vc.Status.ActiveVCL != nil {
		activeHash = vc.Status.ActiveVCL.Hash
	}

	// Resolve backend endpoints from Kubernetes Services.
	endpoints, err := r.resolveBackendEndpoints(ctx, vc)
	if err != nil {
		return ctrl.Result{}, err
	}

	genResult, err := r.Generator.Generate(generator.Input{
		Spec:      &vc.Spec,
		Peers:     peers,
		Endpoints: endpoints,
		Namespace: vc.Namespace,
		Name:      vc.Name,
	})
	if err != nil {
		r.setErrorStatus(ctx, vc, err)
		return ctrl.Result{}, err
	}

	if genResult.Hash != activeHash || len(peers) != len(vc.Status.ClusterPeers) {
		if err := r.pushVCL(ctx, vc, genResult, peers); err != nil {
			r.setErrorStatus(ctx, vc, err)
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
	}

	// 12. Update status and requeue.
	r.updateStatus(ctx, vc, genResult, peers)

	// Requeue quickly if not all replicas are ready yet.
	if int32(len(peers)) < vc.Spec.Replicas {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	// All replicas ready — requeue for drift detection.
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// podToVinylCache maps a Pod event to the owning VinylCache reconcile request.
func (r *VinylCacheReconciler) podToVinylCache(_ context.Context, obj client.Object) []reconcile.Request {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil
	}
	cacheName, ok := pod.Labels[labelVinylCacheName]
	if !ok {
		return nil
	}
	return []reconcile.Request{
		{NamespacedName: client.ObjectKey{Name: cacheName, Namespace: pod.Namespace}},
	}
}

// endpointSliceToVinylCache maps an EndpointSlice event to every VinylCache
// in the same namespace that references the slice's Service via a backend.
func (r *VinylCacheReconciler) endpointSliceToVinylCache(ctx context.Context, obj client.Object) []reconcile.Request {
	es, ok := obj.(*discoveryv1.EndpointSlice)
	if !ok {
		return nil
	}
	svcName := es.Labels[discoveryv1.LabelServiceName]
	if svcName == "" {
		return nil
	}
	list := &v1alpha1.VinylCacheList{}
	if err := r.List(ctx, list, client.InNamespace(es.Namespace)); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for i := range list.Items {
		vc := &list.Items[i]
		for _, b := range vc.Spec.Backends {
			if b.ServiceRef.Name == svcName {
				reqs = append(reqs, reconcile.Request{
					NamespacedName: client.ObjectKey{Name: vc.Name, Namespace: vc.Namespace},
				})
				break
			}
		}
	}
	for _, req := range reqs {
		if r.debouncer != nil {
			r.debouncer.touch(req.NamespacedName)
		}
	}
	return reqs
}

// SetupWithManager registers the controller with the manager.
func (r *VinylCacheReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.debouncer == nil {
		r.debouncer = newDebouncer()
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.VinylCache{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.Secret{}).
		Owns(&corev1.ConfigMap{}).
		Watches(
			&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(r.podToVinylCache),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Watches(
			&discoveryv1.EndpointSlice{},
			handler.EnqueueRequestsFromMapFunc(r.endpointSliceToVinylCache),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Named("vinylcache").
		Complete(r)
}
