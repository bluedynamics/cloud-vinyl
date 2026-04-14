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

// pushVCL pushes VCL to all ready peers in parallel.
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
		log.Info("No ready peers to push VCL to, will requeue")
		return nil // Not an error — updateStatus will set partial state, reconciler will requeue
	}

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
		if pr.err != nil {
			failCount++
			log.Error(pr.err, "VCL push failed for pod", "pod", pr.peer.Name)
		}
	}

	if failCount == len(peers) {
		return fmt.Errorf("VCL push failed on all %d pods", len(peers))
	}
	return nil
}

// collectReadyPeers returns a PeerBackend list for all StatefulSet pods with the Ready condition.
func (r *VinylCacheReconciler) collectReadyPeers(ctx context.Context, vc *v1alpha1.VinylCache) ([]generator.PeerBackend, error) {
	podList := &corev1.PodList{}
	if err := r.List(ctx, podList,
		client.InNamespace(vc.Namespace),
		client.MatchingLabels(map[string]string{"app": vc.Name}),
	); err != nil {
		return nil, fmt.Errorf("listing pods: %w", err)
	}

	var peers []generator.PeerBackend
	for _, pod := range podList.Items {
		if !isPodReady(&pod) {
			continue
		}
		if pod.Status.PodIP == "" {
			continue
		}
		peers = append(peers, generator.PeerBackend{
			Name: strings.ReplaceAll(pod.Name, "-", "_"),
			IP:   pod.Status.PodIP,
			Port: varnishPort,
		})
	}
	return peers, nil
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
		window = 1 * time.Second
	}
	key := types.NamespacedName{Name: vc.Name, Namespace: vc.Namespace}
	return r.debouncer.remaining(key, window)
}
