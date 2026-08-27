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

import v1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"

// legacyShardByLogFields returns the log message and structured fields to
// emit when reconciling a VinylCache whose director.shard.by will be
// silently coerced by the generator (buildTemplateData in
// internal/generator/generator.go) because it is not the one value ("URL")
// the CRD enum allows going forward (#92). Returns ok=false when nothing
// needs to be logged.
//
// The condition here MUST mirror the generator's coercion scope exactly —
// "anything other than an explicit URL", not just the legacy "HASH"
// default — or values the coercion silently rewrites go unlogged, which
// defeats the point of logging at all. A VinylCache can carry an
// unexpected By value for reasons beyond the HASH default: a hand-edited
// object, a future enum member reverted, or anything else that reached
// storage before/around a schema change. All of those must be surfaced the
// same way.
func legacyShardByLogFields(vc *v1alpha1.VinylCache) (msg string, kvs []any, ok bool) {
	s := vc.Spec.Director.Shard
	if s == nil || s.By == "" || s.By == "URL" {
		return "", nil, false
	}
	return "spec.director.shard.by is not a supported value and is being treated as URL (#92); " +
			"update the object to by=URL to make the stored spec accurate",
		[]any{"by", s.By}, true
}
