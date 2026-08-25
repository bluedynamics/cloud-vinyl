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
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
	"github.com/bluedynamics/cloud-vinyl/internal/generator"
)

// pushVCL pushes VCL to all reachable pods in parallel. The caller passes the
// reachable list, not the Ready one: see collectPeers for why.
// Partial failure updates status per-pod but returns an error only if ALL pods fail.
// VCL compilation errors are not retried.
func (r *VinylCacheReconciler) pushVCL(
	ctx context.Context,
	vc *v1alpha1.VinylCache,
	result *generator.Result,
	peers []generator.PeerBackend,
) ([]generator.PeerBackend, error) {
	log := logf.FromContext(ctx)

	if len(peers) == 0 {
		log.Info("No reachable pods to push VCL to, will requeue")
		// Not an error: the reconciler requeues and tries again once a pod is
		// up. Returning nothing matters, because updateStatus must not then
		// record this VCL as active — doing so convinces the next reconcile
		// that the push already happened and the cache never converges. See #77.
		return nil, nil
	}

	pushStart := time.Now()
	defer func() {
		if r.Metrics != nil {
			r.Metrics.VCLPushDuration.Observe(time.Since(pushStart).Seconds())
		}
	}()

	vclName := vclNameFor(vc, result.Hash)

	type pushResult struct {
		peer generator.PeerBackend
		err  error
	}

	results := make([]pushResult, len(peers))
	var wg sync.WaitGroup

	// One attempt per pod, deliberately. Retrying is the reconcile loop's job:
	// a pod that refused the push still lacks the desired VCL, so the next
	// reconcile finds it again through podsNeedingVCL and pushes again, with
	// the workqueue's rate limiting in front of it. An inner retry loop would
	// repeat that work while holding the single reconcile worker, which is what
	// let a VinylCache outlive its delete timeout with the finalizer attached.
	// See docs/superpowers/specs/2026-08-25-reconcile-starvation-design.md.
	for i, peer := range peers {
		wg.Add(1)
		go func(idx int, p generator.PeerBackend) {
			defer wg.Done()
			err := r.AgentClient.PushVCL(ctx, vc.Namespace, p.IP, vclName, result.VCL)
			switch {
			case err == nil:
				results[idx] = pushResult{peer: p, err: nil}
			case strings.Contains(err.Error(), "Already a VCL named"):
				// Idempotent: the pod already carries this exact VCL.
				log.Info("VCL already loaded, skipping", "pod", p.Name, "vcl", vclName)
				results[idx] = pushResult{peer: p, err: nil}
			default:
				results[idx] = pushResult{peer: p, err: err}
			}
		}(i, peer)
	}

	wg.Wait()

	var pushed []generator.PeerBackend
	failCount := 0
	for _, pr := range results {
		res := "success"
		if pr.err != nil {
			res = "error"
			failCount++
			log.Error(pr.err, "VCL push failed for pod", "pod", pr.peer.Name)
		} else {
			pushed = append(pushed, pr.peer)
		}
		if r.Metrics != nil {
			r.Metrics.VCLPushTotal.WithLabelValues(vc.Name, vc.Namespace, res).Inc()
		}
	}

	if failCount == len(peers) {
		return nil, fmt.Errorf("VCL push failed on all %d pods", len(peers))
	}
	// Only the pods that took the VCL are reported. The caller records these as
	// carrying it, and a pod that refused must not be counted as converged.
	return pushed, nil
}

// defaultPushRetryInterval is used when spec.retry.backoffBase is unset. It
// matches the value the fixed requeue used before backoffBase drove it.
const defaultPushRetryInterval = 30 * time.Second

// vclNameFor is the name a generated VCL is pushed under. It embeds the content
// hash, so comparing this name against what varnishd reports tells the operator
// whether a pod already carries the desired VCL.
func vclNameFor(vc *v1alpha1.VinylCache, hash string) string {
	short := hash
	if len(short) > 8 {
		short = short[:8]
	}
	return fmt.Sprintf("%s-%s-%s", vc.Namespace, vc.Name, short)
}

// collectPeers lists the StatefulSet's pods once and returns two views of them.
//
// reachable is the set of pods the operator may push VCL to. A pod qualifies as
// soon as it is Running and has an IP. Readiness is deliberately NOT required:
// a pod stays NotReady precisely until the operator has pushed it a real VCL,
// so gating the push on readiness deadlocks. No push means never ready, which
// means no push. That deadlock was latent for as long as the agent's vcl.list
// parser reported the wrong VCL name and every pod therefore looked Ready
// (#73). The operator talks to the agent over the pod IP, where readiness has
// no bearing on reachability.
//
// ready is the set of pods that may receive traffic. It feeds the peer backends
// of the generated VCL (the shard director) and the proxy pod map. Sharding to
// a pod that is still on the bootstrap VCL would hand users a 503.
func (r *VinylCacheReconciler) collectPeers(
	ctx context.Context,
	vc *v1alpha1.VinylCache,
) (podObservation, error) {
	var reachable, ready []generator.PeerBackend
	podList := &corev1.PodList{}
	if err := r.List(ctx, podList,
		client.InNamespace(vc.Namespace),
		client.MatchingLabels(map[string]string{labelApp: vc.Name}),
	); err != nil {
		return podObservation{}, fmt.Errorf("listing pods: %w", err)
	}

	for _, pod := range podList.Items {
		// A terminated pod keeps its last PodIP in status, but nothing listens
		// there any more. Pushing to it would burn the whole retry budget.
		if pod.Status.Phase != corev1.PodRunning || pod.Status.PodIP == "" {
			continue
		}
		peer := generator.PeerBackend{
			Name: strings.ReplaceAll(pod.Name, "-", "_"),
			IP:   pod.Status.PodIP,
			Port: varnishPort,
		}
		reachable = append(reachable, peer)
		if isPodReady(&pod) {
			ready = append(ready, peer)
		}
	}
	return podObservation{reachable: reachable, ready: ready}, nil
}

// isPodReady returns true if the pod has a Ready condition with status True.
func isPodReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// debounceRemaining returns the duration the reconciler should wait before
// pushing VCL. Zero means "push now". Uses the reconciler-level debouncer,
// which is primed by EndpointSlice events.
func (r *VinylCacheReconciler) debounceRemaining(vc *v1alpha1.VinylCache) time.Duration {
	if r.debouncer == nil {
		return 0
	}
	window := vc.Spec.Debounce.Duration.Duration
	if window <= 0 {
		// Defensive fallback: kubebuilder default materialises 1s on API objects,
		// but tests/direct struct literals may bypass admission.
		window = 1 * time.Second
	}
	key := types.NamespacedName{Name: vc.Name, Namespace: vc.Namespace}
	return r.debouncer.remaining(key, window)
}

// pushRetryInterval is how long to wait before the reconcile loop retries a
// failed VCL push. spec.retry.backoffBase drives it; the loop itself is the
// retry mechanism, so this is the only knob that still has an effect.
func pushRetryInterval(vc *v1alpha1.VinylCache) time.Duration {
	if vc.Spec.Retry.BackoffBase.Duration > 0 {
		return vc.Spec.Retry.BackoffBase.Duration
	}
	return defaultPushRetryInterval
}
