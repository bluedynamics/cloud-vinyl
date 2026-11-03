package controller

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"

	vinylv1 "github.com/bluedynamics/cloud-vinyl/api/v1"
)

func fileStorageVC(storage ...vinylv1.StorageSpec) *vinylv1.VinylCache {
	vc := exporterBaseVC()
	vc.Spec.Storage = storage
	return vc
}

func TestStorageArgs_FileWithoutPathIsProvisioned(t *testing.T) {
	got := storageArgs([]vinylv1.StorageSpec{
		{Name: "disk", Type: "file", Size: resource.MustParse("10Gi")},
	})
	assert.Equal(t, []string{"-s", "disk=file,/var/lib/vinyl-storage/disk/disk.bin,10737418240"}, got)
}

func TestReconcileStatefulSet_ProvisionsFileStorage(t *testing.T) {
	vc := fileStorageVC(vinylv1.StorageSpec{Name: "disk", Type: "file", Size: resource.MustParse("10Gi")})
	ss := getStatefulSet(t, vc)

	var vol *corev1.Volume
	for i := range ss.Spec.Template.Spec.Volumes {
		if strings.HasPrefix(ss.Spec.Template.Spec.Volumes[i].Name, "storage-disk-") {
			vol = &ss.Spec.Template.Spec.Volumes[i]
		}
	}
	require.NotNil(t, vol, "provisioned volume")
	require.NotNil(t, vol.EmptyDir)
	// 10Gi plus 5 % (512Mi), more than the 64Mi minimum.
	wantLimit := resource.MustParse("10752Mi")
	assert.Equal(t, wantLimit.Value(), vol.EmptyDir.SizeLimit.Value())

	c := varnishContainer(t, ss)
	assert.Contains(t, c.VolumeMounts, corev1.VolumeMount{Name: vol.Name, MountPath: "/var/lib/vinyl-storage/disk"})
	assert.Contains(t, c.Args, "disk=file,/var/lib/vinyl-storage/disk/disk.bin,10737418240")
	eph := c.Resources.Requests[corev1.ResourceEphemeralStorage]
	assert.Equal(t, vol.EmptyDir.SizeLimit.Value(), eph.Value(), "request covers the volume")
	_, limited := c.Resources.Limits[corev1.ResourceEphemeralStorage]
	assert.False(t, limited, "sizeLimit bounds the volume, no container limit")
}

func TestReconcileStatefulSet_FileStorageWithPathIsNotProvisioned(t *testing.T) {
	vc := fileStorageVC(vinylv1.StorageSpec{
		Name: "disk", Type: "file", Path: "/cache/disk.bin", Size: resource.MustParse("10Gi"),
	})
	ss := getStatefulSet(t, vc)
	for _, v := range ss.Spec.Template.Spec.Volumes {
		assert.False(t, strings.HasPrefix(v.Name, "storage-"), "unexpected volume %s", v.Name)
	}
	c := varnishContainer(t, ss)
	assert.Contains(t, c.Args, "disk=file,/cache/disk.bin,10737418240")
	_, set := c.Resources.Requests[corev1.ResourceEphemeralStorage]
	assert.False(t, set)
}

func TestVarnishResources_SumsProvisionedVolumes(t *testing.T) {
	vc := fileStorageVC(
		vinylv1.StorageSpec{Name: "a", Type: "file", Size: resource.MustParse("1Gi")},
		vinylv1.StorageSpec{Name: "b", Type: "file", Size: resource.MustParse("100Mi")},
		vinylv1.StorageSpec{Name: "m", Type: "malloc", Size: resource.MustParse("1Gi")},
	)
	vc.Spec.Resources.Requests = corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("2Gi")}

	res := varnishResources(vc)
	// a: 1Gi + 64Mi (5 % would be 51.2Mi); b: 100Mi + 64Mi. malloc adds nothing.
	want := int64(1<<30 + 64<<20 + 100<<20 + 64<<20)
	eph := res.Requests[corev1.ResourceEphemeralStorage]
	assert.Equal(t, want, eph.Value())
	mem := res.Requests[corev1.ResourceMemory]
	assert.Equal(t, "2Gi", mem.String(), "other requests are kept")
	_, mutated := vc.Spec.Resources.Requests[corev1.ResourceEphemeralStorage]
	assert.False(t, mutated, "the spec must not be modified")
}

func TestVarnishResources_ExplicitEphemeralRequestWins(t *testing.T) {
	vc := fileStorageVC(vinylv1.StorageSpec{Name: "disk", Type: "file", Size: resource.MustParse("10Gi")})
	vc.Spec.Resources.Requests = corev1.ResourceList{corev1.ResourceEphemeralStorage: resource.MustParse("20Gi")}
	eph := varnishResources(vc).Requests[corev1.ResourceEphemeralStorage]
	assert.Equal(t, "20Gi", eph.String())
}

func TestFileStorageVolumeName(t *testing.T) {
	for _, name := range []string{"disk", "My_Cache", "a_b", "a-b", "___", strings.Repeat("x", 200)} {
		got := fileStorageVolumeName(name)
		assert.Empty(t, validation.IsDNS1123Label(got), "%q -> %q", name, got)
	}
	assert.NotEqual(t, fileStorageVolumeName("a_b"), fileStorageVolumeName("a-b"),
		"names that sanitize alike must not collide")
	assert.Equal(t, fileStorageVolumeName("disk"), fileStorageVolumeName("disk"), "stable across reconciles")
}
