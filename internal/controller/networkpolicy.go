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
	"net"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	v1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
)

// reconcileNetworkPolicies creates or updates the NetworkPolicies for a VinylCache.
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
	if err := r.reconcileExporterNetworkPolicy(ctx, vc); err != nil {
		return err
	}
	return nil
}

// operatorNamespaceLabel marks the namespace the operator runs in. It is the
// legacy way of authorizing the operator against the agent and invalidation
// policies and has to be applied by hand, so it is only a fallback now.
const operatorNamespaceLabel = "vinyl.bluedynamics.eu/operator-namespace"

// operatorPeers returns the NetworkPolicy peers that identify this operator.
//
// The operator's own pod IP comes first: it needs no cluster-wide labeling and
// therefore works on a stock `helm install`. The namespace label is kept as a
// fallback for setups that label their namespace and for operator replicas whose
// POD_IP is not injected (see issue #58).
func (r *VinylCacheReconciler) operatorPeers() []networkingv1.NetworkPolicyPeer {
	peers := []networkingv1.NetworkPolicyPeer{
		{
			NamespaceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{operatorNamespaceLabel: "true"},
			},
		},
	}
	if cidr := singleHostCIDR(r.OperatorIP); cidr != "" {
		peers = append(peers, networkingv1.NetworkPolicyPeer{
			IPBlock: &networkingv1.IPBlock{CIDR: cidr},
		})
	}
	return peers
}

// singleHostCIDR turns an IP address into a host CIDR ("10.0.0.1/32",
// "fd00::1/128"). It returns "" for empty or unparseable input, so a missing
// POD_IP degrades to the label fallback instead of producing an invalid policy.
func singleHostCIDR(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ""
	}
	if parsed.To4() != nil {
		return parsed.String() + "/32"
	}
	return parsed.String() + "/128"
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
				MatchLabels: map[string]string{labelApp: vc.Name},
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

// reconcileInvalidationNetworkPolicy allows the operator to reach Varnish pods
// on port 8080 for PURGE/BAN forwarding. See operatorPeers for how the operator
// is identified.
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
				MatchLabels: map[string]string{labelApp: vc.Name},
			},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{
					From: r.operatorPeers(),
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

// reconcileAgentNetworkPolicy allows the operator to reach the vinyl-agent
// sidecar on port 9090, which is how VCL is pushed. See operatorPeers for how the
// operator is identified.
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
				MatchLabels: map[string]string{labelApp: vc.Name},
			},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{
					From: r.operatorPeers(),
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

// reconcileExporterNetworkPolicy allows ingress to the varnish-exporter metrics
// port so Prometheus can scrape it. The operator cannot know which namespace
// Prometheus runs in, and the exposed data is read-only, low-sensitivity Varnish
// metrics, so ingress is allowed from all sources — consistent with the
// always-open Varnish HTTP port. The policy exists only while the exporter
// sidecar is enabled; when disabled, a stale policy is removed.
func (r *VinylCacheReconciler) reconcileExporterNetworkPolicy(ctx context.Context, vc *v1alpha1.VinylCache) error {
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vc.Name + "-exporter",
			Namespace: vc.Namespace,
		},
	}

	exp := vc.Spec.Monitoring.Exporter
	if exp == nil || !exp.Enabled {
		// Exporter disabled: remove a previously created policy if present.
		if err := r.Delete(ctx, np); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting exporter NetworkPolicy: %w", err)
		}
		return nil
	}

	port := exporterPort
	if exp.Port != 0 {
		port = exp.Port
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, np, func() error {
		if err := ctrl.SetControllerReference(vc, np, r.Scheme); err != nil {
			return err
		}

		np.Labels = map[string]string{labelVinylCacheName: vc.Name}

		exporterPortVal := intstr.FromInt32(port)
		proto := corev1.ProtocolTCP

		np.Spec = networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{labelApp: vc.Name},
			},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{
					// Empty From = allow from all sources, so Prometheus can scrape
					// regardless of its namespace. Exporter metrics are read-only.
					Ports: []networkingv1.NetworkPolicyPort{
						{Port: &exporterPortVal, Protocol: &proto},
					},
				},
			},
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("reconciling exporter NetworkPolicy: %w", err)
	}
	return nil
}
