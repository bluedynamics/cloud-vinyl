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

package webhook

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	vinylv1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
)

const (
	directorTypeShard    = "shard"
	defaultShardWarmup   = 0.1
	defaultShardRampup   = 30 * time.Second
	defaultShardBy       = "HASH"
	defaultShardHealthy  = "CHOSEN"
	defaultDebounce      = 1 * time.Second
	defaultRetryAttempts = int32(3)
	defaultBackoffBase   = 5 * time.Second
	defaultBackoffMax    = 5 * time.Minute
	defaultProxyPort     = int32(8081)
)

// applyShardDefaults fills ShardSpec defaults on a DirectorSpec whose Type is
// "shard". Idempotent: existing non-zero values are preserved.
func applyShardDefaults(ds *vinylv1alpha1.DirectorSpec) {
	if ds.Type != directorTypeShard {
		return
	}
	if ds.Shard == nil {
		ds.Shard = &vinylv1alpha1.ShardSpec{}
	}
	s := ds.Shard
	if s.Warmup == nil {
		v := defaultShardWarmup
		s.Warmup = &v
	}
	if s.Rampup.Duration == 0 {
		s.Rampup = metav1.Duration{Duration: defaultShardRampup}
	}
	if s.By == "" {
		s.By = defaultShardBy
	}
	if s.Healthy == "" {
		s.Healthy = defaultShardHealthy
	}
}

// DefaultVinylCache applies default values to a VinylCache resource.
// It is idempotent: calling it multiple times on the same object produces the same result.
// Existing non-zero values are never overwritten.
//
// Defaults applied:
//   - Invalidation.Purge.Soft = true (soft purge preserves stale-while-revalidate semantics)
//   - Invalidation.Xkey.SoftPurge = true (when Xkey is set)
//   - Director.Type = directorTypeShard (Varnish upstream recommendation for clustering)
//   - Director.Shard.Warmup = 0.1 (pre-populate alternate backend cache)
//   - Director.Shard.Rampup = 30s (throttle traffic to newly healthy backends)
//   - Director.Shard.By = "HASH" (standard shard key)
//   - Director.Shard.Healthy = "CHOSEN" (standard health evaluation)
//   - Backends[*].Director.Type = directorTypeShard (when .Director is non-nil)
//   - Backends[*].Director.Shard.{Warmup, Rampup, By, Healthy} defaults
//     (same values as top-level director)
//   - Cluster.PeerRouting.Type = directorTypeShard
//   - Debounce.Duration = 1s
//   - Retry.MaxAttempts = 3
//   - Retry.BackoffBase = 5s
//   - Retry.BackoffMax = 5m
//   - ProxyProtocol.Port = 8081 (when ProxyProtocol.Enabled is true)
func DefaultVinylCache(vc *vinylv1alpha1.VinylCache) {
	// Ensure Purge spec exists and apply soft purge default.
	if vc.Spec.Invalidation.Purge == nil {
		vc.Spec.Invalidation.Purge = &vinylv1alpha1.PurgeSpec{}
	}
	if !vc.Spec.Invalidation.Purge.Soft {
		vc.Spec.Invalidation.Purge.Soft = true
	}

	// Xkey soft purge default (only when Xkey is explicitly configured).
	if vc.Spec.Invalidation.Xkey != nil && !vc.Spec.Invalidation.Xkey.SoftPurge {
		vc.Spec.Invalidation.Xkey.SoftPurge = true
	}

	// Director type default: shard.
	if vc.Spec.Director.Type == "" {
		vc.Spec.Director.Type = directorTypeShard
	}

	// Shard director defaults (applied whenever type is shard, not only when it was just defaulted).
	applyShardDefaults(&vc.Spec.Director)

	// Cluster peer routing default.
	if vc.Spec.Cluster.PeerRouting.Type == "" {
		vc.Spec.Cluster.PeerRouting.Type = directorTypeShard
	}

	// Per-backend director defaults (mirror top-level director handling).
	// Applied only when a user has explicitly set .Director on a backend;
	// a nil .Director is resolved to a shard director in the generator.
	for i := range vc.Spec.Backends {
		b := &vc.Spec.Backends[i]
		if b.Director == nil {
			continue
		}
		if b.Director.Type == "" {
			b.Director.Type = directorTypeShard
		}
		applyShardDefaults(b.Director)
	}

	// Debounce default: 1s (matches CRD +kubebuilder:default and controller fallback).
	if vc.Spec.Debounce.Duration.Duration == 0 {
		vc.Spec.Debounce.Duration = metav1.Duration{Duration: defaultDebounce}
	}

	// Retry defaults.
	if vc.Spec.Retry.MaxAttempts == 0 {
		vc.Spec.Retry.MaxAttempts = defaultRetryAttempts
	}
	if vc.Spec.Retry.BackoffBase.Duration == 0 {
		vc.Spec.Retry.BackoffBase = metav1.Duration{Duration: defaultBackoffBase}
	}
	if vc.Spec.Retry.BackoffMax.Duration == 0 {
		vc.Spec.Retry.BackoffMax = metav1.Duration{Duration: defaultBackoffMax}
	}

	// ProxyProtocol port default: 8081 (only when enabled).
	if vc.Spec.ProxyProtocol.Enabled && vc.Spec.ProxyProtocol.Port == 0 {
		vc.Spec.ProxyProtocol.Port = defaultProxyPort
	}
}
