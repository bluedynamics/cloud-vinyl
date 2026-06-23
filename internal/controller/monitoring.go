package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
	"github.com/bluedynamics/cloud-vinyl/internal/monitoring"
)

var (
	serviceMonitorGVK = schema.GroupVersionKind{Group: "monitoring.coreos.com", Version: "v1", Kind: "ServiceMonitor"}
	prometheusRuleGVK = schema.GroupVersionKind{Group: "monitoring.coreos.com", Version: "v1", Kind: "PrometheusRule"}
)

// reconcileMonitoring creates/updates the ServiceMonitor and PrometheusRule when
// requested in the spec AND the prometheus-operator CRDs are installed. It never
// returns an error when the CRDs are absent — clusters without prometheus-operator
// must reconcile normally.
func (r *VinylCacheReconciler) reconcileMonitoring(ctx context.Context, vc *v1alpha1.VinylCache) error {
	log := logf.FromContext(ctx)

	if sm := vc.Spec.Monitoring.ServiceMonitor; sm != nil && sm.Enabled {
		if !r.crdInstalled(serviceMonitorGVK) {
			log.Info("ServiceMonitor requested but monitoring.coreos.com CRD not installed; skipping")
		} else {
			obj, err := toUnstructured(monitoring.GenerateServiceMonitor(vc.Name, vc.Namespace), serviceMonitorGVK)
			if err != nil {
				return err
			}
			if err := r.applyOwned(ctx, vc, obj); err != nil {
				return err
			}
		}
	}

	if pr := vc.Spec.Monitoring.PrometheusRules; pr != nil && pr.Enabled {
		if !r.crdInstalled(prometheusRuleGVK) {
			log.Info("PrometheusRule requested but monitoring.coreos.com CRD not installed; skipping")
		} else {
			obj, err := toUnstructured(monitoring.GeneratePrometheusRule(vc.Namespace), prometheusRuleGVK)
			if err != nil {
				return err
			}
			obj.SetNamespace(vc.Namespace)
			if err := r.applyOwned(ctx, vc, obj); err != nil {
				return err
			}
		}
	}
	return nil
}

// crdInstalled reports whether the cluster knows the given GVK.
func (r *VinylCacheReconciler) crdInstalled(gvk schema.GroupVersionKind) bool {
	mapper := r.Client.RESTMapper()
	if mapper == nil {
		return false
	}
	_, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	return err == nil
}

// toUnstructured converts any of our minimal monitoring structs to an
// Unstructured carrying the given GVK, so it can be applied without a typed
// prometheus-operator dependency.
func toUnstructured(in any, gvk schema.GroupVersionKind) (*unstructured.Unstructured, error) {
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(in)
	if err != nil {
		return nil, err
	}
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(gvk)
	return u, nil
}

// applyOwned sets the controller owner reference and create-or-updates the object.
func (r *VinylCacheReconciler) applyOwned(ctx context.Context, vc *v1alpha1.VinylCache, obj *unstructured.Unstructured) error {
	if err := controllerutil.SetControllerReference(vc, obj, r.Scheme); err != nil {
		return err
	}
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(obj.GroupVersionKind())
	key := client.ObjectKeyFromObject(obj)
	if err := r.Get(ctx, key, existing); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return err
		}
		return r.Create(ctx, obj)
	}
	obj.SetResourceVersion(existing.GetResourceVersion())
	return r.Update(ctx, obj)
}
