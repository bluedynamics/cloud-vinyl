package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
)

func TestStorageArgs_Malloc(t *testing.T) {
	got := storageArgs([]v1alpha1.StorageSpec{
		{Name: "s0", Type: "malloc", Size: resource.MustParse("1500M")},
	})
	assert.Equal(t, []string{"-s", "s0=malloc,1500000000"}, got)
}

func TestStorageArgs_File(t *testing.T) {
	got := storageArgs([]v1alpha1.StorageSpec{
		{Name: "disk", Type: "file", Path: "/var/lib/varnish/cache.bin", Size: resource.MustParse("10Gi")},
	})
	assert.Equal(t, []string{"-s", "disk=file,/var/lib/varnish/cache.bin,10737418240"}, got)
}

func TestStorageArgs_Multiple(t *testing.T) {
	got := storageArgs([]v1alpha1.StorageSpec{
		{Name: "mem", Type: "malloc", Size: resource.MustParse("1G")},
		{Name: "disk", Type: "file", Path: "/var/lib/varnish/cache", Size: resource.MustParse("10G")},
	})
	assert.Equal(t, []string{
		"-s", "mem=malloc,1000000000",
		"-s", "disk=file,/var/lib/varnish/cache,10000000000",
	}, got)
}

func TestStorageArgs_Empty(t *testing.T) {
	assert.Nil(t, storageArgs(nil))
	assert.Nil(t, storageArgs([]v1alpha1.StorageSpec{}))
}

func TestReconcileStatefulSet_UserVolumesAndMountsAppended(t *testing.T) {
	sch := newScheme(t)
	quantity := resource.MustParse("100Mi")
	vc := &v1alpha1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "my-cache", Namespace: "app"},
		Spec: v1alpha1.VinylCacheSpec{
			Replicas: 1,
			Image:    "varnish:7.6",
			Backends: []v1alpha1.BackendSpec{{
				Name: "app", ServiceRef: v1alpha1.ServiceRef{Name: "svc"},
			}},
			Pod: v1alpha1.PodSpec{
				Volumes: []corev1.Volume{
					{
						Name: "cache-ssd",
						VolumeSource: corev1.VolumeSource{
							EmptyDir: &corev1.EmptyDirVolumeSource{
								SizeLimit: &quantity,
							},
						},
					},
				},
				VolumeMounts: []corev1.VolumeMount{
					{Name: "cache-ssd", MountPath: "/var/lib/varnish-cache"},
				},
			},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).Build()
	r := &VinylCacheReconciler{Client: cli, Scheme: sch}
	require.NoError(t, r.reconcileStatefulSet(context.Background(), vc))

	ss := &appsv1.StatefulSet{}
	require.NoError(t, cli.Get(context.Background(),
		types.NamespacedName{Name: vc.Name, Namespace: vc.Namespace}, ss))

	// User volume present in pod spec.
	var foundVolume bool
	for _, v := range ss.Spec.Template.Spec.Volumes {
		if v.Name == "cache-ssd" {
			foundVolume = true
			require.NotNil(t, v.EmptyDir)
			require.NotNil(t, v.EmptyDir.SizeLimit)
			assert.Equal(t, "100Mi", v.EmptyDir.SizeLimit.String())
		}
	}
	assert.True(t, foundVolume, "user volume 'cache-ssd' must be appended to pod volumes")

	// User mount present on the varnish container.
	var varnish *corev1.Container
	for i := range ss.Spec.Template.Spec.Containers {
		if ss.Spec.Template.Spec.Containers[i].Name == "varnish" {
			varnish = &ss.Spec.Template.Spec.Containers[i]
		}
	}
	require.NotNil(t, varnish)
	var foundMount bool
	for _, m := range varnish.VolumeMounts {
		if m.Name == "cache-ssd" {
			foundMount = true
			assert.Equal(t, "/var/lib/varnish-cache", m.MountPath)
		}
	}
	assert.True(t, foundMount, "user volumeMount must be appended to varnish container")

	// Reserved volumes still present.
	reserved := []string{"agent-token", "varnish-secret", "varnish-workdir", "varnish-tmp", "bootstrap-vcl"}
	for _, rn := range reserved {
		var found bool
		for _, v := range ss.Spec.Template.Spec.Volumes {
			if v.Name == rn {
				found = true
			}
		}
		assert.True(t, found, "reserved volume %q must remain present", rn)
	}
}

func TestReconcileStatefulSet_VolumeClaimTemplatesPassthrough(t *testing.T) {
	sch := newScheme(t)
	storageClass := "hcloud-volumes"
	vc := &v1alpha1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "my-cache", Namespace: "app"},
		Spec: v1alpha1.VinylCacheSpec{
			Replicas: 2,
			Image:    "varnish:7.6",
			Backends: []v1alpha1.BackendSpec{{
				Name: "app", ServiceRef: v1alpha1.ServiceRef{Name: "svc"},
			}},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{Name: "cache-ssd"},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					StorageClassName: &storageClass,
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("80Gi"),
						},
					},
				},
			}},
			Pod: v1alpha1.PodSpec{
				VolumeMounts: []corev1.VolumeMount{
					{Name: "cache-ssd", MountPath: "/var/lib/varnish-cache"},
				},
			},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).Build()
	r := &VinylCacheReconciler{Client: cli, Scheme: sch}
	require.NoError(t, r.reconcileStatefulSet(context.Background(), vc))

	ss := &appsv1.StatefulSet{}
	require.NoError(t, cli.Get(context.Background(),
		types.NamespacedName{Name: vc.Name, Namespace: vc.Namespace}, ss))

	require.Len(t, ss.Spec.VolumeClaimTemplates, 1)
	pvc := ss.Spec.VolumeClaimTemplates[0]
	assert.Equal(t, "cache-ssd", pvc.Name)
	require.NotNil(t, pvc.Spec.StorageClassName)
	assert.Equal(t, "hcloud-volumes", *pvc.Spec.StorageClassName)
	assert.Equal(t, "80Gi", pvc.Spec.Resources.Requests.Storage().String())
}

func TestReconcileStatefulSet_NoUserVolumes_DefaultsUnchanged(t *testing.T) {
	sch := newScheme(t)
	vc := &v1alpha1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "my-cache", Namespace: "app"},
		Spec: v1alpha1.VinylCacheSpec{
			Replicas: 1,
			Image:    "varnish:7.6",
			Backends: []v1alpha1.BackendSpec{{
				Name: "app", ServiceRef: v1alpha1.ServiceRef{Name: "svc"},
			}},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).Build()
	r := &VinylCacheReconciler{Client: cli, Scheme: sch}
	require.NoError(t, r.reconcileStatefulSet(context.Background(), vc))

	ss := &appsv1.StatefulSet{}
	require.NoError(t, cli.Get(context.Background(),
		types.NamespacedName{Name: vc.Name, Namespace: vc.Namespace}, ss))

	assert.Empty(t, ss.Spec.VolumeClaimTemplates,
		"no user claim templates -> VolumeClaimTemplates stays empty")
	assert.Len(t, ss.Spec.Template.Spec.Volumes, 5,
		"no user volumes -> only the 5 operator-managed volumes remain")
}
