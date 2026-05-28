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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	vinylv1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
	"github.com/bluedynamics/cloud-vinyl/internal/webhook"
)

// minimalValidVC returns a VinylCache with the minimum valid configuration required
// to pass validation (one valid backend, no blocked params or storage types).
func minimalValidVC() *vinylv1alpha1.VinylCache {
	return &vinylv1alpha1.VinylCache{
		Spec: vinylv1alpha1.VinylCacheSpec{
			Replicas: 1,
			Image:    "ghcr.io/bluedynamics/cloud-vinyl-varnish:7.6.0",
			Backends: []vinylv1alpha1.BackendSpec{
				{
					Name:       "app",
					ServiceRef: vinylv1alpha1.ServiceRef{Name: "app-service"},
				},
			},
		},
	}
}

// --- varnishParameters blocklist ---

func TestValidate_VarnishParameters_Blocklist(t *testing.T) {
	cases := []struct {
		name    string
		param   string
		wantErr bool
	}{
		{
			name:    "vcc_allow_inline_c is blocked (RCE risk)",
			param:   "vcc_allow_inline_c",
			wantErr: true,
		},
		{
			name:    "cc_command is blocked (arbitrary compiler invocation)",
			param:   "cc_command",
			wantErr: true,
		},
		{
			name:    "thread_pool_min is allowed",
			param:   "thread_pool_min",
			wantErr: false,
		},
		{
			name:    "ban_lurker_sleep is allowed",
			param:   "ban_lurker_sleep",
			wantErr: false,
		},
		{
			name:    "thread_pool_max is allowed",
			param:   "thread_pool_max",
			wantErr: false,
		},
		{
			name:    "timeout_idle is allowed",
			param:   "timeout_idle",
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vc := minimalValidVC()
			vc.Spec.VarnishParams = map[string]string{tc.param: "1"}

			_, err := webhook.ValidateVinylCache(vc)
			if tc.wantErr {
				require.Error(t, err, "expected validation error for param %q", tc.param)
				assert.Contains(t, err.Error(), tc.param)
			} else {
				assert.NoError(t, err, "expected no validation error for param %q", tc.param)
			}
		})
	}
}

func TestValidate_VarnishParameters_MultipleBlocked(t *testing.T) {
	vc := minimalValidVC()
	vc.Spec.VarnishParams = map[string]string{
		"vcc_allow_inline_c": "1",
		"cc_command":         "gcc",
		"thread_pool_min":    "100",
	}

	_, err := webhook.ValidateVinylCache(vc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vcc_allow_inline_c")
	assert.Contains(t, err.Error(), "cc_command")
	assert.NotContains(t, err.Error(), "thread_pool_min")
}

func TestValidate_VarnishParameters_Empty(t *testing.T) {
	vc := minimalValidVC()
	vc.Spec.VarnishParams = nil

	_, err := webhook.ValidateVinylCache(vc)
	assert.NoError(t, err)
}

// --- storage type blocklist ---

func TestValidate_StorageType_Blocklist(t *testing.T) {
	cases := []struct {
		name        string
		storageType string
		wantErr     bool
	}{
		{
			name:        "persistent is blocked (deprecated, broken across restarts)",
			storageType: "persistent",
			wantErr:     true,
		},
		{
			name:        "umem is blocked (Solaris/illumos only)",
			storageType: "umem",
			wantErr:     true,
		},
		{
			name:        "default is blocked (confusing alias for malloc)",
			storageType: "default",
			wantErr:     true,
		},
		{
			name:        "malloc is allowed",
			storageType: "malloc",
			wantErr:     false,
		},
		{
			name:        "file is allowed",
			storageType: "file",
			wantErr:     false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vc := minimalValidVC()
			vc.Spec.Storage = []vinylv1alpha1.StorageSpec{
				{Name: "cache", Type: tc.storageType},
			}

			_, err := webhook.ValidateVinylCache(vc)
			if tc.wantErr {
				require.Error(t, err, "expected validation error for storage type %q", tc.storageType)
				assert.Contains(t, err.Error(), tc.storageType)
			} else {
				assert.NoError(t, err, "expected no validation error for storage type %q", tc.storageType)
			}
		})
	}
}

func TestValidate_StorageType_NoStorage(t *testing.T) {
	vc := minimalValidVC()
	vc.Spec.Storage = nil

	_, err := webhook.ValidateVinylCache(vc)
	assert.NoError(t, err)
}

