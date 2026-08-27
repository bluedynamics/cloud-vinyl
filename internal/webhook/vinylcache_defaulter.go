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
	directorTypeShard   = "shard"
	defaultShardWarmup  = 0.1
	defaultShardRampup  = 30 * time.Second
	defaultShardBy      = "URL"
	defaultShardHealthy = "CHOSEN"
	defaultDebounce     = 1 * time.Second
	// defaultBackoffBase is how long the reconcile loop waits before retrying a
	// failed VCL push. It is 30s rather than the 5s this constant held while it
	// seeded an inner retry loop, so that removing that loop does not silently
	// make failure retries six times more frequent.
	defaultBackoffBase = 30 * time.Second
	defaultBackoffMax  = 5 * time.Minute
	defaultProxyPort   = int32(8081)
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
// Existing non-zero values are preserved, but only for fields whose zero value
// genuinely means "unset" (numbers, strings, durations). Plain bool fields
// cannot make that distinction: false is indistinguishable from unset, so a
// default that "fills in false" would silently overwrite an explicit false.
// Such fields (e.g. Invalidation.Purge.Soft) are therefore *bool and are
// defaulted at the CRD level via +kubebuilder:default, never in this function.
//
// Defaults applied:
//   - Director.Type = directorTypeShard (Varnish upstream recommendation for clustering)
//   - Director.Shard.Warmup = 0.1 (pre-populate alternate backend cache)
//   - Director.Shard.Rampup = 30s (throttle traffic to newly healthy backends)
//   - Director.Shard.By = "URL" (the only shard key that works on the
//     request-time .backend() call in vcl_recv; see #92)
//   - Director.Shard.Healthy = "CHOSEN" (standard health evaluation)
//   - Backends[*].Director.Type = directorTypeShard (when .Director is non-nil)
//   - Backends[*].Director.Shard.{Warmup, Rampup, By, Healthy} defaults
//     (same values as top-level director)
//   - Cluster.PeerRouting.Type = directorTypeShard
//   - Debounce.Duration = 1s
//   - Retry.BackoffBase = 30s
//   - Retry.BackoffMax = 5m
//   - ProxyProtocol.Port = 8081 (when ProxyProtocol.Enabled is true)
func DefaultVinylCache(vc *vinylv1alpha1.VinylCache) {
	// Ensure Purge spec exists. Soft is intentionally left untouched here:
	// its true default is applied at the CRD level (+kubebuilder:default on
	// PurgeSpec.Soft), which can tell "unset" apart from an explicit false.
	if vc.Spec.Invalidation.Purge == nil {
		vc.Spec.Invalidation.Purge = &vinylv1alpha1.PurgeSpec{}
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
	//
	// MaxAttempts is deliberately NOT defaulted any more. It no longer has any
	// effect, and filling it in would show users a value that does nothing.
	// See docs/superpowers/specs/2026-08-25-reconcile-starvation-design.md.
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
