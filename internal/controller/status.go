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
	"errors"
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	vinylv1 "github.com/bluedynamics/cloud-vinyl/api/v1"
	"github.com/bluedynamics/cloud-vinyl/internal/catalog"
	"github.com/bluedynamics/cloud-vinyl/internal/generator"
)

// reasonScaledToZero is the reason of the conditions that report a cache
// switched off on purpose (spec.replicas: 0).
const reasonScaledToZero = "ScaledToZero"

// updateStatus refreshes the VinylCache status after a successful reconciliation.
func (r *VinylCacheReconciler) updateStatus(
	ctx context.Context,
	vc *vinylv1.VinylCache,
	result *generator.Result,
	obs podObservation,
	pushedCount int,
) {
	peers := obs.ready
	log := logf.FromContext(ctx)

	vc.Status.InvalidationURL = invalidationURL(vc)

	// Only claim a VCL is active once it actually reached a pod. The reconciler
	// decides whether to push by comparing this hash against the freshly
	// generated one, so recording a hash that was pushed to nobody makes the
	// next reconcile skip the push and the cache never converges (#77).
	//
	// pushedCount is also 0 when the push was skipped because the VCL had not
	// changed. Leaving the existing status untouched is correct there too.
	if pushedCount > 0 {
		now := metav1.NewTime(time.Now())
		vc.Status.ActiveVCL = &vinylv1.ActiveVCLStatus{
			Name:     fmt.Sprintf("%s-%s", vc.Namespace, vc.Name),
			Hash:     result.Hash,
			PushedAt: &now,
		}
	}

	if r.Metrics != nil {
		// Distinct VCL versions actually observed across the pods. This is the
		// quantity the VinylCacheVCLDrift alert describes ("more than 2 VCL
		// versions loaded"); until now the gauge was hardcoded to 1, so the
		// alert could never fire. A pod whose agent could not be reached
		// reports no hash, which is absence of information rather than another
		// version, so it does not count (spec E4).
		distinct := make(map[string]struct{}, len(obs.vclNames))
		for _, h := range obs.vclNames {
			if h != "" {
				distinct[h] = struct{}{}
			}
		}
		r.Metrics.VCLVersionsLoaded.WithLabelValues(vc.Name, vc.Namespace).Set(float64(len(distinct)))
	}

	// Rebuild ClusterPeers from every reachable pod, not just the Ready ones,
	// and with values that mean something: the pod's real Ready state and the
	// VCL hash it actually reports. Recording the desired hash for everyone
	// made drift undetectable by construction (spec E3/E4).
	readyByName := make(map[string]bool, len(peers))
	for _, p := range peers {
		readyByName[p.Name] = true
	}
	vc.Status.ClusterPeers = make([]vinylv1.ClusterPeerStatus, 0, len(obs.reachable))
	for _, p := range obs.reachable {
		vc.Status.ClusterPeers = append(vc.Status.ClusterPeers, vinylv1.ClusterPeerStatus{
			PodName:       p.Name,
			Ready:         readyByName[p.Name],
			ActiveVCLHash: obs.vclNames[p.IP],
		})
	}
	vc.Status.ReadyPeers = int32(len(peers))
	vc.Status.TotalPeers = vc.Spec.Replicas
	r.setReplicaStatus(ctx, vc)

	allReplicasReady := int32(len(peers)) >= vc.Spec.Replicas
	scaledToZero := vc.Spec.Replicas == 0

	// VCLSynced reflects whether VCL was pushed to all available peers (not blocked).
	// It stays True even when waiting for more replicas to start.
	// Reads "ready", not "pushed": this counts pods carrying the Ready
	// condition, not push targets. The old wording ("VCL pushed to 0/1 pods")
	// read like a push failure during the #77 investigation when it was in fact
	// reporting readiness.
	if scaledToZero {
		setCondition(vc, vinylv1.ConditionVCLSynced, metav1.ConditionTrue, reasonScaledToZero,
			"scaled to zero replicas, no pods to push the VCL to")
	} else {
		setCondition(vc, vinylv1.ConditionVCLSynced, metav1.ConditionTrue, "VCLPushed",
			fmt.Sprintf("VCL active on %d/%d ready pods", len(peers), vc.Spec.Replicas))
	}
	if len(obs.backendsWithoutEndpoints) > 0 {
		// The VCL is pushed regardless: the cache serves its other routes and
		// answers 503 for these, so Ready stays True. Degraded makes the
		// condition visible without taking the pods out of service.
		setCondition(vc, vinylv1.ConditionBackendsAvailable, metav1.ConditionFalse, "BackendWithoutEndpoints",
			"no ready endpoints for backend(s): "+strings.Join(obs.backendsWithoutEndpoints, ", "))
	} else {
		setCondition(vc, vinylv1.ConditionBackendsAvailable, metav1.ConditionTrue, "BackendsConfigured", "all backends have ready endpoints")
	}

	if findConditionStatus(vc, vinylv1.ConditionRuntimeResolved) == metav1.ConditionFalse {
		// The StatefulSet is held back, so the reconciliation is not complete
		// however many replicas happen to be ready.
		setCondition(vc, vinylv1.ConditionProgressing, metav1.ConditionTrue, "RuntimeNotInCatalog",
			"the StatefulSet is held back because the runtime is not in the catalog (see condition RuntimeResolved)")
	} else if scaledToZero {
		setCondition(vc, vinylv1.ConditionProgressing, metav1.ConditionFalse, reasonScaledToZero,
			"reconciliation complete, scaled to zero replicas")
	} else if allReplicasReady {
		setCondition(vc, vinylv1.ConditionProgressing, metav1.ConditionFalse, "ReconcileComplete", "reconciliation complete")
	} else {
		setCondition(vc, vinylv1.ConditionProgressing, metav1.ConditionTrue, "WaitingForReplicas",
			fmt.Sprintf("waiting for %d/%d replicas to become ready", len(peers), vc.Spec.Replicas))
	}

	// Ready is set before the phase is derived: a cache scaled to zero is
	// reported through Ready=True/ScaledToZero, which calculatePhase reads.
	switch {
	case scaledToZero:
		setCondition(vc, vinylv1.ConditionReady, metav1.ConditionTrue, reasonScaledToZero, "scaled to zero replicas")
	case allReplicasReady:
		setCondition(vc, vinylv1.ConditionReady, metav1.ConditionTrue, "AllReady", "VinylCache is ready")
	default:
		setCondition(vc, vinylv1.ConditionReady, metav1.ConditionFalse, "NotAllReady",
			fmt.Sprintf("%d/%d replicas ready", len(peers), vc.Spec.Replicas))
	}
	vc.Status.Phase = calculatePhase(vc)

	if err := r.Status().Update(ctx, vc); err != nil {
		log.Error(err, "updating VinylCache status")
	}
}

