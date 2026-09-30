package controller

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
)

func TestStorageArgs_Malloc(t *testing.T) {
	got := storageArgs([]v1alpha1.StorageSpec{
		{Name: "s0", Type: "malloc", Size: resource.MustParse("1500M")},
	})
	assert.Equal(t, []string{"-s", "s0=malloc,1500000000"}, got)
}

func TestStorageArgs_File(t *testing.T) {
	got := storageArgs([]v1alpha1.StorageSpec{
		{Name: "disk", Type: "file", Path: "/var/lib/varnish/cache.bin", Size: resource.MustParse("10Gi")},
	})
	assert.Equal(t, []string{"-s", "disk=file,/var/lib/varnish/cache.bin,10737418240"}, got)
}

func TestStorageArgs_Multiple(t *testing.T) {
	got := storageArgs([]v1alpha1.StorageSpec{
		{Name: "mem", Type: "malloc", Size: resource.MustParse("1G")},
		{Name: "disk", Type: "file", Path: "/var/lib/varnish/cache", Size: resource.MustParse("10G")},
	})
	assert.Equal(t, []string{
		"-s", "mem=malloc,1000000000",
		"-s", "disk=file,/var/lib/varnish/cache,10000000000",
	}, got)
}

func TestStorageArgs_Empty(t *testing.T) {
	assert.Nil(t, storageArgs(nil))
	assert.Nil(t, storageArgs([]v1alpha1.StorageSpec{}))
}

func TestVarnishParamArgs_Single(t *testing.T) {
	got := varnishParamArgs(map[string]string{"thread_pool_min": "100"})
	assert.Equal(t, []string{"-p", "thread_pool_min=100"}, got)
}

func TestVarnishParamArgs_Empty(t *testing.T) {
	assert.Nil(t, varnishParamArgs(nil))
	assert.Nil(t, varnishParamArgs(map[string]string{}))
}

// TestVarnishParamArgs_SortedByKey guards against reintroducing
// non-deterministic ordering: Go map iteration order is randomized per
// process, so if varnishParamArgs ever iterated the map directly instead of
// sorting, this test would fail on some fraction of runs (not necessarily
// this one). Deliberately unsorted insertion order plus enough keys (7) make
// the odds of an unsorted implementation accidentally producing sorted
// output on any given run astronomically small (1/7! ~= 1/5040), so a single
// run is a reliable regression catcher.
func TestVarnishParamArgs_SortedByKey(t *testing.T) {
	params := map[string]string{
		"thread_pool_timeout": "300",
		"feature":             "+esi",
		"cli_timeout":         "60",
		"timeout_idle":        "5",
		"http_max_hdr":        "64",
		"thread_pool_min":     "100",
		"vsl_space":           "80m",
	}
	got := varnishParamArgs(params)
	assert.Equal(t, []string{
		"-p", "cli_timeout=60",
		"-p", "feature=+esi",
		"-p", "http_max_hdr=64",
		"-p", "thread_pool_min=100",
		"-p", "thread_pool_timeout=300",
		"-p", "timeout_idle=5",
		"-p", "vsl_space=80m",
	}, got, "params must be emitted sorted by key regardless of map iteration order")
}

func exporterBaseVC() *v1alpha1.VinylCache {
	return &v1alpha1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "my-cache", Namespace: "app"},
		Spec: v1alpha1.VinylCacheSpec{
			Replicas: 1,
			Image:    "varnish:8.0.2",
			Backends: []v1alpha1.BackendSpec{{Name: "app", ServiceRef: v1alpha1.ServiceRef{Name: "svc"}}},
		},
	}
}

func getStatefulSet(t *testing.T, vc *v1alpha1.VinylCache) *appsv1.StatefulSet {
	t.Helper()
	sch := newScheme(t)
	cli := fake.NewClientBuilder().WithScheme(sch).Build()
	r := &VinylCacheReconciler{Client: cli, Scheme: sch}
	require.NoError(t, r.reconcileStatefulSet(context.Background(), vc))
	ss := &appsv1.StatefulSet{}
	require.NoError(t, cli.Get(context.Background(),
		types.NamespacedName{Name: vc.Name, Namespace: vc.Namespace}, ss))
	return ss
}

