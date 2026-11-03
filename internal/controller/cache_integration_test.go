//go:build integration

package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The operator's informer cache only holds objects it works with (#145).
// These specs run CacheOptions against a real API server: objects outside
// the selectors or outside the watched namespaces must never reach the cache.
var _ = Describe("Scoped informer cache", func() {
	const watched, other = "cache-scope-watched", "cache-scope-other"

	startCache := func(watchNamespaces []string) (cache.Cache, context.CancelFunc) {
		opts := CacheOptions(watchNamespaces)
		opts.Scheme = scheme.Scheme
		c, err := cache.New(cfg, opts)
		Expect(err).NotTo(HaveOccurred())
		cctx, ccancel := context.WithCancel(ctx)
		go func() { _ = c.Start(cctx) }()
		Expect(c.WaitForCacheSync(cctx)).To(BeTrue())
		return c, ccancel
	}

	create := func(obj client.Object) {
		Expect(k8sClient.Create(ctx, obj)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, obj) })
	}

	configMap := func(ns, name string, lbls map[string]string) *corev1.ConfigMap {
		return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: lbls}}
	}

	// visible reports whether the cache eventually serves obj, or keeps
	// answering NotFound for a while.
	visible := func(c cache.Cache, obj client.Object) bool {
		key := client.ObjectKeyFromObject(obj)
		into := obj.DeepCopyObject().(client.Object)
		found := false
		Consistently(func() error {
			err := c.Get(ctx, key, into)
			if err == nil {
				found = true
				return nil
			}
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}, 2*time.Second, 200*time.Millisecond).Should(Succeed())
		return found
	}

	BeforeEach(func() {
		for _, ns := range []string{watched, other} {
			n := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
			err := k8sClient.Create(ctx, n)
			if err != nil && !apierrors.IsAlreadyExists(err) {
				Expect(err).NotTo(HaveOccurred())
			}
		}
	})

	It("caches owned types only when they carry the cache-name label", func() {
		ours := configMap(watched, "ours", map[string]string{labelVinylCacheName: "www"})
		foreign := configMap(watched, "foreign", map[string]string{"app": "www"})
		create(ours)
		create(foreign)

		c, stop := startCache(nil)
		defer stop()

		Expect(visible(c, ours)).To(BeTrue())
		Expect(visible(c, foreign)).To(BeFalse())
	})

	It("caches the shared agent Secret by its managed-by label", func() {
		agent := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Namespace: watched, Name: agentSecretName,
			Labels: map[string]string{labelManagedBy: managedByValue},
		}}
		foreign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: watched, Name: "db-password"}}
		create(agent)
		create(foreign)

		c, stop := startCache(nil)
		defer stop()

		Expect(visible(c, agent)).To(BeTrue())
		Expect(visible(c, foreign)).To(BeFalse())
	})

	It("caches unlabelled EndpointSlices of backend Services", func() {
		slice := &discoveryv1.EndpointSlice{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: watched, Name: "plone-abcde",
				Labels: map[string]string{discoveryv1.LabelServiceName: "plone"},
			},
			AddressType: discoveryv1.AddressTypeIPv4,
		}
		create(slice)

		c, stop := startCache(nil)
		defer stop()

		Expect(visible(c, slice)).To(BeTrue())
	})

	It("restricts every informer to --watch-namespaces", func() {
		inWatched := configMap(watched, "in", map[string]string{labelVinylCacheName: "www"})
		inOther := configMap(other, "out", map[string]string{labelVinylCacheName: "www"})
		create(inWatched)
		create(inOther)

		c, stop := startCache([]string{watched})
		defer stop()

		Expect(visible(c, inWatched)).To(BeTrue())
		// A namespace outside DefaultNamespaces is not NotFound but an
		// error: the cache refuses to serve it at all.
		err := c.Get(ctx, client.ObjectKeyFromObject(inOther), &corev1.ConfigMap{})
		Expect(err).To(HaveOccurred())
	})

	It("strips managedFields from cached objects", func() {
		cm := configMap(watched, "managed", map[string]string{labelVinylCacheName: "www"})
		create(cm)
		Expect(cm.ManagedFields).NotTo(BeEmpty(), "the API server records managedFields on create")

		c, stop := startCache(nil)
		defer stop()

		got := &corev1.ConfigMap{}
		Eventually(func() error { return c.Get(ctx, client.ObjectKeyFromObject(cm), got) }).Should(Succeed())
		Expect(got.ManagedFields).To(BeEmpty())
	})
})