// setErrorStatus sets the VinylCache phase and conditions to reflect an error.
func (r *VinylCacheReconciler) setErrorStatus(ctx context.Context, vc *vinylv1.VinylCache, reconcileErr error) {
	log := logf.FromContext(ctx)

	vc.Status.Phase = vinylv1.PhaseError
	vc.Status.Message = reconcileErr.Error()
	setCondition(vc, vinylv1.ConditionReady, metav1.ConditionFalse, "ReconcileError", reconcileErr.Error())
	setCondition(vc, vinylv1.ConditionVCLSynced, metav1.ConditionFalse, "VCLPushFailed", reconcileErr.Error())

	if err := r.Status().Update(ctx, vc); err != nil {
		log.Error(err, "updating VinylCache error status")
	}
}

// calculatePhase derives the overall phase from the current conditions and replica state.
//
//   - Stopped:  Ready=True with reason ScaledToZero (spec.replicas is 0). Takes
//     precedence over Degraded: a stopped site usually has backends without
//     endpoints, which must not paint it as degraded.
//   - Ready:    VCLSynced=True and BackendsAvailable=True and all replicas ready
//   - Degraded: VCLSynced=False (VCL push failed or partial) or
//     BackendsAvailable=False (a backend has no endpoints)
//   - Error:    set explicitly via setErrorStatus
//   - Pending:  initial state or waiting for replicas
func calculatePhase(vc *vinylv1.VinylCache) string {
	if c := findCondition(vc, vinylv1.ConditionReady); c != nil &&
		c.Status == metav1.ConditionTrue && c.Reason == reasonScaledToZero {
		return vinylv1.PhaseStopped
	}
	vclSynced := findConditionStatus(vc, vinylv1.ConditionVCLSynced)
	backendsAvailable := findConditionStatus(vc, vinylv1.ConditionBackendsAvailable)

	if vclSynced == metav1.ConditionTrue && backendsAvailable == metav1.ConditionTrue &&
		vc.Status.ReadyPeers >= vc.Status.TotalPeers {
		return vinylv1.PhaseReady
	}
	if vclSynced == metav1.ConditionFalse || backendsAvailable == metav1.ConditionFalse {
		return vinylv1.PhaseDegraded
	}
	return vinylv1.PhasePending
}

