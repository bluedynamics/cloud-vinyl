//go:build integration

package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	autoscalingv1 "k8s.io/api/autoscaling/v1"

	vinylv1 "github.com/bluedynamics/cloud-vinyl/api/v1"
)

var _ = Describe("spec.replicas validation", func() {
	newCache := func(name string, replicas int32) *vinylv1.VinylCache {
		return &vinylv1.VinylCache{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: vinylv1.VinylCacheSpec{
				Replicas: replicas,
				Backends: []vinylv1.BackendSpec{{
					Name: "echo", ServiceRef: vinylv1.ServiceRef{Name: "echo"}, Port: 80,
				}},
			},
		}
	}

	It("accepts replicas: 0 and a scale to 0 through the scale subresource", func() {
		vc := newCache("zero-replicas", 0)
		Expect(k8sClient.Create(ctx, vc)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, vc) })

		one := newCache("scale-to-zero", 1)
		Expect(k8sClient.Create(ctx, one)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, one) })

		scale := &autoscalingv1.Scale{
			ObjectMeta: metav1.ObjectMeta{Name: one.Name, Namespace: one.Namespace},
			Spec:       autoscalingv1.ScaleSpec{Replicas: 0},
		}
		Expect(k8sClient.SubResource("scale").Update(ctx, one, client.WithSubResourceBody(scale))).To(Succeed())

		got := &vinylv1.VinylCache{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: one.Name, Namespace: one.Namespace}, got)).To(Succeed())
		Expect(got.Spec.Replicas).To(Equal(int32(0)))
	})

	// The scale subresource reads status.replicas and status.selector, the
	// fields setReplicaStatus fills (#180). A real API server proves the paths.
	It("reports status.replicas and status.selector through the scale subresource", func() {
		vc := newCache("scale-status", 2)
		Expect(k8sClient.Create(ctx, vc)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, vc) })

		vc.Status.Replicas = 2
		vc.Status.Selector = "app=scale-status"
		Expect(k8sClient.Status().Update(ctx, vc)).To(Succeed())

		scale := &autoscalingv1.Scale{}
		Expect(k8sClient.SubResource("scale").Get(ctx, vc, scale)).To(Succeed())
		Expect(scale.Spec.Replicas).To(Equal(int32(2)))
		Expect(scale.Status.Replicas).To(Equal(int32(2)))
		Expect(scale.Status.Selector).To(Equal("app=scale-status"))
	})

	It("still rejects a negative replica count", func() {
		err := k8sClient.Create(ctx, newCache("negative-replicas", -1))
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "got %v", err)
	})
})
