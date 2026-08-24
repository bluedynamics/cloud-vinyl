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
) error {
	log := logf.FromContext(ctx)

	if len(peers) == 0 {
		log.Info("No reachable pods to push VCL to, will requeue")
		return nil // Not an error — updateStatus will set partial state, reconciler will requeue
	}

	pushStart := time.Now()
	defer func() {
		if r.Metrics != nil {
			r.Metrics.VCLPushDuration.Observe(time.Since(pushStart).Seconds())
		}
	}()

	maxAttempts := int32(3)
	if vc.Spec.Retry.MaxAttempts > 0 {
		maxAttempts = vc.Spec.Retry.MaxAttempts
	}
	backoffBase := 5 * time.Second
	if vc.Spec.Retry.BackoffBase.Duration > 0 {
		backoffBase = vc.Spec.Retry.BackoffBase.Duration
	}

	vclName := fmt.Sprintf("%s-%s-%s", vc.Namespace, vc.Name, result.Hash[:8])

	type pushResult struct {
		peer generator.PeerBackend
		err  error
	}

	results := make([]pushResult, len(peers))
	var wg sync.WaitGroup

	for i, peer := range peers {
		wg.Add(1)
		go func(idx int, p generator.PeerBackend) {
			defer wg.Done()
			var lastErr error
			for attempt := int32(0); attempt < maxAttempts; attempt++ {
				if attempt > 0 {
					backoff := time.Duration(attempt) * backoffBase
					select {
					case <-ctx.Done():
						results[idx] = pushResult{peer: p, err: ctx.Err()}
						return
					case <-time.After(backoff):
					}
				}
				err := r.AgentClient.PushVCL(ctx, vc.Namespace, p.IP, vclName, result.VCL)
				if err == nil {
					results[idx] = pushResult{peer: p, err: nil}
					return
				}
				// VCL with this name already loaded — treat as success (idempotent).
				if strings.Contains(err.Error(), "Already a VCL named") {
					log.Info("VCL already loaded, skipping", "pod", p.Name, "vcl", vclName)
					results[idx] = pushResult{peer: p, err: nil}
					return
				}
				lastErr = err
				// Do not retry VCL compilation errors.
				if strings.Contains(err.Error(), "VCL compilation failed") {
					log.Error(err, "VCL compilation error — not retrying", "pod", p.Name)
					break
				}
				log.Error(err, "VCL push failed, retrying", "pod", p.Name, "attempt", attempt+1)
			}
			results[idx] = pushResult{peer: p, err: lastErr}
		}(i, peer)
	}

	wg.Wait()

	failCount := 0
	for _, pr := range results {
		res := "success"
		if pr.err != nil {
			res = "error"
			failCount++
			log.Error(pr.err, "VCL push failed for pod", "pod", pr.peer.Name)
		}
		if r.Metrics != nil {
			r.Metrics.VCLPushTotal.WithLabelValues(vc.Name, vc.Namespace, res).Inc()
		}
	}

	if failCount == len(peers) {
		return fmt.Errorf("VCL push failed on all %d pods", len(peers))
	}
	return nil
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
) (reachable, ready []generator.PeerBackend, err error) {
	podList := &corev1.PodList{}
	if err := r.List(ctx, podList,
		client.InNamespace(vc.Namespace),
		client.MatchingLabels(map[string]string{"app": vc.Name}),
	); err != nil {
		return nil, nil, fmt.Errorf("listing pods: %w", err)
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
	return reachable, ready, nil
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
