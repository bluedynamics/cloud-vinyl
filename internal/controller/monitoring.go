package controller

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	vinylv1 "github.com/bluedynamics/cloud-vinyl/api/v1"
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
func (r *VinylCacheReconciler) reconcileMonitoring(ctx context.Context, vc *vinylv1.VinylCache) error {
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
			obj, err := toUnstructured(monitoring.GeneratePrometheusRule(vc.Name, vc.Namespace, ruleOptions(pr)), prometheusRuleGVK)
			if err != nil {
				return err
			}
			if err := r.applyOwned(ctx, vc, obj); err != nil {
				return err
			}
			if err := r.deleteLegacyPrometheusRule(ctx, vc); err != nil {
				return err
			}
		}
	}
	return nil
}

// ruleOptions maps the optional spec tuning to the generator's options.
func ruleOptions(pr *vinylv1.PrometheusRulesSpec) monitoring.Options {
	var opts monitoring.Options
	if lhr := pr.LowHitRatio; lhr != nil {
		if lhr.Threshold != nil {
			opts.LowHitRatioThreshold = *lhr.Threshold
		}
		opts.LowHitRatioFor = lhr.For
	}
	return opts
}

// deleteLegacyPrometheusRule removes the rule every VinylCache shared before
// #143 under one fixed name, if this cache controls it. When two caches in a
// namespace fought over it, the other one deletes it on its next reconcile.
func (r *VinylCacheReconciler) deleteLegacyPrometheusRule(ctx context.Context, vc *vinylv1.VinylCache) error {
	legacy := &unstructured.Unstructured{}
	legacy.SetGroupVersionKind(prometheusRuleGVK)
	key := client.ObjectKey{Namespace: vc.Namespace, Name: monitoring.LegacyPrometheusRuleName}
	if err := r.Get(ctx, key, legacy); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !metav1.IsControlledBy(legacy, vc) {
		return nil
	}
	return client.IgnoreNotFound(r.Delete(ctx, legacy))
}

// crdInstalled reports whether the cluster knows the given GVK.
func (r *VinylCacheReconciler) crdInstalled(gvk schema.GroupVersionKind) bool {
	mapper := r.RESTMapper()
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
func (r *VinylCacheReconciler) applyOwned(ctx context.Context, vc *vinylv1.VinylCache, obj *unstructured.Unstructured) error {
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
