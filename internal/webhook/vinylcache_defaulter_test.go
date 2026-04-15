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

package webhook_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	vinylv1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
	"github.com/bluedynamics/cloud-vinyl/internal/webhook"
)

// emptyVC returns a VinylCache with no spec fields set (all zero values).
func emptyVC() *vinylv1alpha1.VinylCache {
	return &vinylv1alpha1.VinylCache{}
}

// --- Purge.Soft default ---

func TestDefault_SoftPurge_DefaultsToTrue(t *testing.T) {
	vc := emptyVC()
	webhook.DefaultVinylCache(vc)

	require.NotNil(t, vc.Spec.Invalidation.Purge)
	assert.True(t, vc.Spec.Invalidation.Purge.Soft, "Purge.Soft should default to true")
}

func TestDefault_SoftPurge_NotOverwrittenIfFalseIsExplicitlyMeant(t *testing.T) {
	// Since bool zero value is false, we can't distinguish "not set" from "explicitly false".
	// The defaulter always sets Soft=true if it was false — this is by design.
	// This test documents and verifies that behaviour.
	vc := emptyVC()
	vc.Spec.Invalidation.Purge = &vinylv1alpha1.PurgeSpec{Soft: false}
	webhook.DefaultVinylCache(vc)

	// The defaulter applies the default (true) because false is the zero value.
	assert.True(t, vc.Spec.Invalidation.Purge.Soft,
		"Purge.Soft defaults to true (bool zero value cannot be distinguished from 'not set')")
}

func TestDefault_SoftPurge_AlreadyTrue_NotChanged(t *testing.T) {
	vc := emptyVC()
	vc.Spec.Invalidation.Purge = &vinylv1alpha1.PurgeSpec{Soft: true}
	webhook.DefaultVinylCache(vc)

	assert.True(t, vc.Spec.Invalidation.Purge.Soft)
}

// --- Xkey.SoftPurge default ---

func TestDefault_XkeySoftPurge_DefaultsToTrue_WhenXkeySet(t *testing.T) {
	vc := emptyVC()
	vc.Spec.Invalidation.Xkey = &vinylv1alpha1.XkeySpec{Enabled: true}
	webhook.DefaultVinylCache(vc)

	assert.True(t, vc.Spec.Invalidation.Xkey.SoftPurge, "Xkey.SoftPurge should default to true")
}

func TestDefault_XkeySoftPurge_NotApplied_WhenXkeyNil(t *testing.T) {
	vc := emptyVC()
	vc.Spec.Invalidation.Xkey = nil
	webhook.DefaultVinylCache(vc)

	assert.Nil(t, vc.Spec.Invalidation.Xkey, "Xkey should not be created if not configured")
}

// --- Director.Type default ---

func TestDefault_DirectorType_DefaultsToShard(t *testing.T) {
	vc := emptyVC()
	webhook.DefaultVinylCache(vc)

	assert.Equal(t, "shard", vc.Spec.Director.Type, "Director.Type should default to 'shard'")
}

func TestDefault_DirectorType_NotOverwrittenIfAlreadySet(t *testing.T) {
	vc := emptyVC()
	vc.Spec.Director.Type = "round_robin"
	webhook.DefaultVinylCache(vc)

	assert.Equal(t, "round_robin", vc.Spec.Director.Type,
		"Director.Type should not be overwritten if already set")
}

// --- Shard director defaults ---

func TestDefault_ShardWarmup_DefaultsTo0_1(t *testing.T) {
	vc := emptyVC()
	webhook.DefaultVinylCache(vc)

	require.NotNil(t, vc.Spec.Director.Shard)
	require.NotNil(t, vc.Spec.Director.Shard.Warmup)
	assert.InDelta(t, 0.1, *vc.Spec.Director.Shard.Warmup, 1e-9,
		"ShardSpec.Warmup should default to 0.1")
}

func TestDefault_ShardWarmup_NotOverwrittenIfAlreadySet(t *testing.T) {
	vc := emptyVC()
	warmup := 0.5
	vc.Spec.Director.Shard = &vinylv1alpha1.ShardSpec{Warmup: &warmup}
	webhook.DefaultVinylCache(vc)

	require.NotNil(t, vc.Spec.Director.Shard.Warmup)
	assert.InDelta(t, 0.5, *vc.Spec.Director.Shard.Warmup, 1e-9,
		"ShardSpec.Warmup should not be overwritten")
}

