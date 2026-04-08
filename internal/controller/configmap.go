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
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	v1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
)

const bootstrapVCL = `vcl 4.1;

backend bootstrap_placeholder {
    .host = "127.0.0.1";
    .port = "1";
}

sub vcl_recv {
    return (synth(503, "Cache initializing — waiting for VCL push from cloud-vinyl operator"));
}

sub vcl_synth {
    set resp.http.Content-Type = "text/plain; charset=utf-8";
    set resp.http.Retry-After = "5";
    synthetic(resp.reason);
    return (deliver);
}
`

// reconcileConfigMap creates or updates the ConfigMap containing the bootstrap VCL.
func (r *VinylCacheReconciler) reconcileConfigMap(ctx context.Context, vc *v1alpha1.VinylCache) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vc.Name + "-bootstrap-vcl",
			Namespace: vc.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
		if err := ctrl.SetControllerReference(vc, cm, r.Scheme); err != nil {
			return err
		}
		cm.Labels = map[string]string{labelVinylCacheName: vc.Name}
		cm.Data = map[string]string{
			"default.vcl": bootstrapVCL,
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("reconciling bootstrap VCL ConfigMap: %w", err)
	}
	return nil
}
