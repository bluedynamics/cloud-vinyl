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
	"maps"
	"os"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	v1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
)

// labelVinylCacheName is the label key used to identify resources belonging to a VinylCache.
const labelVinylCacheName = "vinyl.bluedynamics.eu/cache-name"

// reconcileStatefulSet creates or updates the StatefulSet for the VinylCache.
func (r *VinylCacheReconciler) reconcileStatefulSet(ctx context.Context, vc *v1alpha1.VinylCache) error {
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vc.Name,
			Namespace: vc.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, sts, func() error {
		if err := ctrl.SetControllerReference(vc, sts, r.Scheme); err != nil {
			return err
		}

		parallel := appsv1.ParallelPodManagement
		replicas := vc.Spec.Replicas

		podLabels := map[string]string{
			"app":               vc.Name,
			labelVinylCacheName: vc.Name,
		}
		// Merge user-defined pod labels.
		maps.Copy(podLabels, vc.Spec.Pod.Labels)

		// Build Varnish container.
		varnishContainer := corev1.Container{
			Name:  "varnish",
			Image: vc.Spec.Image,
			Ports: []corev1.ContainerPort{
				{Name: "http", ContainerPort: varnishPort, Protocol: corev1.ProtocolTCP},
			},
			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      "agent-token",
					MountPath: "/run/vinyl",
					ReadOnly:  true,
				},
			},
			SecurityContext: &corev1.SecurityContext{
				RunAsNonRoot:             boolPtr(true),
				ReadOnlyRootFilesystem:   boolPtr(true),
				AllowPrivilegeEscalation: boolPtr(false),
			},
			Resources: vc.Spec.Resources,
		}

		// Add proxy protocol port if enabled.
		if vc.Spec.ProxyProtocol.Enabled {
			ppPort := int32(8081)
			if vc.Spec.ProxyProtocol.Port != 0 {
				ppPort = vc.Spec.ProxyProtocol.Port
			}
			varnishContainer.Ports = append(varnishContainer.Ports, corev1.ContainerPort{
				Name:          "proxy",
				ContainerPort: ppPort,
				Protocol:      corev1.ProtocolTCP,
			})
		}

		// Build agent sidecar container.
		agentSecretName := "vinyl-agent-" + vc.Name
		// Agent image: use AGENT_IMAGE env var (set by Helm chart from operator image),
		// falling back to the varnish image for backward compatibility.
		agentImage := os.Getenv("AGENT_IMAGE")
		if agentImage == "" {
			agentImage = vc.Spec.Image
		}
		agentContainer := corev1.Container{
			Name:  "vinyl-agent",
			Image: agentImage,
			Ports: []corev1.ContainerPort{
				{Name: "agent", ContainerPort: agentPort, Protocol: corev1.ProtocolTCP},
			},
			Env: []corev1.EnvVar{
				{Name: "VARNISH_SECRET_FILE", Value: "/etc/varnish/secret"},
				{Name: "AGENT_TOKEN_FILE", Value: "/run/vinyl/agent-token"},
			},
			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      "agent-token",
					MountPath: "/run/vinyl",
					ReadOnly:  true,
				},
			},
			SecurityContext: &corev1.SecurityContext{
				RunAsNonRoot:             boolPtr(true),
				ReadOnlyRootFilesystem:   boolPtr(true),
				AllowPrivilegeEscalation: boolPtr(false),
			},
		}

		volumes := []corev1.Volume{
			{
				Name: "agent-token",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: agentSecretName,
						Items: []corev1.KeyToPath{
							{Key: "agent-token", Path: "agent-token"},
						},
					},
				},
			},
		}

		podSpec := corev1.PodSpec{
			Containers:        []corev1.Container{varnishContainer, agentContainer},
			Volumes:           volumes,
			NodeSelector:      vc.Spec.Pod.NodeSelector,
			Tolerations:       vc.Spec.Pod.Tolerations,
			Affinity:          vc.Spec.Pod.Affinity,
			PriorityClassName: vc.Spec.Pod.PriorityClass,
		}

		sts.Spec = appsv1.StatefulSetSpec{
			Replicas:            &replicas,
			PodManagementPolicy: parallel,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": vc.Name},
			},
			ServiceName: vc.Name,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      podLabels,
					Annotations: vc.Spec.Pod.Annotations,
				},
				Spec: podSpec,
			},
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("reconciling StatefulSet: %w", err)
	}
	return nil
}

// boolPtr returns a pointer to a bool value.
func boolPtr(b bool) *bool {
	return &b
}