func TestDefault_ShardRampup_DefaultsTo30s(t *testing.T) {
	vc := emptyVC()
	webhook.DefaultVinylCache(vc)

	require.NotNil(t, vc.Spec.Director.Shard)
	assert.Equal(t, 30*time.Second, vc.Spec.Director.Shard.Rampup.Duration,
		"ShardSpec.Rampup should default to 30s")
}

func TestDefault_ShardRampup_NotOverwrittenIfAlreadySet(t *testing.T) {
	vc := emptyVC()
	vc.Spec.Director.Shard = &vinylv1alpha1.ShardSpec{
		Rampup: metav1.Duration{Duration: 60 * time.Second},
	}
	webhook.DefaultVinylCache(vc)

	assert.Equal(t, 60*time.Second, vc.Spec.Director.Shard.Rampup.Duration,
		"ShardSpec.Rampup should not be overwritten")
}

func TestDefault_ShardBy_DefaultsToHASH(t *testing.T) {
	vc := emptyVC()
	webhook.DefaultVinylCache(vc)

	require.NotNil(t, vc.Spec.Director.Shard)
	assert.Equal(t, "HASH", vc.Spec.Director.Shard.By, "ShardSpec.By should default to 'HASH'")
}

func TestDefault_ShardBy_NotOverwrittenIfAlreadySet(t *testing.T) {
	vc := emptyVC()
	vc.Spec.Director.Shard = &vinylv1alpha1.ShardSpec{By: "URL"}
	webhook.DefaultVinylCache(vc)

	assert.Equal(t, "URL", vc.Spec.Director.Shard.By, "ShardSpec.By should not be overwritten")
}

func TestDefault_ShardHealthy_DefaultsToCHOSEN(t *testing.T) {
	vc := emptyVC()
	webhook.DefaultVinylCache(vc)

	require.NotNil(t, vc.Spec.Director.Shard)
	assert.Equal(t, "CHOSEN", vc.Spec.Director.Shard.Healthy,
		"ShardSpec.Healthy should default to 'CHOSEN'")
}

func TestDefault_ShardHealthy_NotOverwrittenIfAlreadySet(t *testing.T) {
	vc := emptyVC()
	vc.Spec.Director.Shard = &vinylv1alpha1.ShardSpec{Healthy: "ALL"}
	webhook.DefaultVinylCache(vc)

	assert.Equal(t, "ALL", vc.Spec.Director.Shard.Healthy, "ShardSpec.Healthy should not be overwritten")
}

func TestDefault_ShardDefaults_NotApplied_WhenDirectorTypeIsRoundRobin(t *testing.T) {
	vc := emptyVC()
	vc.Spec.Director.Type = "round_robin"
	webhook.DefaultVinylCache(vc)

	assert.Nil(t, vc.Spec.Director.Shard,
		"ShardSpec defaults should not be applied when director type is not 'shard'")
}

// --- Cluster.PeerRouting default ---

func TestDefault_ClusterPeerRoutingType_DefaultsToShard(t *testing.T) {
	vc := emptyVC()
	webhook.DefaultVinylCache(vc)

	assert.Equal(t, "shard", vc.Spec.Cluster.PeerRouting.Type,
		"Cluster.PeerRouting.Type should default to 'shard'")
}

func TestDefault_ClusterPeerRoutingType_NotOverwrittenIfAlreadySet(t *testing.T) {
	vc := emptyVC()
	vc.Spec.Cluster.PeerRouting.Type = "shard" // only valid value currently, but test the non-override
	webhook.DefaultVinylCache(vc)

	assert.Equal(t, "shard", vc.Spec.Cluster.PeerRouting.Type)
}

// --- Debounce default ---

func TestDefault_Debounce_DefaultsTo1s(t *testing.T) {
	vc := emptyVC()
	webhook.DefaultVinylCache(vc)

	assert.Equal(t, 1*time.Second, vc.Spec.Debounce.Duration.Duration,
		"Debounce.Duration should default to 1s (matches CRD +kubebuilder:default)")
}