func TestReconcileStatefulSet_ExporterSidecarWhenEnabled(t *testing.T) {
	vc := exporterBaseVC()
	vc.Spec.Monitoring.Exporter = &v1alpha1.ExporterSpec{Enabled: true}

	ss := getStatefulSet(t, vc)

	var exporter *corev1.Container
	for i := range ss.Spec.Template.Spec.Containers {
		if ss.Spec.Template.Spec.Containers[i].Name == "vinyl-exporter" {
			exporter = &ss.Spec.Template.Spec.Containers[i]
		}
	}
	require.NotNil(t, exporter, "exporter sidecar must be present when enabled")
	assert.Equal(t, "ghcr.io/bluedynamics/varnish-exporter:1.6.1", exporter.Image)

	var mount *corev1.VolumeMount
	for i := range exporter.VolumeMounts {
		if exporter.VolumeMounts[i].MountPath == "/var/lib/varnish" {
			mount = &exporter.VolumeMounts[i]
		}
	}
	require.NotNil(t, mount, "exporter must mount /var/lib/varnish")
	assert.True(t, mount.ReadOnly, "exporter VSM mount must be read-only")
	assert.Equal(t, "varnish-workdir", mount.Name)
}

func TestReconcileStatefulSet_NoExporterByDefault(t *testing.T) {
	ss := getStatefulSet(t, exporterBaseVC())
	for _, c := range ss.Spec.Template.Spec.Containers {
		assert.NotEqual(t, "vinyl-exporter", c.Name)
	}
}

func tracingVC(name string) *v1alpha1.VinylCache {
	vc := exporterBaseVC()
	vc.Name = name
	vc.Spec.Tracing = v1alpha1.TracingSpec{
		Enabled: true,
		OTLP: v1alpha1.OTLPSpec{
			Endpoint: "collector.monitoring.svc:4317",
			Insecure: true,
		},
	}
	return vc
}

func TestReconcileStatefulSet_TracerSidecarWhenEnabled(t *testing.T) {
	ss := getStatefulSet(t, tracingVC("traced"))
	var tracer *corev1.Container
	for i := range ss.Spec.Template.Spec.Containers {
		if ss.Spec.Template.Spec.Containers[i].Name == "vinyl-tracer" {
			tracer = &ss.Spec.Template.Spec.Containers[i]
		}
	}
	require.NotNil(t, tracer, "vinyl-tracer container missing")

	env := map[string]string{}
	for _, e := range tracer.Env {
		env[e.Name] = e.Value
	}
	assert.Equal(t, "collector.monitoring.svc:4317", env["OTLP_ENDPOINT"])
	assert.Equal(t, "grpc", env["OTLP_PROTOCOL"], "protocol defaults to grpc")
	assert.Equal(t, "true", env["OTLP_INSECURE"])
	assert.Equal(t, "traced", env["TRACER_SERVICE_NAME"], "serviceName defaults to CR name")

	require.Len(t, tracer.VolumeMounts, 1)
	assert.Equal(t, "/var/lib/varnish", tracer.VolumeMounts[0].MountPath)
	assert.True(t, tracer.VolumeMounts[0].ReadOnly)
}

func TestReconcileStatefulSet_NoTracerByDefault(t *testing.T) {
	ss := getStatefulSet(t, exporterBaseVC())
	for _, c := range ss.Spec.Template.Spec.Containers {
		assert.NotEqual(t, "vinyl-tracer", c.Name)
	}
}

func TestReconcileStatefulSet_TracerImageFromEnv(t *testing.T) {
	t.Setenv("TRACER_IMAGE", "example.org/tracer:test")
	ss := getStatefulSet(t, tracingVC("traced-img"))
	for _, c := range ss.Spec.Template.Spec.Containers {
		if c.Name == "vinyl-tracer" {
			assert.Equal(t, "example.org/tracer:test", c.Image)
			return
		}
	}
	t.Fatal("vinyl-tracer container missing")
}

