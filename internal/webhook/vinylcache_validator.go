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