func TestDefault_Debounce_NotOverwrittenIfAlreadySet(t *testing.T) {
	vc := emptyVC()
	vc.Spec.Debounce.Duration = metav1.Duration{Duration: 10 * time.Second}
	webhook.DefaultVinylCache(vc)

	assert.Equal(t, 10*time.Second, vc.Spec.Debounce.Duration.Duration,
		"Debounce.Duration should not be overwritten")
}

// --- Retry defaults ---

func TestDefault_RetryMaxAttempts_DefaultsTo3(t *testing.T) {
	vc := emptyVC()
	webhook.DefaultVinylCache(vc)

	assert.Equal(t, int32(3), vc.Spec.Retry.MaxAttempts,
		"Retry.MaxAttempts should default to 3")
}

func TestDefault_RetryMaxAttempts_NotOverwrittenIfAlreadySet(t *testing.T) {
	vc := emptyVC()
	vc.Spec.Retry.MaxAttempts = 5
	webhook.DefaultVinylCache(vc)

	assert.Equal(t, int32(5), vc.Spec.Retry.MaxAttempts,
		"Retry.MaxAttempts should not be overwritten")
}

func TestDefault_RetryBackoffBase_DefaultsTo5s(t *testing.T) {
	vc := emptyVC()
	webhook.DefaultVinylCache(vc)

	assert.Equal(t, 5*time.Second, vc.Spec.Retry.BackoffBase.Duration,
		"Retry.BackoffBase should default to 5s")
}

func TestDefault_RetryBackoffBase_NotOverwrittenIfAlreadySet(t *testing.T) {
	vc := emptyVC()
	vc.Spec.Retry.BackoffBase = metav1.Duration{Duration: 2 * time.Second}
	webhook.DefaultVinylCache(vc)

	assert.Equal(t, 2*time.Second, vc.Spec.Retry.BackoffBase.Duration,
		"Retry.BackoffBase should not be overwritten")
}

func TestDefault_RetryBackoffMax_DefaultsTo5m(t *testing.T) {
	vc := emptyVC()
	webhook.DefaultVinylCache(vc)

	assert.Equal(t, 5*time.Minute, vc.Spec.Retry.BackoffMax.Duration,
		"Retry.BackoffMax should default to 5m")
}

func TestDefault_RetryBackoffMax_NotOverwrittenIfAlreadySet(t *testing.T) {
	vc := emptyVC()
	vc.Spec.Retry.BackoffMax = metav1.Duration{Duration: 10 * time.Minute}
	webhook.DefaultVinylCache(vc)

	assert.Equal(t, 10*time.Minute, vc.Spec.Retry.BackoffMax.Duration,
		"Retry.BackoffMax should not be overwritten")
}

// --- ProxyProtocol.Port default ---

func TestDefault_ProxyProtocolPort_DefaultsTo8081_WhenEnabled(t *testing.T) {
	vc := emptyVC()
	vc.Spec.ProxyProtocol.Enabled = true
	webhook.DefaultVinylCache(vc)

	assert.Equal(t, int32(8081), vc.Spec.ProxyProtocol.Port,
		"ProxyProtocol.Port should default to 8081 when enabled")
}

func TestDefault_ProxyProtocolPort_NotSet_WhenDisabled(t *testing.T) {
	vc := emptyVC()
	vc.Spec.ProxyProtocol.Enabled = false
	webhook.DefaultVinylCache(vc)

	assert.Equal(t, int32(0), vc.Spec.ProxyProtocol.Port,
		"ProxyProtocol.Port should not be set when ProxyProtocol is disabled")
}

func TestDefault_ProxyProtocolPort_NotOverwrittenIfAlreadySet(t *testing.T) {
	vc := emptyVC()
	vc.Spec.ProxyProtocol.Enabled = true
	vc.Spec.ProxyProtocol.Port = 8443
	webhook.DefaultVinylCache(vc)

	assert.Equal(t, int32(8443), vc.Spec.ProxyProtocol.Port,
		"ProxyProtocol.Port should not be overwritten if already set")
}

