package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/api/resource"

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