// --- backend name VCL identifier validation ---

func TestValidate_BackendNames_VCLConformant(t *testing.T) {
	cases := []struct {
		name        string
		backendName string
		wantErr     bool
	}{
		{
			name:        "underscore separator is valid",
			backendName: "my_backend",
			wantErr:     false,
		},
		{
			name:        "letter followed by digit is valid",
			backendName: "app1",
			wantErr:     false,
		},
		{
			name:        "mixed case is valid",
			backendName: "AppBackend",
			wantErr:     false,
		},
		{
			name:        "single letter is valid",
			backendName: "a",
			wantErr:     false,
		},
		{
			name:        "underscore suffix is valid",
			backendName: "backend_",
			wantErr:     false,
		},
		{
			name:        "hyphen is invalid (not a VCL identifier character)",
			backendName: "my-backend",
			wantErr:     true,
		},
		{
			name:        "starts with digit is invalid",
			backendName: "123start",
			wantErr:     true,
		},
		{
			name:        "starts with underscore is invalid",
			backendName: "_backend",
			wantErr:     true,
		},
		{
			name:        "empty string is invalid",
			backendName: "",
			wantErr:     true,
		},
		{
			name:        "dot separator is invalid",
			backendName: "my.backend",
			wantErr:     true,
		},
		{
			name:        "space is invalid",
			backendName: "my backend",
			wantErr:     true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vc := minimalValidVC()
			vc.Spec.Backends = []vinylv1alpha1.BackendSpec{
				{
					Name:       tc.backendName,
					ServiceRef: vinylv1alpha1.ServiceRef{Name: "svc"},
				},
			}

			_, err := webhook.ValidateVinylCache(vc)
			if tc.wantErr {
				require.Error(t, err, "expected validation error for backend name %q", tc.backendName)
			} else {
				assert.NoError(t, err, "expected no validation error for backend name %q", tc.backendName)
			}
		})
	}
}

// --- CIDR allowedSources validation ---