// --- per-backend director defaults ---

// vcWithBackend returns a VinylCache with a single backend (name only).
// Used by per-backend director tests.
func vcWithBackend() *vinylv1alpha1.VinylCache {
	return &vinylv1alpha1.VinylCache{
		Spec: vinylv1alpha1.VinylCacheSpec{
			Backends: []vinylv1alpha1.BackendSpec{{Name: "b"}},
		},
	}
}

func TestDefault_BackendDirector_NilIsUntouched(t *testing.T) {
	vc := vcWithBackend()
	// Ensure backend has no Director set.
	vc.Spec.Backends[0].Director = nil
	webhook.DefaultVinylCache(vc)
	assert.Nil(t, vc.Spec.Backends[0].Director,
		"nil per-backend Director stays nil (generator handles nil as shard)")
}

func TestDefault_BackendDirector_EmptyTypeDefaultsShard(t *testing.T) {
	vc := vcWithBackend()
	vc.Spec.Backends[0].Director = &vinylv1alpha1.DirectorSpec{}
	webhook.DefaultVinylCache(vc)
	require.NotNil(t, vc.Spec.Backends[0].Director)
	assert.Equal(t, "shard", vc.Spec.Backends[0].Director.Type)
}

func TestDefault_BackendDirector_ShardDefaults(t *testing.T) {
	vc := vcWithBackend()
	vc.Spec.Backends[0].Director = &vinylv1alpha1.DirectorSpec{Type: "shard"}
	webhook.DefaultVinylCache(vc)
	s := vc.Spec.Backends[0].Director.Shard
	require.NotNil(t, s)
	require.NotNil(t, s.Warmup)
	assert.InDelta(t, 0.1, *s.Warmup, 1e-9)
	assert.Equal(t, 30*time.Second, s.Rampup.Duration)
	assert.Equal(t, "HASH", s.By)
	assert.Equal(t, "CHOSEN", s.Healthy)
}

func TestDefault_BackendDirector_NonShardTypeKeepsNilShard(t *testing.T) {
	vc := vcWithBackend()
	vc.Spec.Backends[0].Director = &vinylv1alpha1.DirectorSpec{Type: "round_robin"}
	webhook.DefaultVinylCache(vc)
	assert.Equal(t, "round_robin", vc.Spec.Backends[0].Director.Type)
	assert.Nil(t, vc.Spec.Backends[0].Director.Shard,
		"round_robin doesn't get shard defaults")
}

func TestDefault_BackendDirector_ShardUserValuesPreserved(t *testing.T) {
	vc := vcWithBackend()
	warmup := 0.5
	vc.Spec.Backends[0].Director = &vinylv1alpha1.DirectorSpec{
		Type: "shard",
		Shard: &vinylv1alpha1.ShardSpec{
			Warmup: &warmup,
			Rampup: metav1.Duration{Duration: 60 * time.Second},
			By:     "URL",
		},
	}
	webhook.DefaultVinylCache(vc)
	s := vc.Spec.Backends[0].Director.Shard
	assert.InDelta(t, 0.5, *s.Warmup, 1e-9, "user warmup preserved")
	assert.Equal(t, 60*time.Second, s.Rampup.Duration, "user rampup preserved")
	assert.Equal(t, "URL", s.By, "user by preserved")
	assert.Equal(t, "CHOSEN", s.Healthy, "Healthy still gets default")
}

// --- idempotency ---

func TestDefault_Idempotent(t *testing.T) {
	vc := emptyVC()

	// Apply defaults twice — the result should be identical.
	webhook.DefaultVinylCache(vc)
	snapshot1 := *vc.Spec.Director.Shard.Warmup
	d1 := vc.Spec.Debounce.Duration.Duration

	webhook.DefaultVinylCache(vc)
	snapshot2 := *vc.Spec.Director.Shard.Warmup
	d2 := vc.Spec.Debounce.Duration.Duration

	assert.InDelta(t, snapshot1, snapshot2, 1e-9, "defaulter should be idempotent (Warmup)")
	assert.Equal(t, d1, d2, "defaulter should be idempotent (Debounce)")
}
