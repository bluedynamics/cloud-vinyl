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

	v1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
	"github.com/bluedynamics/cloud-vinyl/internal/generator"
)

// podObservation is what a single reconcile learned about a cache's pods.
// Keeping the three views together stops them drifting apart: every one of them
// answers a different question, and PR #77 spent three rounds on code that used
// the wrong one.
type podObservation struct {
	// reachable pods are Running with an IP. These are the push targets;
	// readiness is deliberately not required, because a pod stays NotReady
	// until it has been pushed a VCL.
	reachable []generator.PeerBackend
	// ready pods may take traffic: shard director peers and proxy routing.
	ready []generator.PeerBackend
	// vclNames maps pod IP to the VCL name that pod actually reports.
	// varnishd is the source of truth here, which is what makes a restarted
	// pod heal by itself.
	vclNames map[string]string
}

// observeVCLNames asks every reachable pod's agent which VCL it currently
// carries, keyed by pod IP.
//
// The hash is observed rather than remembered on purpose. A pod whose varnishd
// restarts drops back to the bootstrap VCL but keeps its name, so bookkeeping
// held in status would claim it still has the pushed VCL and it would never be
// pushed again. See docs/superpowers/specs/2026-08-25-vcl-konvergenz-design.md.
//
// A pod whose agent cannot be reached maps to the empty string, which makes it
// a push target. The push is idempotent, so a redundant push costs little,
// while wrongly assuming a pod is up to date stalls convergence outright.
func (r *VinylCacheReconciler) observeVCLNames(
	ctx context.Context,
	vc *v1alpha1.VinylCache,
	pods []generator.PeerBackend,
) map[string]string {
	if len(pods) == 0 {
		return nil
	}
	observed := make(map[string]string, len(pods))
	for _, p := range pods {
		name, err := r.AgentClient.ActiveVCLName(ctx, vc.Namespace, p.IP)
		if err != nil {
			observed[p.IP] = ""
			continue
		}
		observed[p.IP] = name
	}
	return observed
}

// podsNeedingVCL selects the pods that do not already carry the desired VCL.
//
// This replaces the old aggregate push condition, which compared one hash for
// the whole cache and one peer count against another. Neither could see a
// reachable pod that simply had not been pushed yet, which is how replicas that
// joined after the first pod converged ended up without VCL indefinitely.
//
// A pod missing from observed counts as diverging: either its agent could not
// be asked, or it was not there when the hashes were taken. Both mean "push it".
func podsNeedingVCL(
	reachable []generator.PeerBackend,
	observed map[string]string,
	desiredName string,
) []generator.PeerBackend {
	var targets []generator.PeerBackend
	for _, p := range reachable {
		if observed[p.IP] != desiredName {
			targets = append(targets, p)
		}
	}
	return targets
}
