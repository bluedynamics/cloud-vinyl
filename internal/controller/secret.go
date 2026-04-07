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
	"crypto/rand"
	"encoding/hex"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	v1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
)

// reconcileSecret creates (if absent) or preserves the agent-token Secret.
// The token is never rotated — idempotent by design.
func (r *VinylCacheReconciler) reconcileSecret(ctx context.Context, vc *v1alpha1.VinylCache) error {
	secretName := "vinyl-agent-" + vc.Name

	existing := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: vc.Namespace}, existing)
	if err == nil {
		// Secret already exists — do not rotate the token.
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("getting agent Secret: %w", err)
	}

	// Generate random tokens: one for agent auth, one for varnish admin CLI.
	agentRaw := make([]byte, 32)
	if _, err := rand.Read(agentRaw); err != nil {
		return fmt.Errorf("generating agent token: %w", err)
	}
	varnishRaw := make([]byte, 32)
	if _, err := rand.Read(varnishRaw); err != nil {
		return fmt.Errorf("generating varnish secret: %w", err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: vc.Namespace,
			Labels: map[string]string{
				labelVinylCacheName: vc.Name,
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"agent-token":    []byte(hex.EncodeToString(agentRaw)),
			"varnish-secret": []byte(hex.EncodeToString(varnishRaw)),
		},
	}
	if err := ctrl.SetControllerReference(vc, secret, r.Scheme); err != nil {
		return fmt.Errorf("setting OwnerReference on Secret: %w", err)
	}

	if err := r.Create(ctx, secret); err != nil {
		return fmt.Errorf("creating agent Secret: %w", err)
	}
	return nil
}