func TestReconcileStatefulSet_UserVolumesAndMountsAppended(t *testing.T) {
	sch := newScheme(t)
	quantity := resource.MustParse("100Mi")
	vc := &v1alpha1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "my-cache", Namespace: "app"},
		Spec: v1alpha1.VinylCacheSpec{
			Replicas: 1,
			Image:    "varnish:8.0.2",
			Backends: []v1alpha1.BackendSpec{{
				Name: "app", ServiceRef: v1alpha1.ServiceRef{Name: "svc"},
			}},
			Pod: v1alpha1.PodSpec{
				Volumes: []corev1.Volume{
					{
						Name: "cache-ssd",
						VolumeSource: corev1.VolumeSource{
							EmptyDir: &corev1.EmptyDirVolumeSource{
								SizeLimit: &quantity,
							},
						},
					},
				},
				VolumeMounts: []corev1.VolumeMount{
					{Name: "cache-ssd", MountPath: "/var/lib/varnish-cache"},
				},
			},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).Build()
	r := &VinylCacheReconciler{Client: cli, Scheme: sch}
	require.NoError(t, r.reconcileStatefulSet(context.Background(), vc))

	ss := &appsv1.StatefulSet{}
	require.NoError(t, cli.Get(context.Background(),
		types.NamespacedName{Name: vc.Name, Namespace: vc.Namespace}, ss))

	// User volume present in pod spec.
	var foundVolume bool
	for _, v := range ss.Spec.Template.Spec.Volumes {
		if v.Name == "cache-ssd" {
			foundVolume = true
			require.NotNil(t, v.EmptyDir)
			require.NotNil(t, v.EmptyDir.SizeLimit)
			assert.Equal(t, "100Mi", v.EmptyDir.SizeLimit.String())
		}
	}
	assert.True(t, foundVolume, "user volume 'cache-ssd' must be appended to pod volumes")

	// User mount present on the varnish container.
	var varnish *corev1.Container
	for i := range ss.Spec.Template.Spec.Containers {
		if ss.Spec.Template.Spec.Containers[i].Name == "varnish" {
			varnish = &ss.Spec.Template.Spec.Containers[i]
		}
	}
	require.NotNil(t, varnish)
	var foundMount bool
	for _, m := range varnish.VolumeMounts {
		if m.Name == "cache-ssd" {
			foundMount = true
			assert.Equal(t, "/var/lib/varnish-cache", m.MountPath)
		}
	}
	assert.True(t, foundMount, "user volumeMount must be appended to varnish container")

	// Reserved volumes still present.
	reserved := []string{"agent-token", "varnish-secret", "varnish-workdir", "varnish-tmp", "bootstrap-vcl"}
	for _, rn := range reserved {
		var found bool
		for _, v := range ss.Spec.Template.Spec.Volumes {
			if v.Name == rn {
				found = true
			}
		}
		assert.True(t, found, "reserved volume %q must remain present", rn)
	}
}

func TestReconcileStatefulSet_VolumeClaimTemplatesPassthrough(t *testing.T) {
	sch := newScheme(t)
	storageClass := "hcloud-volumes"
	vc := &v1alpha1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "my-cache", Namespace: "app"},
		Spec: v1alpha1.VinylCacheSpec{
			Replicas: 2,
			Image:    "varnish:8.0.2",
			Backends: []v1alpha1.BackendSpec{{
				Name: "app", ServiceRef: v1alpha1.ServiceRef{Name: "svc"},
			}},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{Name: "cache-ssd"},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					StorageClassName: &storageClass,
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("80Gi"),
						},
					},
				},
			}},
			Pod: v1alpha1.PodSpec{
				VolumeMounts: []corev1.VolumeMount{
					{Name: "cache-ssd", MountPath: "/var/lib/varnish-cache"},
				},
			},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).Build()
	r := &VinylCacheReconciler{Client: cli, Scheme: sch}
	require.NoError(t, r.reconcileStatefulSet(context.Background(), vc))

	ss := &appsv1.StatefulSet{}
	require.NoError(t, cli.Get(context.Background(),
		types.NamespacedName{Name: vc.Name, Namespace: vc.Namespace}, ss))

	require.Len(t, ss.Spec.VolumeClaimTemplates, 1)
	pvc := ss.Spec.VolumeClaimTemplates[0]
	assert.Equal(t, "cache-ssd", pvc.Name)
	require.NotNil(t, pvc.Spec.StorageClassName)
	assert.Equal(t, "hcloud-volumes", *pvc.Spec.StorageClassName)
	assert.Equal(t, "80Gi", pvc.Spec.Resources.Requests.Storage().String())
}

func TestReconcileStatefulSet_NoUserVolumes_DefaultsUnchanged(t *testing.T) {
	sch := newScheme(t)
	vc := &v1alpha1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "my-cache", Namespace: "app"},
		Spec: v1alpha1.VinylCacheSpec{
			Replicas: 1,
			Image:    "varnish:8.0.2",
			Backends: []v1alpha1.BackendSpec{{
				Name: "app", ServiceRef: v1alpha1.ServiceRef{Name: "svc"},
			}},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).Build()
	r := &VinylCacheReconciler{Client: cli, Scheme: sch}
	require.NoError(t, r.reconcileStatefulSet(context.Background(), vc))

	ss := &appsv1.StatefulSet{}
	require.NoError(t, cli.Get(context.Background(),
		types.NamespacedName{Name: vc.Name, Namespace: vc.Namespace}, ss))

	assert.Empty(t, ss.Spec.VolumeClaimTemplates,
		"no user claim templates -> VolumeClaimTemplates stays empty")
	assert.Len(t, ss.Spec.Template.Spec.Volumes, 5,
		"no user volumes -> only the 5 operator-managed volumes remain")
}