// setCondition sets (or updates) a named condition on the VinylCache status.
func setCondition(vc *vinylv1.VinylCache, condType string, status metav1.ConditionStatus, reason, message string) {
	now := metav1.NewTime(time.Now())
	for i, c := range vc.Status.Conditions {
		if c.Type == condType {
			transitioned := now
			if c.Status == status {
				// Only a Status flip is a transition; reason, message and
				// observedGeneration are always refreshed so a repeated failure
				// with new details does not report a stale message.
				transitioned = c.LastTransitionTime
			}
			vc.Status.Conditions[i] = metav1.Condition{
				Type:               condType,
				Status:             status,
				Reason:             reason,
				Message:            message,
				LastTransitionTime: transitioned,
				ObservedGeneration: vc.Generation,
			}
			return
		}
	}
	// Condition not found — append it.
	vc.Status.Conditions = append(vc.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
		ObservedGeneration: vc.Generation,
	})
}

// findConditionStatus returns the Status of a condition by type, or "" if not found.
func findConditionStatus(vc *vinylv1.VinylCache, condType string) metav1.ConditionStatus {
	for _, c := range vc.Status.Conditions {
		if c.Type == condType {
			return c.Status
		}
	}
	return ""
}

// resolveRuntime resolves the runtime of a VinylCache from the catalog and
// records the outcome (status.runtime and the RuntimeResolved condition) in
// the persisted status right away. Persisting here, not in updateStatus,
// keeps the outcome visible when a later step returns early (debounce,
// backends without endpoints).
//
// It returns the effective runtime (catalog entry with the per-cache image
// overrides applied) and whether it resolved. A catalog entry that has
// vanished is reported as resolved=false with no error: the caller skips the
// StatefulSet so running pods keep their images, and there is no hot error
// loop. A nil catalog is a programming error and returned as an error.
func (r *VinylCacheReconciler) resolveRuntime(
	ctx context.Context, vc *vinylv1.VinylCache,
) (rt catalog.Runtime, resolved bool, err error) {
	if r.Catalog == nil {
		return catalog.Runtime{}, false, errors.New("runtime catalog is not configured")
	}
	log := logf.FromContext(ctx)

	prevRuntime := vc.Status.Runtime.DeepCopy()
	prevCond := findCondition(vc, vinylv1.ConditionRuntimeResolved)

	base, rerr := r.Catalog.Resolve(vc.Spec.Flavor, vc.Spec.Version)
	switch {
	case rerr == nil:
		rt = effectiveRuntime(vc, base)
		status := &vinylv1.RuntimeStatus{
			Flavor:  rt.Flavor,
			Version: rt.Version,
			Daemon:  rt.Daemon,
		}
		if exp := vc.Spec.Monitoring.Exporter; exp != nil && exp.Enabled {
			status.Exporter = rt.Exporter
		}
		if vc.Spec.Tracing.Enabled {
			status.Tracer = rt.Tracer
		}
		vc.Status.Runtime = status
		setCondition(vc, vinylv1.ConditionRuntimeResolved, metav1.ConditionTrue, "Resolved",
			fmt.Sprintf("runtime %s %s resolved from the catalog", rt.Flavor, rt.Version))
		resolved = true
	case errors.Is(rerr, catalog.ErrNotInCatalog):
		log.Info("runtime not in catalog, leaving the StatefulSet unchanged", "reason", rerr.Error())
		setCondition(vc, vinylv1.ConditionRuntimeResolved, metav1.ConditionFalse, "NotInCatalog", rerr.Error())
	default:
		return catalog.Runtime{}, false, fmt.Errorf("resolving runtime: %w", rerr)
	}

	// Skip the write when nothing changed: a concurrent writer's conflict
	// would otherwise abort the reconcile on every pass. Comparing the
	// condition as a whole is safe, setCondition keeps LastTransitionTime
	// unless the status flips.
	if equality.Semantic.DeepEqual(prevRuntime, vc.Status.Runtime) &&
		equality.Semantic.DeepEqual(prevCond, findCondition(vc, vinylv1.ConditionRuntimeResolved)) {
		return rt, resolved, nil
	}
	if err := r.Status().Update(ctx, vc); err != nil {
		return catalog.Runtime{}, false, fmt.Errorf("updating runtime status: %w", err)
	}
	return rt, resolved, nil
}

