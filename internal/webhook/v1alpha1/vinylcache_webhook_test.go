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

package v1alpha1

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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
			Image:    "ghcr.io/bluedynamics/cloud-vinyl-varnish:9.1.0",
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
		// The StatefulSet's volumeClaimTemplates are immutable, so the webhook
		// has to compare against the stored object (#46). Only a real update
		// proves that ValidateUpdate gets the old object at all.
		It("rejects an edit to spec.volumeClaimTemplates but admits other updates", func() {
			vc := minimalAdmissibleVC("vct-immutable")
			vc.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{Name: "cache-ssd"},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")},
					},
				},
			}}
			Expect(k8sClient.Create(ctx, vc)).To(Succeed())
			DeferCleanup(func() {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, vc))).To(Succeed())
			})

			vc.Spec.Replicas = 2
			Expect(k8sClient.Update(ctx, vc)).To(Succeed(), "an update that leaves the templates alone is fine")

			vc.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests[corev1.ResourceStorage] = resource.MustParse("20Gi")
			err := k8sClient.Update(ctx, vc)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("admission webhook"))
			Expect(err.Error()).To(ContainSubstring("spec.volumeClaimTemplates is immutable"))
		})

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

		// The CRD carries a CEL x-kubernetes-validations rule on
		// spec.varnishParameters (api/v1alpha1/vinylcache_types.go) as a
		// backstop independent of the admission webhook: the webhook's
		// failurePolicy=fail only protects against the webhook being down,
		// not against it never having been installed. This test proves the
		// CEL rule actually fires on its own, not merely that *some* layer
		// rejects the key (both layers reject it here, since this envtest
		// suite runs with the webhook installed too).
		//
		// It distinguishes the layers by error shape, confirmed empirically
		// against this suite's real envtest output before writing these
		// assertions (not assumed from Kubernetes docs): CRD schema/CEL
		// validation runs inside the API server's object-validation step,
		// which executes before the validating admission webhook chain is
		// even invoked. So for a key that only a CEL violation (not the
		// webhook's blocklist) would catch, the observed rejection carries
		// the CEL rule's own `message:` text and never reaches
		// ValidateVinylCache at all — the error string contains no
		// "admission webhook ... denied the request" wrapper, which is the
		// distinct phrasing controller-runtime/the API server adds only for
		// webhook denials (see the sibling test below, which triggers an
		// actual webhook denial for contrast).
		It("rejects a shape-invalid varnishParameters key via the CRD's CEL rule, without reaching the webhook", func() {
			vc := minimalAdmissibleVC("cel-key-shape-invalid")
			vc.Spec.VarnishParams = map[string]string{"THREAD_POOL_MIN": "100"}
			err := k8sClient.Create(ctx, vc)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(
				"varnishParameters keys must be bare varnishd parameter names (lowercase letters, digits, underscore)"),
				"must carry the CEL rule's own message")
			Expect(err.Error()).NotTo(ContainSubstring("admission webhook"),
				"must not carry the webhook-denial wrapper: the CEL rule at the CRD "+
					"schema layer must reject this before ValidateVinylCache ever runs")
		})

		// Contrast case for the test above: a key that is CEL-shape-valid
		// (lowercase, no padding) but on the webhook's blocklist. This one
		// can only be caught by ValidateVinylCache, so its rejection is
		// expected to carry the webhook-denial wrapper — proving the two
		// layers are independent, not that the CEL rule now does everything.
		It("still rejects a shape-valid but blocklisted key via the webhook", func() {
			vc := minimalAdmissibleVC("webhook-blocklist")
			vc.Spec.VarnishParams = map[string]string{"cc_command": "gcc"}
			err := k8sClient.Create(ctx, vc)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("admission webhook"),
				"a blocklisted-but-shape-valid key can only be caught by the webhook")
			Expect(err.Error()).To(ContainSubstring(`varnishParameters key "cc_command" is not allowed`))
		})

		// A retry on a hash or fallback director would pick the same backend
		// again, so the CRD rejects an explicit maxRetries > 0 for them (#117).
		It("rejects retry.maxRetries > 0 for a fallback director via CEL", func() {
			vc := minimalAdmissibleVC("cel-retry-fallback")
			vc.Spec.Backends[0].Director = &vinylv1alpha1.DirectorSpec{Type: "fallback"}
			vc.Spec.Backends[0].Retry = &vinylv1alpha1.FetchRetrySpec{MaxRetries: new(int32(1))}
			err := k8sClient.Create(ctx, vc)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("retry.maxRetries must be 0 for hash and fallback directors"))
		})

		It("admits retry.maxRetries 0 for a hash director", func() {
			vc := minimalAdmissibleVC("retry-hash-off")
			vc.Spec.Backends[0].Director = &vinylv1alpha1.DirectorSpec{Type: "hash"}
			vc.Spec.Backends[0].Retry = &vinylv1alpha1.FetchRetrySpec{MaxRetries: new(int32(0))}
			Expect(k8sClient.Create(ctx, vc)).To(Succeed())
			DeferCleanup(func() {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, vc))).To(Succeed())
			})
		})

		It("admits a MALLOC_CONF override in spec.pod.env", func() {
			vc := minimalAdmissibleVC("env-malloc-conf")
			vc.Spec.Pod.Env = []corev1.EnvVar{{Name: "MALLOC_CONF", Value: ""}}
			Expect(k8sClient.Create(ctx, vc)).To(Succeed())
			DeferCleanup(func() {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, vc))).To(Succeed())
			})
		})
	})

})