func TestValidate_AllowedSources_Purge_CIDRSyntax(t *testing.T) {
	cases := []struct {
		name    string
		cidr    string
		wantErr bool
	}{
		{
			name:    "host CIDR /32 is valid",
			cidr:    "10.0.0.1/32",
			wantErr: false,
		},
		{
			name:    "network CIDR /24 is valid",
			cidr:    "10.0.0.0/24",
			wantErr: false,
		},
		{
			name:    "IPv6 CIDR is valid",
			cidr:    "2001:db8::/32",
			wantErr: false,
		},
		{
			name:    "octet 256 is invalid",
			cidr:    "10.0.256.0/24",
			wantErr: true,
		},
		{
			name:    "not-an-ip is invalid",
			cidr:    "not-an-ip",
			wantErr: true,
		},
		{
			name:    "missing prefix length is invalid",
			cidr:    "10.0.0.1",
			wantErr: true,
		},
		{
			name:    "empty string is invalid",
			cidr:    "",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run("purge/"+tc.name, func(t *testing.T) {
			vc := minimalValidVC()
			vc.Spec.Invalidation.Purge = &vinylv1alpha1.PurgeSpec{
				Enabled:        true,
				AllowedSources: []string{tc.cidr},
			}

			_, err := webhook.ValidateVinylCache(vc)
			if tc.wantErr {
				require.Error(t, err, "expected validation error for CIDR %q", tc.cidr)
				assert.Contains(t, err.Error(), tc.cidr)
			} else {
				assert.NoError(t, err, "expected no validation error for CIDR %q", tc.cidr)
			}
		})
	}
}

func TestValidate_AllowedSources_BAN_CIDRSyntax(t *testing.T) {
	vc := minimalValidVC()
	vc.Spec.Invalidation.BAN = &vinylv1alpha1.BANSpec{
		Enabled:        true,
		AllowedSources: []string{"192.168.1.0/24", "not-valid"},
	}

	_, err := webhook.ValidateVinylCache(vc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not-valid")
}

func TestValidate_AllowedSources_Xkey_CIDRSyntax(t *testing.T) {
	vc := minimalValidVC()
	vc.Spec.Invalidation.Xkey = &vinylv1alpha1.XkeySpec{
		Enabled:        true,
		AllowedSources: []string{"172.16.0.0/12", "bad-cidr"},
	}

	_, err := webhook.ValidateVinylCache(vc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad-cidr")
}

func TestValidate_AllowedSources_NilInvalidationSpecs(t *testing.T) {
	vc := minimalValidVC()
	vc.Spec.Invalidation.Purge = nil
	vc.Spec.Invalidation.BAN = nil
	vc.Spec.Invalidation.Xkey = nil

	_, err := webhook.ValidateVinylCache(vc)
	assert.NoError(t, err)
}

// --- combined validation: multiple errors accumulated ---

func TestValidate_MultipleErrors_AllAccumulated(t *testing.T) {
	vc := minimalValidVC()
	vc.Spec.VarnishParams = map[string]string{"vcc_allow_inline_c": "1"}
	vc.Spec.Storage = []vinylv1alpha1.StorageSpec{
		{Name: "bad", Type: "persistent"},
	}
	vc.Spec.Backends = []vinylv1alpha1.BackendSpec{
		{Name: "bad-name", ServiceRef: vinylv1alpha1.ServiceRef{Name: "svc"}},
	}
	vc.Spec.Invalidation.Purge = &vinylv1alpha1.PurgeSpec{
		AllowedSources: []string{"not-a-cidr"},
	}

	_, err := webhook.ValidateVinylCache(vc)
	require.Error(t, err)
	errStr := err.Error()
	assert.Contains(t, errStr, "vcc_allow_inline_c")
	assert.Contains(t, errStr, "persistent")
	assert.Contains(t, errStr, "bad-name")
	assert.Contains(t, errStr, "not-a-cidr")
}

// --- valid minimal configuration passes ---

func TestValidate_ValidMinimalConfig_Passes(t *testing.T) {
	vc := minimalValidVC()

	_, err := webhook.ValidateVinylCache(vc)
	assert.NoError(t, err)
}

func TestValidate_ValidFullConfig_Passes(t *testing.T) {
	vc := minimalValidVC()
	vc.Spec.VarnishParams = map[string]string{
		"thread_pool_min": "100",
		"thread_pool_max": "1000",
	}
	vc.Spec.Storage = []vinylv1alpha1.StorageSpec{
		{Name: "cache", Type: "malloc"},
		{Name: "transient", Type: "file", Path: "/var/cache"},
	}
	vc.Spec.Backends = append(vc.Spec.Backends, vinylv1alpha1.BackendSpec{
		Name:       "legacy",
		ServiceRef: vinylv1alpha1.ServiceRef{Name: "legacy-svc"},
	})
	vc.Spec.Invalidation.Purge = &vinylv1alpha1.PurgeSpec{
		Enabled:        true,
		AllowedSources: []string{"10.0.0.0/8", "192.168.0.0/16"},
	}
	vc.Spec.Invalidation.BAN = &vinylv1alpha1.BANSpec{
		Enabled:        true,
		AllowedSources: []string{"172.16.0.0/12"},
	}
	vc.Spec.Invalidation.Xkey = &vinylv1alpha1.XkeySpec{
		Enabled:        true,
		AllowedSources: []string{"10.0.0.0/8"},
	}

	_, err := webhook.ValidateVinylCache(vc)
	assert.NoError(t, err)
}

// --- pod volumes, volumeMounts, volumeClaimTemplates, and storage path collisions ---

// validBaseVinylCache returns a minimally valid VinylCache with ObjectMeta set,
// used by tests that exercise pod.volumes, pod.volumeMounts, volumeClaimTemplates,
// and storage-path collision validation.
func validBaseVinylCache() *vinylv1alpha1.VinylCache {
	return &vinylv1alpha1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "vc", Namespace: "ns"},
		Spec: vinylv1alpha1.VinylCacheSpec{
			Replicas: 1,
			Image:    "varnish:7.6",
			Backends: []vinylv1alpha1.BackendSpec{
				{Name: "app", ServiceRef: vinylv1alpha1.ServiceRef{Name: "svc"}},
			},
		},
	}
}

func TestValidate_PodVolumeName_ReservedIsRejected(t *testing.T) {
	vc := validBaseVinylCache()
	vc.Spec.Pod.Volumes = []corev1.Volume{{
		Name:         "varnish-workdir", // reserved
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	}}
	_, err := webhook.ValidateVinylCache(vc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "varnish-workdir")
	assert.Contains(t, err.Error(), "reserved")
}

func TestValidate_VolumeClaimTemplateName_ReservedIsRejected(t *testing.T) {
	vc := validBaseVinylCache()
	vc.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{{
		ObjectMeta: metav1.ObjectMeta{Name: "bootstrap-vcl"}, // reserved
	}}
	_, err := webhook.ValidateVinylCache(vc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bootstrap-vcl")
}

func TestValidate_VolumeNameDuplicate_AcrossVolumesAndClaims_Rejected(t *testing.T) {
	vc := validBaseVinylCache()
	vc.Spec.Pod.Volumes = []corev1.Volume{{
		Name:         "cache-ssd",
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	}}
	vc.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{{
		ObjectMeta: metav1.ObjectMeta{Name: "cache-ssd"},
	}}
	_, err := webhook.ValidateVinylCache(vc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cache-ssd")
	assert.Contains(t, err.Error(), "duplicate")
}

func TestValidate_VolumeMountPath_ReservedIsRejected(t *testing.T) {
	vc := validBaseVinylCache()
	vc.Spec.Pod.Volumes = []corev1.Volume{{
		Name:         "my-ssd",
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	}}
	vc.Spec.Pod.VolumeMounts = []corev1.VolumeMount{
		{Name: "my-ssd", MountPath: "/var/lib/varnish"}, // reserved
	}
	_, err := webhook.ValidateVinylCache(vc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "/var/lib/varnish")
}

func TestValidate_VolumeMountPath_AncestorOfReservedIsRejected(t *testing.T) {
	// Guards against a shadowing attack: mounting /etc/varnish would
	// cover the operator's subPath mounts at /etc/varnish/secret and
	// /etc/varnish/default.vcl, giving a user with VinylCache edit
	// rights a new vector onto operator-owned data.
	cases := []struct {
		name      string
		mountPath string
	}{
		{"etc-varnish", "/etc/varnish"}, // ancestor of /etc/varnish/secret and /etc/varnish/default.vcl
		{"var-lib", "/var/lib"},         // ancestor of /var/lib/varnish
		{"root", "/"},                   // ancestor of everything
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vc := validBaseVinylCache()
			vc.Spec.Pod.Volumes = []corev1.Volume{{
				Name:         "my-vol",
				VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
			}}
			vc.Spec.Pod.VolumeMounts = []corev1.VolumeMount{
				{Name: "my-vol", MountPath: tc.mountPath},
			}
			_, err := webhook.ValidateVinylCache(vc)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.mountPath)
			assert.Contains(t, err.Error(), "conflicts with a reserved")
		})
	}
}

