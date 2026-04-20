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

// Package webhook contains the business logic for the VinylCache admission webhooks.
// The kubebuilder scaffolding in internal/webhook/v1alpha1/ delegates to these functions,
// keeping the actual validation and defaulting logic independently testable.
package webhook

import (
	"fmt"
	"net"
	"strings"
	"unicode"

	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	vinylv1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
)

// forbiddenVarnishParams lists varnishd -p parameters that are blocked for security reasons.
// vcc_allow_inline_c: allows C code in VCL → Remote Code Execution risk.
// cc_command: arbitrary compiler invocation.
var forbiddenVarnishParams = map[string]bool{
	"vcc_allow_inline_c": true,
	"cc_command":         true,
}

// forbiddenStorageTypes lists storage backend types that are not permitted.
// persistent: effectively deprecated (deprecated_persistent), fundamentally broken
//
//	across restarts, bans are not persisted.
//
// umem: Solaris/illumos only, not available on Linux.
// default: identical to malloc on Linux but confusing.
var forbiddenStorageTypes = map[string]bool{
	"persistent": true,
	"umem":       true,
	"default":    true,
}

// reservedVolumeNames collide with operator-managed volumes on every pod.
// Users cannot reuse these names in spec.pod.volumes or
// spec.volumeClaimTemplates.
var reservedVolumeNames = map[string]bool{
	"agent-token":     true,
	"varnish-secret":  true,
	"varnish-workdir": true,
	"varnish-tmp":     true,
	"bootstrap-vcl":   true,
}

// reservedMountPaths are owned by the operator. spec.pod.volumeMounts
// must not mount into these, and spec.storage[].path must not place
// files under them.
var reservedMountPaths = []string{
	"/run/vinyl",
	"/etc/varnish/secret",
	"/etc/varnish/default.vcl",
	"/var/lib/varnish",
	"/tmp",
}

// pathIsReserved reports whether p equals a reserved mount path or sits
// under one (with a trailing slash boundary to avoid false positives
// like "/var/lib/varnish-cache" hitting "/var/lib/varnish").
func pathIsReserved(p string) bool {
	for _, r := range reservedMountPaths {
		if p == r {
			return true
		}
		if strings.HasPrefix(p, r+"/") {
			return true
		}
	}
	return false
}

// pathConflictsWithReserved reports whether user path p either sits under
// a reserved path (pathIsReserved) OR is an ancestor of one. Ancestor
// mounts must be rejected for mountPath because kubelet would otherwise
// let the user-declared mount shadow the operator's narrower subPath
// mount (e.g. mounting "/etc/varnish" shadows the agent secret at
// "/etc/varnish/secret"), giving a user with VinylCache edit rights a
// new path to operator-owned data.
func pathConflictsWithReserved(p string) bool {
	if pathIsReserved(p) {
		return true
	}
	// "/" is the ancestor of every reserved path; special-case it so the
	// HasPrefix check below doesn't have to deal with "/"+"/"="//" quirks.
	if p == "/" {
		return true
	}
	for _, r := range reservedMountPaths {
		if strings.HasPrefix(r, p+"/") {
			return true
		}
	}
	return false
}