// findCondition returns a copy of the named condition, or nil if absent.
func findCondition(vc *vinylv1.VinylCache, condType string) *metav1.Condition {
	for _, c := range vc.Status.Conditions {
		if c.Type == condType {
			return c.DeepCopy()
		}
	}
	return nil
}

// logBackendEndpointTransition logs when the set of backends without
// endpoints changes, not on every reconcile. The previous state is the
// BackendsAvailable condition from the last status update.
func logBackendEndpointTransition(ctx context.Context, vc *vinylv1.VinylCache, missing []string) {
	wasMissing := findConditionStatus(vc, vinylv1.ConditionBackendsAvailable) == metav1.ConditionFalse
	if (len(missing) > 0) == wasMissing {
		return
	}
	log := logf.FromContext(ctx)
	if len(missing) > 0 {
		log.Info("backends without ready endpoints, serving 503 for them", "backends", missing)
		return
	}
	log.Info("all backends have ready endpoints again")
}

// podSelectorLabels are the labels the StatefulSet selects its pods by.
func podSelectorLabels(vc *vinylv1.VinylCache) map[string]string {
	return map[string]string{labelApp: vc.Name}
}

// setReplicaStatus copies the replica counts of the owned StatefulSet and
// sets the pod selector string. Both back the Replicas printer column and the
// scale subresource (statuspath/selectorpath), which read nothing else (#180).
// Without a StatefulSet (not created yet, or held back because the runtime is
// not in the catalog) the counts are 0; the selector is always set.
func (r *VinylCacheReconciler) setReplicaStatus(ctx context.Context, vc *vinylv1.VinylCache) {
	vc.Status.Selector = labels.SelectorFromSet(podSelectorLabels(vc)).String()

	sts := &appsv1.StatefulSet{}
	err := r.Get(ctx, client.ObjectKey{Namespace: vc.Namespace, Name: vc.Name}, sts)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			logf.FromContext(ctx).Error(err, "reading the StatefulSet for the replica status")
			return
		}
		sts.Status = appsv1.StatefulSetStatus{}
	}
	vc.Status.Replicas = sts.Status.Replicas
	vc.Status.ReadyReplicas = sts.Status.ReadyReplicas
	vc.Status.UpdatedReplicas = sts.Status.UpdatedReplicas
	vc.Status.AvailableReplicas = sts.Status.AvailableReplicas
}
