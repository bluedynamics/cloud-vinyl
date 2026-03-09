//go:build integration

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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	vinylv1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
	"github.com/bluedynamics/cloud-vinyl/internal/generator"
)

// stubGenerator returns a fixed VCL result for integration tests.
type stubGenerator struct{}

func (s *stubGenerator) Generate(_ generator.Input) (*generator.Result, error) {
	return &generator.Result{VCL: "vcl 4.1;", Hash: "testhash"}, nil
}

var _ = Describe("VinylCache Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default", // TODO(user):Modify as needed
		}
		vinylcache := &vinylv1alpha1.VinylCache{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind VinylCache")
			err := k8sClient.Get(ctx, typeNamespacedName, vinylcache)
			if err != nil && errors.IsNotFound(err) {
				resource := &vinylv1alpha1.VinylCache{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: vinylv1alpha1.VinylCacheSpec{
						Replicas: 1,
						Image:    "ghcr.io/bluedynamics/cloud-vinyl-varnish:latest",
						Backends: []vinylv1alpha1.BackendSpec{
							{
								Name:       "backend",
								ServiceRef: vinylv1alpha1.ServiceRef{Name: "test-service"},
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			// TODO(user): Cleanup logic after each test, like removing the resource instance.
			resource := &vinylv1alpha1.VinylCache{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance VinylCache")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})
		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &VinylCacheReconciler{
				Client:      k8sClient,
				Scheme:      k8sClient.Scheme(),
				Generator:   &stubGenerator{},
				AgentClient: &mockAgentClient{},
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			// TODO(user): Add more specific assertions depending on your controller's reconciliation logic.
			// Example: If you expect a certain status condition after reconciliation, verify it here.
		})
	})
})
