package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	vinylv1 "github.com/bluedynamics/cloud-vinyl/api/v1"
	"github.com/bluedynamics/cloud-vinyl/internal/catalog"
	"github.com/bluedynamics/cloud-vinyl/internal/generator"
)

func runtimeResolveFixture(t *testing.T, vc *vinylv1.VinylCache) (*VinylCacheReconciler, client.Client) {
	t.Helper()
	sch := newScheme(t)
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(vc).
		WithStatusSubresource(vc).Build()
	return &VinylCacheReconciler{
		Client: cli, Scheme: sch, Generator: generator.New(), Catalog: testCatalog(t),
	}, cli
}

func reconcileOnce(t *testing.T, r *VinylCacheReconciler, vc *vinylv1.VinylCache) {
	t.Helper()
	_, err := r.Reconcile(context.Background(),
		ctrl.Request{NamespacedName: client.ObjectKeyFromObject(vc)})
	require.NoError(t, err)
}

func getVC(t *testing.T, cli client.Client, vc *vinylv1.VinylCache) *vinylv1.VinylCache {
	t.Helper()
	got := &vinylv1.VinylCache{}
	require.NoError(t, cli.Get(context.Background(), client.ObjectKeyFromObject(vc), got))
	return got
}

func TestReconcile_SetsRuntimeStatus(t *testing.T) {
	t.Run("sidecars enabled", func(t *testing.T) {
		vc := &vinylv1.VinylCache{
			ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "ns1"},
			Spec: vinylv1.VinylCacheSpec{
				Image:      "varnish:8.0.2",
				Monitoring: vinylv1.MonitoringSpec{Exporter: &vinylv1.ExporterSpec{Enabled: true}},
				Tracing:    vinylv1.TracingSpec{Enabled: true},
			},
		}
		r, cli := runtimeResolveFixture(t, vc)
		reconcileOnce(t, r, vc)

		got := getVC(t, cli, vc)
		require.NotNil(t, got.Status.Runtime)
		assert.Equal(t, vinylv1.RuntimeStatus{
			Flavor:   vinylv1.FlavorVarnish,
			Version:  "9.1.0",
			Daemon:   "varnish:8.0.2",
			Exporter: "example.org/varnish-exporter:1.8.3-varnish9.1.0",
			Tracer:   "example.org/cloud-vinyl-tracer:1.0.0",
		}, *got.Status.Runtime)
		assert.Equal(t, metav1.ConditionTrue, findConditionStatus(got, vinylv1.ConditionRuntimeResolved))
	})

	t.Run("sidecars disabled", func(t *testing.T) {
		vc := &vinylv1.VinylCache{
			ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "ns1"},
			Spec:       vinylv1.VinylCacheSpec{Image: "varnish:8.0.2"},
		}
		r, cli := runtimeResolveFixture(t, vc)
		reconcileOnce(t, r, vc)

		got := getVC(t, cli, vc)
		require.NotNil(t, got.Status.Runtime)
		assert.Equal(t, vinylv1.RuntimeStatus{
			Flavor: vinylv1.FlavorVarnish, Version: "9.1.0", Daemon: "varnish:8.0.2",
		}, *got.Status.Runtime)
	})
}

func TestReconcile_VanishedEntryFreezesStatefulSet(t *testing.T) {
	vc := &vinylv1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "ns1"},
		Spec:       vinylv1.VinylCacheSpec{Flavor: vinylv1.FlavorVinyl, Version: "9.1.0"},
	}
	r, cli := runtimeResolveFixture(t, vc)
	reconcileOnce(t, r, vc)

	key := client.ObjectKey{Name: "c1", Namespace: "ns1"}
	before := &appsv1.StatefulSet{}
	require.NoError(t, cli.Get(context.Background(), key, before))

	cat, err := catalog.Parse([]byte(`
varnish:
  default: "9.1.0"
  versions:
    "9.1.0":
      daemon: docker.io/library/varnish:9.1.0
      exporter: example.org/varnish-exporter:1.8.3-varnish9.1.0
      tracer: example.org/cloud-vinyl-tracer:1.0.0
`))
	require.NoError(t, err)
	r.Catalog = cat
	reconcileOnce(t, r, vc)

	after := &appsv1.StatefulSet{}
	require.NoError(t, cli.Get(context.Background(), key, after))
	assert.Equal(t, before.Spec.Template, after.Spec.Template, "pod template must stay frozen")

	got := getVC(t, cli, vc)
	var cond *metav1.Condition
	for i := range got.Status.Conditions {
		if got.Status.Conditions[i].Type == vinylv1.ConditionRuntimeResolved {
			cond = &got.Status.Conditions[i]
		}
	}
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, "NotInCatalog", cond.Reason)
	assert.Equal(t, 1, strings.Count(cond.Message, `flavor "vinyl"`), "flavor named once: %s", cond.Message)
	assert.True(t, strings.HasPrefix(cond.Message, `flavor "vinyl" is not in the runtime catalog`), cond.Message)

	// Progressing must not claim completion while the StatefulSet is held back.
	assert.Equal(t, metav1.ConditionTrue, findConditionStatus(got, vinylv1.ConditionProgressing))
	for _, c := range got.Status.Conditions {
		if c.Type == vinylv1.ConditionProgressing {
			assert.Equal(t, "RuntimeNotInCatalog", c.Reason)
			assert.Contains(t, c.Message, "held back")
		}
	}
}

func TestReconcile_UnchangedRuntimeIssuesNoExtraRuntimeStatusUpdate(t *testing.T) {
	vc := &vinylv1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "ns1"},
		Spec:       vinylv1.VinylCacheSpec{Image: "varnish:8.0.2"},
	}
	sch := newScheme(t)
	updates := 0
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(vc).WithStatusSubresource(vc).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				updates++
				return c.SubResource(sub).Update(ctx, obj, opts...)
			},
		}).Build()
	r := &VinylCacheReconciler{Client: cli, Scheme: sch, Generator: generator.New(), Catalog: testCatalog(t)}

	cur := getVC(t, cli, vc)
	_, _, err := r.resolveRuntime(context.Background(), cur)
	require.NoError(t, err)
	assert.Equal(t, 1, updates, "first resolution persists the outcome")

	cur = getVC(t, cli, vc)
	_, resolved, err := r.resolveRuntime(context.Background(), cur)
	require.NoError(t, err)
	assert.True(t, resolved)
	assert.Equal(t, 1, updates, "unchanged inputs must not write again")

	// A changed input is written.
	cur = getVC(t, cli, vc)
	cur.Spec.Image = "varnish:8.0.3"
	_, _, err = r.resolveRuntime(context.Background(), cur)
	require.NoError(t, err)
	assert.Equal(t, 2, updates)
}
