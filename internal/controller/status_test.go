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
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
)

func TestCalculatePhase_NoPodsReady_ReturnsPending(t *testing.T) {
	vc := &v1alpha1.VinylCache{}
	vc.Status.ReadyPeers = 0

	phase := calculatePhase(vc)
	if phase != v1alpha1.PhasePending {
		t.Errorf("expected %q, got %q", v1alpha1.PhasePending, phase)
	}
}

func TestCalculatePhase_AllReady_ReturnsReady(t *testing.T) {
	vc := &v1alpha1.VinylCache{}
	vc.Status.ReadyPeers = 3
	vc.Status.Conditions = []metav1.Condition{
		{Type: v1alpha1.ConditionVCLSynced, Status: metav1.ConditionTrue},
		{Type: v1alpha1.ConditionBackendsAvailable, Status: metav1.ConditionTrue},
	}

	phase := calculatePhase(vc)
	if phase != v1alpha1.PhaseReady {
		t.Errorf("expected %q, got %q", v1alpha1.PhaseReady, phase)
	}
}

func TestCalculatePhase_VCLNotSynced_ReturnsDegraded(t *testing.T) {
	vc := &v1alpha1.VinylCache{}
	vc.Status.ReadyPeers = 2
	vc.Status.Conditions = []metav1.Condition{
		{Type: v1alpha1.ConditionVCLSynced, Status: metav1.ConditionFalse},
		{Type: v1alpha1.ConditionBackendsAvailable, Status: metav1.ConditionTrue},
	}

	phase := calculatePhase(vc)
	if phase != v1alpha1.PhaseDegraded {
		t.Errorf("expected %q, got %q", v1alpha1.PhaseDegraded, phase)
	}
}

func failedCondition(msg string, ltt metav1.Time) []metav1.Condition {
	return []metav1.Condition{{
		Type:               v1alpha1.ConditionVCLSynced,
		Status:             metav1.ConditionFalse,
		Reason:             "VCLPushFailed",
		Message:            msg,
		LastTransitionTime: ltt,
		ObservedGeneration: 1,
	}}
}

// A repeated failure with a changed message must surface the new message.
// Reporting "all 1 pods" while status.message already says "all 2 pods" sent
// the reporter of #58 looking in the wrong place.
func TestSetCondition_RefreshesMessageAndKeepsTransitionTime(t *testing.T) {
	old := metav1.NewTime(time.Now().Add(-time.Hour))
	vc := &v1alpha1.VinylCache{}
	vc.Generation = 2
	vc.Status.Conditions = failedCondition("VCL push failed on all 1 pods", old)

	setCondition(vc, v1alpha1.ConditionVCLSynced, metav1.ConditionFalse, "VCLPushFailed",
		"VCL push failed on all 2 pods")

	got := vc.Status.Conditions[0]
	if got.Message != "VCL push failed on all 2 pods" {
		t.Errorf("message not refreshed: got %q", got.Message)
	}
	if !got.LastTransitionTime.Equal(&old) {
		t.Errorf("LastTransitionTime must not move while the status stays False: got %v, want %v",
			got.LastTransitionTime, old)
	}
	if got.ObservedGeneration != 2 {
		t.Errorf("ObservedGeneration not refreshed: got %d, want 2", got.ObservedGeneration)
	}
}

func TestSetCondition_StatusChangeMovesTransitionTime(t *testing.T) {
	old := metav1.NewTime(time.Now().Add(-time.Hour))
	vc := &v1alpha1.VinylCache{}
	vc.Status.Conditions = failedCondition("VCL push failed on all 2 pods", old)

	setCondition(vc, v1alpha1.ConditionVCLSynced, metav1.ConditionTrue, "VCLPushed", "VCL pushed to 2/2 pods")

	got := vc.Status.Conditions[0]
	if got.LastTransitionTime.Equal(&old) {
		t.Error("LastTransitionTime must move when the status flips")
	}
	if got.Status != metav1.ConditionTrue || got.Reason != "VCLPushed" {
		t.Errorf("condition not updated: %+v", got)
	}
}
