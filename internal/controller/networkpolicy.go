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
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	v1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
)

// reconcileNetworkPolicies creates or updates the three NetworkPolicies for a VinylCache.
func (r *VinylCacheReconciler) reconcileNetworkPolicies(ctx context.Context, vc *v1alpha1.VinylCache) error {
	if err := r.reconcileTrafficNetworkPolicy(ctx, vc); err != nil {
		return err
	}
	if err := r.reconcileInvalidationNetworkPolicy(ctx, vc); err != nil {
		return err
	}
	if err := r.reconcileAgentNetworkPolicy(ctx, vc); err != nil {
		return err
	}
	return nil
}

// reconcileTrafficNetworkPolicy allows all ingress to the Varnish HTTP port (8080).
// Varnish is an HTTP cache and must be reachable by Ingress controllers, Services,
// and any upstream client. Cluster peers also need port 8080 for shard routing.
// Port 6082 (admin CLI) is localhost-only and not exposed.
func (r *VinylCacheReconciler) reconcileTrafficNetworkPolicy(ctx context.Context, vc *v1alpha1.VinylCache) error {
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vc.Name + "-traffic",
			Namespace: vc.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, np, func() error {
		if err := ctrl.SetControllerReference(vc, np, r.Scheme); err != nil {
			return err
		}

		np.Labels = map[string]string{labelVinylCacheName: vc.Name}

		httpPort := intstr.FromInt32(varnishPort)
		proto := corev1.ProtocolTCP

		np.Spec = networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{"app": vc.Name},
			},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{
					// Empty From = allow from all sources.
					Ports: []networkingv1.NetworkPolicyPort{
						{Port: &httpPort, Protocol: &proto},
					},
				},
			},
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("reconciling traffic NetworkPolicy: %w", err)
	}
	return nil
}

// reconcileInvalidationNetworkPolicy allows traffic from the operator namespace
// to reach Varnish pods on port 8080 for PURGE/BAN forwarding.
func (r *VinylCacheReconciler) reconcileInvalidationNetworkPolicy(ctx context.Context, vc *v1alpha1.VinylCache) error {
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vc.Name + "-invalidation",
			Namespace: vc.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, np, func() error {
		if err := ctrl.SetControllerReference(vc, np, r.Scheme); err != nil {
			return err
		}

		np.Labels = map[string]string{labelVinylCacheName: vc.Name}

		httpPort := intstr.FromInt32(varnishPort)
		proto := corev1.ProtocolTCP

		np.Spec = networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{"app": vc.Name},
			},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{
					From: []networkingv1.NetworkPolicyPeer{
						{
							// Allow from operator namespace (identified by label).
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"vinyl.bluedynamics.eu/operator-namespace": "true",
								},
							},
						},
					},
					Ports: []networkingv1.NetworkPolicyPort{
						{Port: &httpPort, Protocol: &proto},
					},
				},
			},
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("reconciling invalidation NetworkPolicy: %w", err)
	}
	return nil
}

// reconcileAgentNetworkPolicy allows traffic from the operator namespace to reach
// the vinyl-agent sidecar on port 9090.
func (r *VinylCacheReconciler) reconcileAgentNetworkPolicy(ctx context.Context, vc *v1alpha1.VinylCache) error {
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vc.Name + "-agent",
			Namespace: vc.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, np, func() error {
		if err := ctrl.SetControllerReference(vc, np, r.Scheme); err != nil {
			return err
		}

		np.Labels = map[string]string{labelVinylCacheName: vc.Name}

		agentPortVal := intstr.FromInt32(agentPort)
		proto := corev1.ProtocolTCP

		np.Spec = networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{"app": vc.Name},
			},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{
					From: []networkingv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"vinyl.bluedynamics.eu/operator-namespace": "true",
								},
							},
						},
					},
					Ports: []networkingv1.NetworkPolicyPort{
						{Port: &agentPortVal, Protocol: &proto},
					},
				},
			},
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("reconciling agent NetworkPolicy: %w", err)
	}
	return nil
}
