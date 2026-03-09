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

const directorTypeShard = "shard"

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
//   - Cluster.PeerRouting.Type = directorTypeShard
//   - Debounce.Duration = 5s
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
	if vc.Spec.Director.Type == directorTypeShard {
		if vc.Spec.Director.Shard == nil {
			vc.Spec.Director.Shard = &vinylv1alpha1.ShardSpec{}
		}
		s := vc.Spec.Director.Shard
		if s.Warmup == nil {
			v := 0.1
			s.Warmup = &v
		}
		if s.Rampup.Duration == 0 {
			s.Rampup = metav1.Duration{Duration: 30 * time.Second}
		}
		if s.By == "" {
			s.By = "HASH"
		}
		if s.Healthy == "" {
			s.Healthy = "CHOSEN"
		}
	}

	// Cluster peer routing default.
	if vc.Spec.Cluster.PeerRouting.Type == "" {
		vc.Spec.Cluster.PeerRouting.Type = directorTypeShard
	}

	// Debounce default: 5s.
	if vc.Spec.Debounce.Duration.Duration == 0 {
		vc.Spec.Debounce.Duration = metav1.Duration{Duration: 5 * time.Second}
	}

	// Retry defaults.
	if vc.Spec.Retry.MaxAttempts == 0 {
		vc.Spec.Retry.MaxAttempts = 3
	}
	if vc.Spec.Retry.BackoffBase.Duration == 0 {
		vc.Spec.Retry.BackoffBase = metav1.Duration{Duration: 5 * time.Second}
	}
	if vc.Spec.Retry.BackoffMax.Duration == 0 {
		vc.Spec.Retry.BackoffMax = metav1.Duration{Duration: 5 * time.Minute}
	}

	// ProxyProtocol port default: 8081 (only when enabled).
	if vc.Spec.ProxyProtocol.Enabled && vc.Spec.ProxyProtocol.Port == 0 {
		vc.Spec.ProxyProtocol.Port = 8081
	}
}
