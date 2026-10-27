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
// The kubebuilder scaffolding in internal/webhook/v1/ delegates to these functions,
// keeping the actual validation and defaulting logic independently testable.
package webhook

import (
	"fmt"
	"net"
	"strings"
	"unicode"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	vinylv1 "github.com/bluedynamics/cloud-vinyl/api/v1"
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

// reservedStorageNames are storage names varnishd defines itself. Varnish 9.1
// added a built-in "synth" storage, and a user storage of that name makes the
// VCL compiler abort with an assertion at startup, so the pod crash-loops.
var reservedStorageNames = map[string]bool{
	"synth": true,
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

// reservedPodPorts are the fixed ports the operator binds in every pod. The
// containers share the pod's network namespace, so a PROXY listener on one of
// them keeps varnishd from starting.
var reservedPodPorts = map[int32]string{
	8080: "the varnish HTTP listener",
	6082: "the varnishd admin CLI",
	9090: "the vinyl-agent",
	9131: "the metrics exporter",
	9464: "the tracer metrics endpoint",
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
//   - varnishParameters key shape (bare varnishd parameter names) and blocklist
//     (security-sensitive parameters)
//   - storage type blocklist and reserved storage names
//   - backend name VCL identifier conformance
//   - allowedSources CIDR syntax for purge, BAN, and xkey invalidation
//   - spec.pod.env duplicate names
//   - spec.proxyProtocol.port not colliding with a port the operator binds
//   - tracing.otlp.endpoint required and host:port formatted when tracing is enabled
func ValidateVinylCache(vc *vinylv1.VinylCache) (admission.Warnings, error) {
	var errs []string

	// Validate varnishParameters key shape and blocklist.
	//
	// The shape check runs unconditionally, not just when a key happens to
	// collide with forbiddenVarnishParams: varnishParamArgs (controller-side)
	// renders the raw map key straight into the varnish container's argv, so
	// a key like "  cc_command", "cc_command " or "CC_COMMAND" would slip
	// past a bare forbiddenVarnishParams[k] lookup while varnishd still
	// treats it as (or close enough to) the blocked parameter. Rejecting
	// anything that isn't a plain lowercase varnishd parameter name closes
	// that off in one rule instead of trying to enumerate every
	// normalization that could be used to dodge the blocklist.
	for k := range vc.Spec.VarnishParams {
		if !isValidVarnishParamKey(k) {
			errs = append(errs, fmt.Sprintf(
				"varnishParameters key %q is not a valid varnishd parameter name "+
					"(must start with a lowercase letter and contain only lowercase letters, digits, and underscores)",
				k,
			))
		}
		if forbiddenVarnishParams[k] {
			errs = append(errs, fmt.Sprintf("varnishParameters key %q is not allowed", k))
		}
	}

	// Validate storage type blocklist and reserved storage names.
	for _, s := range vc.Spec.Storage {
		if forbiddenStorageTypes[s.Type] {
			errs = append(errs, fmt.Sprintf("storage type %q is not allowed (use malloc or file)", s.Type))
		}
		if reservedStorageNames[s.Name] {
			errs = append(errs, fmt.Sprintf("storage name %q is reserved by varnishd", s.Name))
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

	errs = append(errs, validatePodEnv(vc.Spec.Pod.Env)...)

	errs = append(errs, validateProxyProtocolPort(vc)...)

	// Validate spec.storage[].path does not write into a reserved mount.
	for _, s := range vc.Spec.Storage {
		if s.Type == "file" && pathIsReserved(s.Path) {
			errs = append(errs, fmt.Sprintf(
				"spec.storage[%q].path %q is reserved by the operator; mount your own volume and place the cache file there",
				s.Name, s.Path))
		}
	}

	// Validate tracing: an enabled tracer without a collector endpoint (or
	// with a malformed one) would crash-loop the sidecar.
	if vc.Spec.Tracing.Enabled {
		ep := vc.Spec.Tracing.OTLP.Endpoint
		if ep == "" {
			errs = append(errs, "tracing.otlp.endpoint is required when tracing is enabled")
		} else if _, _, err := net.SplitHostPort(ep); err != nil {
			errs = append(errs, fmt.Sprintf("tracing.otlp.endpoint %q must be host:port: %v", ep, err))
		}
	}

	if len(errs) > 0 {
		return nil, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil, nil
}

// ValidateVinylCacheUpdate validates an update: everything ValidateVinylCache
// checks on the new object, plus the rules that need the old one.
func ValidateVinylCacheUpdate(old, vc *vinylv1.VinylCache) (admission.Warnings, error) {
	warnings, err := ValidateVinylCache(vc)
	var errs []string
	if err != nil {
		errs = append(errs, err.Error())
	}
	// A StatefulSet's volumeClaimTemplates cannot change after creation, so
	// an edit here would fail every later reconcile (#46). Semantic
	// equality treats 1Gi and 1024Mi, and nil and empty, as the same.
	if !equality.Semantic.DeepEqual(old.Spec.VolumeClaimTemplates, vc.Spec.VolumeClaimTemplates) {
		errs = append(errs, "spec.volumeClaimTemplates is immutable after creation, as on a StatefulSet. "+
			"Existing PVCs outlive the VinylCache and are reused when it is recreated, so to change "+
			"their size resize the PVCs directly (if the StorageClass allows expansion), or delete "+
			"the VinylCache and its PVCs and create it again")
	}
	if len(errs) > 0 {
		return warnings, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return warnings, nil
}

// validateProxyProtocolPort rejects a PROXY listener on a port the operator
// already binds in the pod, which would keep varnishd from starting.
func validateProxyProtocolPort(vc *vinylv1.VinylCache) []string {
	pp := vc.Spec.ProxyProtocol
	if !pp.Enabled {
		return nil
	}
	if what, taken := reservedPodPorts[pp.Port]; taken {
		return []string{fmt.Sprintf("spec.proxyProtocol.port %d is taken by %s", pp.Port, what)}
	}
	if exp := vc.Spec.Monitoring.Exporter; exp != nil && exp.Enabled && exp.Port == pp.Port {
		return []string{fmt.Sprintf("spec.proxyProtocol.port %d is taken by the metrics exporter", pp.Port)}
	}
	return nil
}

// validatePodEnv checks spec.pod.env for duplicate names, which would leave
// it to the kubelet which value wins.
func validatePodEnv(env []corev1.EnvVar) []string {
	var errs []string
	seen := make(map[string]bool, len(env))
	for _, e := range env {
		if seen[e.Name] {
			errs = append(errs, fmt.Sprintf(
				"spec.pod.env[%q]: duplicate env name", e.Name))
			continue
		}
		seen[e.Name] = true
	}
	return errs
}

func purgeAllowedSources(vc *vinylv1.VinylCache) []string {
	if vc.Spec.Invalidation.Purge == nil {
		return nil
	}
	return vc.Spec.Invalidation.Purge.AllowedSources
}

func banAllowedSources(vc *vinylv1.VinylCache) []string {
	if vc.Spec.Invalidation.BAN == nil {
		return nil
	}
	return vc.Spec.Invalidation.BAN.AllowedSources
}

func xkeyAllowedSources(vc *vinylv1.VinylCache) []string {
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

// isValidVarnishParamKey reports whether key is a bare varnishd parameter
// name: starts with a lowercase ASCII letter, followed by lowercase ASCII
// letters, digits, or underscores. Equivalent to the pattern
// ^[a-z][a-z0-9_]*$.
//
// This is intentionally an allowlist shape check, not just "absent from
// forbiddenVarnishParams": varnishd parameter names are lowercase, so
// anything outside this shape (whitespace padding, uppercase, punctuation)
// is either not a real parameter name or a disguised variant of one, and
// either way has no legitimate reason to reach varnishArgs.
func isValidVarnishParamKey(key string) bool {
	if len(key) == 0 {
		return false
	}
	runes := []rune(key)
	if runes[0] < 'a' || runes[0] > 'z' {
		return false
	}
	for _, r := range runes[1:] {
		isLower := r >= 'a' && r <= 'z'
		isDigit := r >= '0' && r <= '9'
		if !isLower && !isDigit && r != '_' {
			return false
		}
	}
	return true
}
