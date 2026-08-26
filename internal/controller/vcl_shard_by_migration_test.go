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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
)

func vcWithShardBy(by string) *v1alpha1.VinylCache {
	vc := &v1alpha1.VinylCache{}
	if by != "" {
		vc.Spec.Director.Shard = &v1alpha1.ShardSpec{By: by}
	}
	return vc
}

// TestLegacyShardByLogFields_ArbitraryUnexpectedValue_LogsToo is the case the
// review flagged: the original condition only fired on the literal "HASH",
// a blacklist, while the generator's coercion (buildTemplateData in
// internal/generator/generator.go) treats anything that isn't exactly "URL"
// as needing coercion — a whitelist. A value that is neither "URL" nor the
// legacy "HASH" (a hand-edited object, a future enum member reverted, ...)
// must still be logged: the coercion is silent about it otherwise, which is
// exactly the property this logging exists to prevent.
func TestLegacyShardByLogFields_ArbitraryUnexpectedValue_LogsToo(t *testing.T) {
	vc := vcWithShardBy("RANDOM")
	msg, kvs, ok := legacyShardByLogFields(vc)
	require.True(t, ok, "a By value that is neither URL nor HASH must still be logged")
	assert.Contains(t, msg, "spec.director.shard.by")
	assert.Contains(t, msg, "#92")
	assert.Equal(t, []any{"by", "RANDOM"}, kvs)
}

func TestLegacyShardByLogFields_HASH_Logs(t *testing.T) {
	// Regression: the legacy default value must still be caught.
	vc := vcWithShardBy("HASH")
	_, kvs, ok := legacyShardByLogFields(vc)
	require.True(t, ok)
	assert.Equal(t, []any{"by", "HASH"}, kvs)
}

func TestLegacyShardByLogFields_URL_NoLog(t *testing.T) {
	vc := vcWithShardBy("URL")
	_, _, ok := legacyShardByLogFields(vc)
	assert.False(t, ok, "the only value the enum allows must never be logged as unexpected")
}

func TestLegacyShardByLogFields_Empty_NoLog(t *testing.T) {
	vc := vcWithShardBy("")
	_, _, ok := legacyShardByLogFields(vc)
	assert.False(t, ok, "unset By is not unexpected; the generator defaults it to URL on its own")
}

func TestLegacyShardByLogFields_NilShard_NoLog(t *testing.T) {
	vc := &v1alpha1.VinylCache{}
	_, _, ok := legacyShardByLogFields(vc)
	assert.False(t, ok)
}