func TestValidate_VolumeMountName_Unresolvable_Rejected(t *testing.T) {
	vc := validBaseVinylCache()
	vc.Spec.Pod.VolumeMounts = []corev1.VolumeMount{
		{Name: "nonexistent", MountPath: "/data"},
	}
	_, err := webhook.ValidateVinylCache(vc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nonexistent")
	assert.Contains(t, err.Error(), "not declared")
}

func TestValidate_StoragePath_UnderReservedMount_Rejected(t *testing.T) {
	vc := validBaseVinylCache()
	vc.Spec.Storage = []vinylv1alpha1.StorageSpec{{
		Name: "disk",
		Type: "file",
		Path: "/var/lib/varnish/spill.bin", // under reserved /var/lib/varnish
		Size: resource.MustParse("10Gi"),
	}}
	_, err := webhook.ValidateVinylCache(vc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "/var/lib/varnish")
	assert.Contains(t, err.Error(), "storage")
}

func TestValidate_StoragePath_UnderUserMount_Accepted(t *testing.T) {
	vc := validBaseVinylCache()
	vc.Spec.Pod.Volumes = []corev1.Volume{{
		Name:         "cache-ssd",
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	}}
	vc.Spec.Pod.VolumeMounts = []corev1.VolumeMount{
		{Name: "cache-ssd", MountPath: "/var/lib/varnish-cache"},
	}
	vc.Spec.Storage = []vinylv1alpha1.StorageSpec{{
		Name: "disk",
		Type: "file",
		Path: "/var/lib/varnish-cache/spill.bin",
		Size: resource.MustParse("10Gi"),
	}}
	_, err := webhook.ValidateVinylCache(vc)
	require.NoError(t, err)
}
