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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	vinylv1 "github.com/bluedynamics/cloud-vinyl/api/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:deprecatedversion:warning="vinyl.bluedynamics.eu/v1alpha1 VinylCache is deprecated; use vinyl.bluedynamics.eu/v1"
// +kubebuilder:subresource:status
// +kubebuilder:subresource:scale:specpath=.spec.replicas,statuspath=.status.replicas,selectorpath=.status.selector
// +kubebuilder:resource:shortName=vc,categories=vinyl
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Replicas",type="string",JSONPath=".status.readyReplicas"
// +kubebuilder:printcolumn:name="VCL",type="string",JSONPath=".status.conditions[?(@.type=='VCLSynced')].status"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase",priority=1
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// VinylCache is the Schema for the vinylcaches API.
// It describes a complete Varnish cache cluster: replicas, backends, VCL configuration,
// clustering, and cache invalidation strategy.
type VinylCache struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is standard Kubernetes object metadata.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired state of the VinylCache cluster.
	// +optional
	Spec vinylv1.VinylCacheSpec `json:"spec,omitempty"`

	// status defines the observed state of the VinylCache cluster.
	// +optional
	Status vinylv1.VinylCacheStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// VinylCacheList contains a list of VinylCache.
type VinylCacheList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VinylCache `json:"items"`
}
