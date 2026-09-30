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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	vinylv1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
)

// minimalAdmissibleVC returns a VinylCache that passes CRD schema and
// webhook validation with tracing left off. Mirror the shape of
// internal/webhook's minimalValidVC unit fixture (test helpers cannot be
// imported across packages) — copy its required spec fields exactly; if
// the two drift, the admit test fails loudly, which is the point.
func minimalAdmissibleVC(name string) *vinylv1alpha1.VinylCache {
	return &vinylv1alpha1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: vinylv1alpha1.VinylCacheSpec{
			Replicas: 1,
			Image:    "ghcr.io/bluedynamics/cloud-vinyl-varnish:8.0.2",
			Backends: []vinylv1alpha1.BackendSpec{
				{
					Name:       "app",
					ServiceRef: vinylv1alpha1.ServiceRef{Name: "app-service"},
				},
			},
		},
	}
}

var _ = Describe("VinylCache Webhook", func() {
	var (
		obj       *vinylv1alpha1.VinylCache
		oldObj    *vinylv1alpha1.VinylCache
		validator VinylCacheCustomValidator
		defaulter VinylCacheCustomDefaulter
	)

	BeforeEach(func() {
		obj = &vinylv1alpha1.VinylCache{}
		oldObj = &vinylv1alpha1.VinylCache{}
		validator = VinylCacheCustomValidator{}
		Expect(validator).NotTo(BeNil(), "Expected validator to be initialized")
		defaulter = VinylCacheCustomDefaulter{}
		Expect(defaulter).NotTo(BeNil(), "Expected defaulter to be initialized")
		Expect(oldObj).NotTo(BeNil(), "Expected oldObj to be initialized")
		Expect(obj).NotTo(BeNil(), "Expected obj to be initialized")
	})

	AfterEach(func() {
		// TODO (user): Add any teardown logic common to all tests
	})

	Context("When creating VinylCache under Defaulting Webhook", func() {
		// TODO (user): Add logic for defaulting webhooks
		// Example:
		// It("Should apply defaults when a required field is empty", func() {
		//     By("simulating a scenario where defaults should be applied")
		//     obj.SomeFieldWithDefault = ""
		//     By("calling the Default method to apply defaults")
		//     defaulter.Default(ctx, obj)
		//     By("checking that the default values are set")
		//     Expect(obj.SomeFieldWithDefault).To(Equal("default_value"))
		// })
	})

	Context("When creating or updating VinylCache under Validating Webhook", func() {
		It("denies tracing enabled without an OTLP endpoint", func() {
			vc := minimalAdmissibleVC("traced-invalid")
			vc.Spec.Tracing = vinylv1alpha1.TracingSpec{Enabled: true}
			err := k8sClient.Create(ctx, vc)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("tracing.otlp.endpoint is required"))
		})

		It("admits tracing with a host:port endpoint", func() {
			vc := minimalAdmissibleVC("traced-valid")
			vc.Spec.Tracing = vinylv1alpha1.TracingSpec{
				Enabled: true,
				OTLP:    vinylv1alpha1.OTLPSpec{Endpoint: "collector:4317"},
			}
			Expect(k8sClient.Create(ctx, vc)).To(Succeed())
			DeferCleanup(func() {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, vc))).To(Succeed())
			})
		})
	})

})
