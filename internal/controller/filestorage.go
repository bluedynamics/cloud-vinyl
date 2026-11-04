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
	"fmt"
	"hash/fnv"
	"path"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	vinylv1 "github.com/bluedynamics/cloud-vinyl/api/v1"
)

const (
	// FileStorageMountRoot is where the operator mounts the volumes it
	// provisions for file storages without a path (#131). The webhook
	// reserves it.
	FileStorageMountRoot = "/var/lib/vinyl-storage"

	// fileStorageMinMargin is the least headroom between a provisioned
	// volume's sizeLimit and the storage size. varnishd preallocates the
	// file at exactly the storage size; the margin covers filesystem
	// overhead, so a full cache never trips the limit and gets the pod
	// evicted.
	fileStorageMinMargin = 64 << 20
	// fileStorageMarginPercent is the headroom as a share of the size, used
	// when it is larger than fileStorageMinMargin.
	fileStorageMarginPercent = 5
)

// provisionedStorage is a type: file storage without a path. The operator
// provisions an emptyDir volume for it and places the cache file there,
// instead of letting varnishd write into the container's writable layer.
type provisionedStorage struct {
	volume    string
	mountPath string
	filePath  string
	sizeLimit resource.Quantity
}

// isProvisionedFileStorage reports whether the operator provisions the
// volume for s. With an explicit path the user brings their own volume.
func isProvisionedFileStorage(s vinylv1.StorageSpec) bool {
	return s.Type == "file" && s.Path == ""
}

func provisionFileStorage(s vinylv1.StorageSpec) provisionedStorage {
	mount := path.Join(FileStorageMountRoot, s.Name)
	return provisionedStorage{
		volume:    fileStorageVolumeName(s.Name),
		mountPath: mount,
		filePath:  path.Join(mount, s.Name+".bin"),
		sizeLimit: fileStorageSizeLimit(s.Size),
	}
}

// provisionedFileStorages returns the storages the operator provisions, in
// spec order.
func provisionedFileStorages(storage []vinylv1.StorageSpec) []provisionedStorage {
	var out []provisionedStorage
	for _, s := range storage {
		if isProvisionedFileStorage(s) {
			out = append(out, provisionFileStorage(s))
		}
	}
	return out
}

// fileStoragePath is the cache file varnishd gets for a file storage.
func fileStoragePath(s vinylv1.StorageSpec) string {
	if isProvisionedFileStorage(s) {
		return provisionFileStorage(s).filePath
	}
	return s.Path
}

// fileStorageSizeLimit is size plus 5 %, at least plus 64Mi.
func fileStorageSizeLimit(size resource.Quantity) resource.Quantity {
	bytes := size.Value()
	margin := max(bytes*fileStorageMarginPercent/100, fileStorageMinMargin)
	return *resource.NewQuantity(bytes+margin, resource.BinarySI)
}

// fileStorageVolumeName turns a storage name into a volume name. Storage
// names may hold characters a volume name (a DNS-1123 label) cannot, such
// as "_" or upper case, so the name is sanitized and a hash of the original
// keeps two storages apart that sanitize to the same string.
func fileStorageVolumeName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	// "storage-" + base + "-" + 8 hex digits stays within the 63 characters
	// of a DNS-1123 label.
	base := strings.Trim(b.String(), "-")
	if len(base) > 45 {
		base = strings.TrimRight(base[:45], "-")
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	if base == "" {
		return fmt.Sprintf("storage-%08x", h.Sum32())
	}
	return fmt.Sprintf("storage-%s-%08x", base, h.Sum32())
}

// fileStorageVolumes returns the emptyDir volumes for the provisioned
// storages.
func fileStorageVolumes(ps []provisionedStorage) []corev1.Volume {
	vols := make([]corev1.Volume, 0, len(ps))
	for _, p := range ps {
		limit := p.sizeLimit
		vols = append(vols, corev1.Volume{
			Name: p.volume,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &limit},
			},
		})
	}
	return vols
}

// fileStorageMounts returns the varnish container's mounts for the
// provisioned storages.
func fileStorageMounts(ps []provisionedStorage) []corev1.VolumeMount {
	mounts := make([]corev1.VolumeMount, 0, len(ps))
	for _, p := range ps {
		mounts = append(mounts, corev1.VolumeMount{Name: p.volume, MountPath: p.mountPath})
	}
	return mounts
}

// varnishResources returns the varnish container's resources. Unless the
// user requested ephemeral-storage explicitly, the request covers the
// provisioned volumes, so the scheduler only places the pod on a node with
// enough free disk. No limit is added: each volume's sizeLimit bounds it.
func varnishResources(vc *vinylv1.VinylCache) corev1.ResourceRequirements {
	res := *vc.Spec.Resources.DeepCopy()
	ps := provisionedFileStorages(vc.Spec.Storage)
	if len(ps) == 0 {
		return res
	}
	if _, set := res.Requests[corev1.ResourceEphemeralStorage]; set {
		return res
	}
	total := resource.NewQuantity(0, resource.BinarySI)
	for _, p := range ps {
		total.Add(p.sizeLimit)
	}
	if res.Requests == nil {
		res.Requests = corev1.ResourceList{}
	}
	res.Requests[corev1.ResourceEphemeralStorage] = *total
	return res
}
