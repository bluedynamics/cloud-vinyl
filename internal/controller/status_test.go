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
