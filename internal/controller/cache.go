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
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// labelManagedBy marks the per-namespace agent Secret. That Secret is
	// shared by every VinylCache in the namespace, so it cannot carry
	// labelVinylCacheName.
	labelManagedBy = "app.kubernetes.io/managed-by"
	managedByValue = "cloud-vinyl"
)

// CacheOptions scopes the manager's informer cache to the objects the
// operator works with (#145). With controller-runtime's defaults every type
// the operator reads gets a cluster-wide informer, so the operator held every
// Pod, Secret, ConfigMap, Service, StatefulSet and NetworkPolicy of the
// cluster in memory.
//
// Every type the operator creates is cached only if it carries the label the
// operator sets on it. A cached Get of an object outside its selector returns
// NotFound, so every object the operator reads must keep carrying that label;
// all of them have since 1.0.0.
//
// EndpointSlices are not label-scoped: the operator reads the slices of the
// users' backend Services, which do not carry our labels. watchNamespaces,
// when not empty, restricts every informer, these included, to the listed
// namespaces.
//
// managedFields are stripped from every cached object; the operator never
// reads them. Nothing else is stripped: the operator updates objects it read
// from the cache, so a stripped field would be wiped on the server.
func CacheOptions(watchNamespaces []string) cache.Options {
	owned := cache.ByObject{Label: labelExists(labelVinylCacheName)}
	opts := cache.Options{
		DefaultTransform: cache.TransformStripManagedFields(),
		ByObject: map[client.Object]cache.ByObject{
			&appsv1.StatefulSet{}:         owned,
			&corev1.Service{}:             owned,
			&corev1.ConfigMap{}:           owned,
			&corev1.Pod{}:                 owned,
			&networkingv1.NetworkPolicy{}: owned,
			&corev1.Secret{}: {
				Label: labels.SelectorFromSet(labels.Set{labelManagedBy: managedByValue}),
			},
		},
	}
	if len(watchNamespaces) > 0 {
		opts.DefaultNamespaces = make(map[string]cache.Config, len(watchNamespaces))
		for _, ns := range watchNamespaces {
			opts.DefaultNamespaces[ns] = cache.Config{}
		}
	}
	return opts
}

// labelExists selects objects that carry key, whatever its value.
func labelExists(key string) labels.Selector {
	req, err := labels.NewRequirement(key, selection.Exists, nil)
	if err != nil {
		panic(err) // key is a compile-time constant
	}
	return labels.NewSelector().Add(*req)
}

// SplitNamespaces parses the --watch-namespaces flag: a comma-separated list,
// blanks around names and empty entries ignored.
func SplitNamespaces(s string) []string {
	var out []string
	for ns := range strings.SplitSeq(s, ",") {
		if ns = strings.TrimSpace(ns); ns != "" {
			out = append(out, ns)
		}
	}
	return out
}
