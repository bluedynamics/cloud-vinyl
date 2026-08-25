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
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	v1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
)

// labelVinylCacheName is the label key used to identify resources belonging to a VinylCache.
const labelVinylCacheName = "vinyl.bluedynamics.eu/cache-name"

const (
	// labelApp is the label key carrying the VinylCache name. The StatefulSet
	// selector, the Services and the NetworkPolicies all select on it, so they
	// have to agree.
	labelApp = "app"
	// portNameCacheHTTP names the Varnish HTTP port on both the container and
	// the Services that target it.
	portNameCacheHTTP = "cache-http"
	// varnishSecretPath is where the -S shared secret is mounted. It is passed
	// to varnishd as an argument and to the agent via VARNISH_SECRET_FILE.
	varnishSecretPath = "/etc/varnish/secret" //nolint:gosec // Mount path, not a credential
	// volumeVarnishWorkdir is the emptyDir backing /var/lib/varnish.
	volumeVarnishWorkdir = "varnish-workdir"
)

const (
	// exporterPort is the default prometheus_varnish_exporter listen port.
	exporterPort = int32(9131)
	// defaultExporterImage is the default varnish exporter sidecar image.
	defaultExporterImage = "ghcr.io/bluedynamics/varnish-exporter:1.6.1"
)

const (
	// varnishPreStopSleepSeconds is how long the varnish container sleeps in its
	// preStop hook, giving endpoint removal time to propagate before varnishd is
	// signalled so in-flight requests are not cut off.
	varnishPreStopSleepSeconds = 5

	// varnishTerminationGracePeriodSeconds bounds pod shutdown. It must stay above
	// varnishPreStopSleepSeconds and below the Kubernetes default of 30s.
	//
	// The upper bound matters beyond shutdown speed: the namespace controller uses
	// the *declared* grace period, not the observed one, to estimate how long to
	// wait before re-sweeping a terminating namespace, and requeues after
	// estimate/2+1 seconds. Leaving this at the 30s default produced a 16s requeue
	// quantum and made namespace teardown land at ~42s, overshooting the 30s
	// Chainsaw cleanup timeout in E2E (see issue #63).
	varnishTerminationGracePeriodSeconds = 15
)

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
			labelApp:            vc.Name,
			labelVinylCacheName: vc.Name,
		}
		// Merge user-defined pod labels.
		maps.Copy(podLabels, vc.Spec.Pod.Labels)

		// Build Varnish container.
		// The stock varnish image entrypoint passes extra args to varnishd.
		// We need -T (admin CLI) and -S (shared secret) for the agent sidecar.
		varnishArgs := []string{
			"-j", "none",
			"-T", "127.0.0.1:6082",
			"-S", varnishSecretPath,
		}
		// Append -s args for each spec.storage entry (after the fixed args).
		varnishArgs = append(varnishArgs, storageArgs(vc.Spec.Storage)...)

		varnishContainer := corev1.Container{
			Name:  "varnish",
			Image: vc.Spec.Image,
			Args:  varnishArgs,
			Env: []corev1.EnvVar{
				// Override the stock varnish image default port (80) to match
				// the containerPort and Service definitions.
				{Name: "VARNISH_HTTP_PORT", Value: fmt.Sprintf("%d", varnishPort)},
			},
			Ports: []corev1.ContainerPort{
				{Name: portNameCacheHTTP, ContainerPort: varnishPort, Protocol: corev1.ProtocolTCP},
			},
			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      agentTokenKey,
					MountPath: "/run/vinyl",
					ReadOnly:  true,
				},
				{
					Name:      varnishSecretKey,
					MountPath: varnishSecretPath,
					SubPath:   varnishSecretKey,
					ReadOnly:  true,
				},
				{
					Name:      volumeVarnishWorkdir,
					MountPath: "/var/lib/varnish",
				},
				{
					Name:      "varnish-tmp",
					MountPath: "/tmp",
				},
				{
					Name:      "bootstrap-vcl",
					MountPath: "/etc/varnish/default.vcl",
					SubPath:   "default.vcl",
					ReadOnly:  true,
				},
			},
			Lifecycle: &corev1.Lifecycle{
				PreStop: &corev1.LifecycleHandler{
					Exec: &corev1.ExecAction{
						Command: []string{"sleep", strconv.Itoa(varnishPreStopSleepSeconds)},
					},
				},
			},
			SecurityContext: &corev1.SecurityContext{
				RunAsNonRoot:             new(true),
				ReadOnlyRootFilesystem:   new(true),
				AllowPrivilegeEscalation: new(false),
			},
			Resources: vc.Spec.Resources,
		}

		// Append user-declared volume mounts to the varnish container.
		if len(vc.Spec.Pod.VolumeMounts) > 0 {
			varnishContainer.VolumeMounts = append(varnishContainer.VolumeMounts, vc.Spec.Pod.VolumeMounts...)
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
				{Name: "VARNISH_SECRET_FILE", Value: varnishSecretPath},
				{Name: "AGENT_TOKEN_FILE", Value: "/run/vinyl/agent-token"},
			},
			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      agentTokenKey,
					MountPath: "/run/vinyl",
					ReadOnly:  true,
				},
				{
					Name:      varnishSecretKey,
					MountPath: varnishSecretPath,
					SubPath:   varnishSecretKey,
					ReadOnly:  true,
				},
			},
			ReadinessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Path: "/health",
						Port: intstr.FromInt32(agentPort),
					},
				},
				InitialDelaySeconds: 5,
				PeriodSeconds:       5,
				FailureThreshold:    6,
			},
			SecurityContext: &corev1.SecurityContext{
				RunAsNonRoot:             new(true),
				ReadOnlyRootFilesystem:   new(true),
				AllowPrivilegeEscalation: new(false),
			},
		}

		volumes := []corev1.Volume{
			{
				Name: agentTokenKey,
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: agentSecretName,
						Items: []corev1.KeyToPath{
							{Key: agentTokenKey, Path: agentTokenKey},
						},
					},
				},
			},
			{
				Name: varnishSecretKey,
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: agentSecretName,
						Items: []corev1.KeyToPath{
							{Key: varnishSecretKey, Path: varnishSecretKey},
						},
					},
				},
			},
			{
				Name:         volumeVarnishWorkdir,
				VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
			},
			{
				Name:         "varnish-tmp",
				VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
			},
			{
				Name: "bootstrap-vcl",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: vc.Name + "-bootstrap-vcl",
						},
					},
				},
			},
		}

		// Append user-declared pod volumes after the operator-managed defaults.
		if len(vc.Spec.Pod.Volumes) > 0 {
			volumes = append(volumes, vc.Spec.Pod.Volumes...)
		}

		containers := []corev1.Container{varnishContainer, agentContainer}
		if exp := vc.Spec.Monitoring.Exporter; exp != nil && exp.Enabled {
			containers = append(containers, buildExporterContainer(exp))
		}

		uid := int64(65532)
		grace := int64(varnishTerminationGracePeriodSeconds)
		podSpec := corev1.PodSpec{
			Containers:                    containers,
			Volumes:                       volumes,
			NodeSelector:                  vc.Spec.Pod.NodeSelector,
			Tolerations:                   vc.Spec.Pod.Tolerations,
			Affinity:                      vc.Spec.Pod.Affinity,
			PriorityClassName:             vc.Spec.Pod.PriorityClass,
			TerminationGracePeriodSeconds: &grace,
			SecurityContext: &corev1.PodSecurityContext{
				RunAsUser:  &uid,
				RunAsGroup: &uid,
				FSGroup:    &uid,
			},
		}

		sts.Spec = appsv1.StatefulSetSpec{
			Replicas:            &replicas,
			PodManagementPolicy: parallel,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{labelApp: vc.Name},
			},
			ServiceName: vc.Name,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      podLabels,
					Annotations: vc.Spec.Pod.Annotations,
				},
				Spec: podSpec,
			},
			VolumeClaimTemplates: vc.Spec.VolumeClaimTemplates,
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("reconciling StatefulSet: %w", err)
	}
	return nil
}