func TestReconcileStatefulSet_DeclaresTerminationGracePeriod(t *testing.T) {
	ss := getStatefulSet(t, exporterBaseVC())

	grace := ss.Spec.Template.Spec.TerminationGracePeriodSeconds
	require.NotNil(t, grace,
		"varnish pods must declare terminationGracePeriodSeconds; the Kubernetes "+
			"default of 30s is what the namespace controller uses to size its "+
			"re-sweep interval, which stalls namespace teardown (#63)")
	assert.Equal(t, int64(varnishTerminationGracePeriodSeconds), *grace)

	assert.Greater(t, *grace, int64(varnishPreStopSleepSeconds),
		"grace period must outlast the preStop sleep, or varnishd is SIGKILLed mid-drain")
	assert.Less(t, *grace, int64(30),
		"grace period must stay below the Kubernetes default so namespace teardown "+
			"stays inside Chainsaw's cleanup timeout")
}

func TestReconcileStatefulSet_PreStopSleepMatchesConstant(t *testing.T) {
	ss := getStatefulSet(t, exporterBaseVC())

	var varnish *corev1.Container
	for i := range ss.Spec.Template.Spec.Containers {
		if ss.Spec.Template.Spec.Containers[i].Name == "varnish" {
			varnish = &ss.Spec.Template.Spec.Containers[i]
		}
	}
	require.NotNil(t, varnish, "varnish container must be present")
	require.NotNil(t, varnish.Lifecycle)
	require.NotNil(t, varnish.Lifecycle.PreStop)
	require.NotNil(t, varnish.Lifecycle.PreStop.Exec)

	assert.Equal(t,
		[]string{"sleep", strconv.Itoa(varnishPreStopSleepSeconds)},
		varnish.Lifecycle.PreStop.Exec.Command,
		"preStop sleep and the grace-period constant must not drift apart")
}

func varnishContainer(t *testing.T, ss *appsv1.StatefulSet) *corev1.Container {
	t.Helper()
	for i := range ss.Spec.Template.Spec.Containers {
		if ss.Spec.Template.Spec.Containers[i].Name == "varnish" {
			return &ss.Spec.Template.Spec.Containers[i]
		}
	}
	require.Fail(t, "varnish container must be present")
	return nil
}

func TestReconcileStatefulSet_VarnishParamsEmitPArgs(t *testing.T) {
	vc := exporterBaseVC()
	vc.Spec.VarnishParams = map[string]string{
		"thread_pool_min": "100",
		"timeout_idle":    "5",
	}

	varnish := varnishContainer(t, getStatefulSet(t, vc))

	assert.Contains(t, varnish.Args, "-p")
	// Sorted: thread_pool_min before timeout_idle.
	assert.Subset(t, varnish.Args,
		[]string{"-p", "thread_pool_min=100", "-p", "timeout_idle=5"})

	// Assert exact sorted sub-sequence at the tail of Args.
	n := len(varnish.Args)
	require.GreaterOrEqual(t, n, 4)
	assert.Equal(t, []string{"-p", "thread_pool_min=100", "-p", "timeout_idle=5"},
		varnish.Args[n-4:], "varnishParameters must be emitted as -p key=value, sorted by key")
}

func TestReconcileStatefulSet_NoVarnishParams_NoPArgs(t *testing.T) {
	varnish := varnishContainer(t, getStatefulSet(t, exporterBaseVC()))
	assert.NotContains(t, varnish.Args, "-p",
		"nil varnishParameters must not emit any -p arg")

	vc := exporterBaseVC()
	vc.Spec.VarnishParams = map[string]string{}
	varnish = varnishContainer(t, getStatefulSet(t, vc))
	assert.NotContains(t, varnish.Args, "-p",
		"empty varnishParameters must not emit any -p arg")
}

func TestReconcileStatefulSet_VarnishParamsAfterFixedAndStorageArgs(t *testing.T) {
	vc := exporterBaseVC()
	vc.Spec.Storage = []v1alpha1.StorageSpec{
		{Name: "mem", Type: "malloc", Size: resource.MustParse("1G")},
	}
	vc.Spec.VarnishParams = map[string]string{
		"thread_pool_min": "100",
	}

	varnish := varnishContainer(t, getStatefulSet(t, vc))

	assert.Equal(t, []string{
		"-j", "none",
		"-T", "127.0.0.1:6082",
		"-S", varnishSecretPath,
		"-s", "mem=malloc,1000000000",
		"-p", "thread_pool_min=100",
	}, varnish.Args,
		"fixed -j/-T/-S args, then -s storage args, then -p varnishParameters args, in that order")
}
