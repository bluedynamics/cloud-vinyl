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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
	"github.com/bluedynamics/cloud-vinyl/internal/generator"
)

// updateStatus refreshes the VinylCache status after a successful reconciliation.
func (r *VinylCacheReconciler) updateStatus(
	ctx context.Context,
	vc *v1alpha1.VinylCache,
	result *generator.Result,
	peers []generator.PeerBackend,
) {
	log := logf.FromContext(ctx)

	now := metav1.NewTime(time.Now())

	vc.Status.ActiveVCL = &v1alpha1.ActiveVCLStatus{
		Name:     fmt.Sprintf("%s-%s", vc.Namespace, vc.Name),
		Hash:     result.Hash,
		PushedAt: &now,
	}

	// Rebuild ClusterPeers from ready peers.
	vc.Status.ClusterPeers = make([]v1alpha1.ClusterPeerStatus, 0, len(peers))
	for _, p := range peers {
		vc.Status.ClusterPeers = append(vc.Status.ClusterPeers, v1alpha1.ClusterPeerStatus{
			PodName:       p.Name,
			Ready:         true,
			ActiveVCLHash: result.Hash,
		})
	}
	vc.Status.ReadyPeers = int32(len(peers))
	vc.Status.TotalPeers = vc.Spec.Replicas

	setCondition(vc, v1alpha1.ConditionVCLSynced, metav1.ConditionTrue, "VCLPushed", "VCL successfully pushed to all ready pods")
	setCondition(vc, v1alpha1.ConditionBackendsAvailable, metav1.ConditionTrue, "BackendsConfigured", "backend configuration applied")
	setCondition(vc, v1alpha1.ConditionProgressing, metav1.ConditionFalse, "ReconcileComplete", "reconciliation complete")

	vc.Status.Phase = calculatePhase(vc)
	setCondition(vc, v1alpha1.ConditionReady, metav1.ConditionTrue, "AllReady", "VinylCache is ready")

	if err := r.Status().Update(ctx, vc); err != nil {
		log.Error(err, "updating VinylCache status")
	}
}

// setErrorStatus sets the VinylCache phase and conditions to reflect an error.
func (r *VinylCacheReconciler) setErrorStatus(ctx context.Context, vc *v1alpha1.VinylCache, reconcileErr error) {
	log := logf.FromContext(ctx)

	vc.Status.Phase = v1alpha1.PhaseError
	vc.Status.Message = reconcileErr.Error()
	setCondition(vc, v1alpha1.ConditionReady, metav1.ConditionFalse, "ReconcileError", reconcileErr.Error())
	setCondition(vc, v1alpha1.ConditionVCLSynced, metav1.ConditionFalse, "VCLPushFailed", reconcileErr.Error())

	if err := r.Status().Update(ctx, vc); err != nil {
		log.Error(err, "updating VinylCache error status")
	}
}

// calculatePhase derives the overall phase from the current conditions.
//
//   - Ready:    VCLSynced=True and BackendsAvailable=True (operator reconciled successfully)
//   - Degraded: VCLSynced=False (VCL push failed)
//   - Error:    set explicitly via setErrorStatus
//   - Pending:  initial state before first successful reconcile
func calculatePhase(vc *v1alpha1.VinylCache) string {
	vclSynced := findConditionStatus(vc, v1alpha1.ConditionVCLSynced)
	backendsAvailable := findConditionStatus(vc, v1alpha1.ConditionBackendsAvailable)

	if vclSynced == metav1.ConditionTrue && backendsAvailable == metav1.ConditionTrue {
		return v1alpha1.PhaseReady
	}
	if vclSynced == metav1.ConditionFalse {
		return v1alpha1.PhaseDegraded
	}
	return v1alpha1.PhasePending
}

// setCondition sets (or updates) a named condition on the VinylCache status.
func setCondition(vc *v1alpha1.VinylCache, condType string, status metav1.ConditionStatus, reason, message string) {
	now := metav1.NewTime(time.Now())
	for i, c := range vc.Status.Conditions {
		if c.Type == condType {
			if c.Status == status && c.Reason == reason {
				// No effective change — preserve LastTransitionTime.
				return
			}
			vc.Status.Conditions[i] = metav1.Condition{
				Type:               condType,
				Status:             status,
				Reason:             reason,
				Message:            message,
				LastTransitionTime: now,
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
func findConditionStatus(vc *v1alpha1.VinylCache, condType string) metav1.ConditionStatus {
	for _, c := range vc.Status.Conditions {
		if c.Type == condType {
			return c.Status
		}
	}
	return ""
}