// buildExporterContainer returns the prometheus_varnish_exporter sidecar. It
// shares the varnish-workdir volume read-only to read the VSM (varnishstat) data.
func buildExporterContainer(exp *v1alpha1.ExporterSpec) corev1.Container {
	image := defaultExporterImage
	if exp.Image.Repository != "" {
		tag := exp.Image.Tag
		if tag == "" {
			tag = "latest"
		}
		image = exp.Image.Repository + ":" + tag
	}
	port := exporterPort
	if exp.Port != 0 {
		port = exp.Port
	}
	return corev1.Container{
		Name:  "vinyl-exporter",
		Image: image,
		Ports: []corev1.ContainerPort{
			{Name: "exporter", ContainerPort: port, Protocol: corev1.ProtocolTCP},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: volumeVarnishWorkdir, MountPath: "/var/lib/varnish", ReadOnly: true},
		},
		SecurityContext: &corev1.SecurityContext{
			RunAsNonRoot:             new(true),
			ReadOnlyRootFilesystem:   new(true),
			AllowPrivilegeEscalation: new(false),
		},
		Resources: exp.Resources,
	}
}

// storageArgs renders spec.storage entries as varnishd "-s <name>=<type>,..."
// command-line args. See https://varnish-cache.org/docs/8.0/reference/varnishd.html#argument-list
// for the syntax accepted by -s.
func storageArgs(storage []v1alpha1.StorageSpec) []string {
	if len(storage) == 0 {
		return nil
	}
	args := make([]string, 0, 2*len(storage))
	for _, s := range storage {
		var spec string
		switch s.Type {
		case "malloc":
			// -s <name>=malloc,<size>
			spec = fmt.Sprintf("%s=malloc,%d", s.Name, s.Size.Value())
		case "file":
			// -s <name>=file,<path>,<size>
			spec = fmt.Sprintf("%s=file,%s,%d", s.Name, s.Path, s.Size.Value())
		default:
			continue // webhook already rejects other types; belt-and-braces skip
		}
		args = append(args, "-s", spec)
	}
	return args
}
