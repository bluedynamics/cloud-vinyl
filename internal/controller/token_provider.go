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
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// K8sTokenProvider reads agent tokens from Kubernetes Secrets.
// It implements proxy.TokenProvider.
type K8sTokenProvider struct {
	client client.Reader
}

// NewK8sTokenProvider creates a new K8sTokenProvider.
func NewK8sTokenProvider(c client.Reader) *K8sTokenProvider {
	return &K8sTokenProvider{client: c}
}

// GetToken reads the agent-token from the per-namespace Secret.
// Returns empty string on error (unauthenticated fallback).
func (p *K8sTokenProvider) GetToken(namespace string) string {
	log := logf.Log.WithName("token-provider")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	secret := &corev1.Secret{}
	key := types.NamespacedName{Name: agentSecretName, Namespace: namespace}
	if err := p.client.Get(ctx, key, secret); err != nil {
		log.Error(err, "Failed to read agent secret", "namespace", namespace)
		return ""
	}

	token, ok := secret.Data["agent-token"]
	if !ok {
		log.Info("Agent secret missing 'agent-token' key", "namespace", namespace)
		return ""
	}
	return string(token)
}