// ValidateVinylCache performs semantic validation of a VinylCache resource that is not
// expressible as CRD schema constraints. It is called by both ValidateCreate and ValidateUpdate.
//
// Checks performed:
//   - varnishParameters blocklist (security-sensitive parameters)
//   - storage type blocklist
//   - backend name VCL identifier conformance
//   - allowedSources CIDR syntax for purge, BAN, and xkey invalidation
func ValidateVinylCache(vc *vinylv1alpha1.VinylCache) (admission.Warnings, error) {
	var errs []string

	// Validate varnishParameters blocklist.
	for k := range vc.Spec.VarnishParams {
		if forbiddenVarnishParams[k] {
			errs = append(errs, fmt.Sprintf("varnishParameters key %q is not allowed", k))
		}
	}

	// Validate storage type blocklist.
	for _, s := range vc.Spec.Storage {
		if forbiddenStorageTypes[s.Type] {
			errs = append(errs, fmt.Sprintf("storage type %q is not allowed (use malloc or file)", s.Type))
		}
	}

	// Validate backend names are VCL-conformant identifiers.
	for _, b := range vc.Spec.Backends {
		if !isValidVCLIdentifier(b.Name) {
			errs = append(errs, fmt.Sprintf(
				"backend name %q must start with a letter and contain only letters, digits, underscores",
				b.Name,
			))
		}
	}

	// Validate CIDR syntax in all invalidation allowedSources fields.
	for _, sources := range [][]string{
		purgeAllowedSources(vc),
		banAllowedSources(vc),
		xkeyAllowedSources(vc),
	} {
		for _, cidr := range sources {
			if _, _, err := net.ParseCIDR(cidr); err != nil {
				errs = append(errs, fmt.Sprintf("invalid CIDR %q: %v", cidr, err))
			}
		}
	}

	// Collect all user-declared volume names (pod.volumes + volumeClaimTemplates).
	declared := make(map[string]bool, len(vc.Spec.Pod.Volumes)+len(vc.Spec.VolumeClaimTemplates))

	for _, v := range vc.Spec.Pod.Volumes {
		if reservedVolumeNames[v.Name] {
			errs = append(errs, fmt.Sprintf(
				"spec.pod.volumes[%q]: name is reserved by the operator", v.Name))
			continue
		}
		if declared[v.Name] {
			errs = append(errs, fmt.Sprintf(
				"spec.pod.volumes[%q]: duplicate volume name", v.Name))
			continue
		}
		declared[v.Name] = true
	}

	for _, c := range vc.Spec.VolumeClaimTemplates {
		if reservedVolumeNames[c.Name] {
			errs = append(errs, fmt.Sprintf(
				"spec.volumeClaimTemplates[%q]: name is reserved by the operator", c.Name))
			continue
		}
		if declared[c.Name] {
			errs = append(errs, fmt.Sprintf(
				"spec.volumeClaimTemplates[%q]: duplicate — name already used in spec.pod.volumes or another claim template", c.Name))
			continue
		}
		declared[c.Name] = true
	}

	// Validate mount paths + that each mount resolves to a declared volume.
	for _, m := range vc.Spec.Pod.VolumeMounts {
		if pathConflictsWithReserved(m.MountPath) {
			errs = append(errs, fmt.Sprintf(
				"spec.pod.volumeMounts[%q]: mountPath %q conflicts with a reserved operator mount",
				m.Name, m.MountPath))
		}
		if !declared[m.Name] && !reservedVolumeNames[m.Name] {
			errs = append(errs, fmt.Sprintf(
				"spec.pod.volumeMounts[%q]: volume is not declared in spec.pod.volumes or spec.volumeClaimTemplates",
				m.Name))
		}
	}

	// Validate spec.storage[].path does not write into a reserved mount.
	for _, s := range vc.Spec.Storage {
		if s.Type == "file" && pathIsReserved(s.Path) {
			errs = append(errs, fmt.Sprintf(
				"spec.storage[%q].path %q is reserved by the operator; mount your own volume and place the cache file there",
				s.Name, s.Path))
		}
	}

	if len(errs) > 0 {
		return nil, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil, nil
}

func purgeAllowedSources(vc *vinylv1alpha1.VinylCache) []string {
	if vc.Spec.Invalidation.Purge == nil {
		return nil
	}
	return vc.Spec.Invalidation.Purge.AllowedSources
}

func banAllowedSources(vc *vinylv1alpha1.VinylCache) []string {
	if vc.Spec.Invalidation.BAN == nil {
		return nil
	}
	return vc.Spec.Invalidation.BAN.AllowedSources
}

func xkeyAllowedSources(vc *vinylv1alpha1.VinylCache) []string {
	if vc.Spec.Invalidation.Xkey == nil {
		return nil
	}
	return vc.Spec.Invalidation.Xkey.AllowedSources
}

// isValidVCLIdentifier returns true if name is a valid VCL identifier:
// starts with a letter, followed by letters, digits, or underscores.
func isValidVCLIdentifier(name string) bool {
	if len(name) == 0 {
		return false
	}
	runes := []rune(name)
	if !unicode.IsLetter(runes[0]) {
		return false
	}
	for _, r := range runes[1:] {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
			return false
		}
	}
	return true
}
