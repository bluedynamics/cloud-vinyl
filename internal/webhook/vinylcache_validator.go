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
	"regexp"
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
	// The operator mounts the volumes it provisions for file storages
	// without a path here (controller.FileStorageMountRoot, #131).
	"/var/lib/vinyl-storage",
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

	errs = append(errs, validateHashHeaders(vc)...)

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

	errs = append(errs, validatePodAntiAffinity(vc.Spec.Pod)...)

	errs = append(errs, validateProxyProtocolPort(vc)...)

	errs = append(errs, validateFileStorage(vc.Spec.Storage)...)

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

	warnings := legacyBackendWarnings(vc)
	warnings = append(warnings, fileStorageWarnings(vc)...)

	if len(errs) > 0 {
		return warnings, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return warnings, nil
}

// validateFileStorage checks type: file storages. With a path, the file must
// not land in a mount the operator owns. Without one, the operator
// provisions a volume of the storage's size (#131), so the size is required.
func validateFileStorage(storage []vinylv1.StorageSpec) []string {
	var errs []string
	for _, s := range storage {
		if s.Type != "file" {
			continue
		}
		switch {
		case s.Path == "" && s.Size.Sign() <= 0:
			errs = append(errs, fmt.Sprintf(
				"spec.storage[%q]: a file storage without path needs a size; the operator provisions a volume of that size",
				s.Name))
		case pathIsReserved(s.Path):
			errs = append(errs, fmt.Sprintf(
				"spec.storage[%q].path %q is reserved by the operator; mount your own volume and place the cache file there, or omit path to let the operator provision one",
				s.Name, s.Path))
		}
	}
	return errs
}

// fileStorageWarnings warns about a file storage whose explicit path lies
// under none of spec.pod.volumeMounts. varnishd then writes the file into
// the container's writable layer, where it counts against the node's image
// filesystem and can get unrelated pods evicted. A warning, not an error:
// existing caches configured that way must stay updatable.
func fileStorageWarnings(vc *vinylv1.VinylCache) admission.Warnings {
	var warnings admission.Warnings
	for _, s := range vc.Spec.Storage {
		if s.Type != "file" || s.Path == "" || pathIsReserved(s.Path) {
			continue
		}
		mounted := false
		for _, m := range vc.Spec.Pod.VolumeMounts {
			mp := strings.TrimRight(m.MountPath, "/")
			if strings.HasPrefix(s.Path, mp+"/") {
				mounted = true
				break
			}
		}
		if !mounted {
			warnings = append(warnings, fmt.Sprintf(
				"spec.storage[%q].path %q lies under none of spec.pod.volumeMounts, so the cache file is written into the container's writable layer; mount a volume there, or omit path to let the operator provision one",
				s.Name, s.Path))
		}
	}
	return warnings
}

// snippetField pairs a spec.vcl.snippets field path with its VCL text.
type snippetField struct {
	path string
	vcl  string
}

// snippetFields lists every spec.vcl.snippets field in a stable order.
func snippetFields(s vinylv1.VCLSnippets) []snippetField {
	const p = "spec.vcl.snippets."
	return []snippetField{
		{p + "header", s.Header},
		{p + "vclInit", s.VCLInit},
		{p + "vclRecv", s.VCLRecv},
		{p + "vclHash", s.VCLHash},
		{p + "vclHit", s.VCLHit},
		{p + "vclMiss", s.VCLMiss},
		{p + "vclPass", s.VCLPass},
		{p + "vclPurge", s.VCLPurge},
		{p + "vclPipe", s.VCLPipe},
		{p + "vclBackendFetch", s.VCLBackendFetch},
		{p + "vclBackendResponse", s.VCLBackendResponse},
		{p + "vclDeliver", s.VCLDeliver},
		{p + "vclSynth", s.VCLSynth},
		{p + "vclBackendError", s.VCLBackendError},
		{p + "vclFini", s.VCLFini},
	}
}

// legacyBackendWarnings reports snippets that reference per-pod backend names
// like "plone_0". Per-pod names are renumbered whenever the endpoints change,
// so such a snippet breaks on the next rollout (#35). It is a warning, not an
// error, to stay compatible with existing resources.
func legacyBackendWarnings(vc *vinylv1.VinylCache) admission.Warnings {
	var warnings admission.Warnings
	fields := snippetFields(vc.Spec.VCL.Snippets)
	for _, b := range vc.Spec.Backends {
		if b.Name == "" {
			continue
		}
		pat, err := regexp.Compile(`\b` + regexp.QuoteMeta(b.Name) + `_\d+\b`)
		if err != nil {
			continue
		}
		for _, f := range fields {
			if m := pat.FindString(f.vcl); m != "" {
				warnings = append(warnings, fmt.Sprintf(
					"%s references the per-pod backend %q; per-pod names are renumbered on every endpoint change. Use '%s.backend()' instead",
					f.path, m, b.Name))
			}
		}
	}
	return warnings
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

// validatePodAntiAffinity rejects spec.pod.podAntiAffinity next to a full
// affinity.podAntiAffinity. The operator only generates its rule when the
// latter is empty, so the shorthand would be ignored without a word (#147).
func validatePodAntiAffinity(pod vinylv1.PodSpec) []string {
	if pod.PodAntiAffinity != nil && pod.Affinity != nil && pod.Affinity.PodAntiAffinity != nil {
		return []string{"spec.pod.podAntiAffinity and spec.pod.affinity.podAntiAffinity are mutually exclusive: " +
			"set the shorthand or a full podAntiAffinity, not both"}
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

// httpHeaderToken matches an HTTP header field name (RFC 7230 token / tchar).
var httpHeaderToken = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

// validateHashHeaders rejects a hash.header that is not an HTTP header token.
// The value is rendered into VCL as req.http.<header> (#173), so anything
// else would break VCL compilation or inject VCL.
func validateHashHeaders(vc *vinylv1.VinylCache) []string {
	var errs []string
	check := func(path string, d *vinylv1.DirectorSpec) {
		if d == nil || d.Hash == nil || d.Hash.Header == "" {
			return
		}
		if !httpHeaderToken.MatchString(d.Hash.Header) {
			errs = append(errs, fmt.Sprintf(
				"%s.hash.header %q is not a valid HTTP header name (letters, digits and !#$%%&'*+-.^_`|~ only)",
				path, d.Hash.Header))
		}
	}
	check("spec.director", &vc.Spec.Director)
	for i := range vc.Spec.Backends {
		check(fmt.Sprintf("spec.backends[%q].director", vc.Spec.Backends[i].Name), vc.Spec.Backends[i].Director)
	}
	return errs
}
