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

	vinylv1 "github.com/bluedynamics/cloud-vinyl/api/v1"
)

func TestStorageArgs_Malloc(t *testing.T) {
	got := storageArgs([]vinylv1.StorageSpec{
		{Name: "s0", Type: "malloc", Size: resource.MustParse("1500M")},
	})
	assert.Equal(t, []string{"-s", "s0=malloc,1500000000"}, got)
}

func TestStorageArgs_File(t *testing.T) {
	got := storageArgs([]vinylv1.StorageSpec{
		{Name: "disk", Type: "file", Path: "/var/lib/varnish/cache.bin", Size: resource.MustParse("10Gi")},
	})
	assert.Equal(t, []string{"-s", "disk=file,/var/lib/varnish/cache.bin,10737418240"}, got)
}

func TestStorageArgs_Multiple(t *testing.T) {
	got := storageArgs([]vinylv1.StorageSpec{
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
	assert.Nil(t, storageArgs([]vinylv1.StorageSpec{}))
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
		"default_ttl":         "120",
		"cli_timeout":         "60",
		"timeout_idle":        "5",
		"http_max_hdr":        "64",
		"thread_pool_min":     "100",
		"vsl_space":           "80m",
	}
	got := varnishParamArgs(params)
	assert.Equal(t, []string{
		"-p", "cli_timeout=60",
		"-p", "default_ttl=120",
		"-p", "http_max_hdr=64",
		"-p", "thread_pool_min=100",
		"-p", "thread_pool_timeout=300",
		"-p", "timeout_idle=5",
		"-p", "vsl_space=80m",
	}, got, "params must be emitted sorted by key regardless of map iteration order")
}

func exporterBaseVC() *vinylv1.VinylCache {
	return &vinylv1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "my-cache", Namespace: "app"},
		Spec: vinylv1.VinylCacheSpec{
			Replicas: 1,
			Image:    "varnish:9.1.0",
			Backends: []vinylv1.BackendSpec{{Name: "app", ServiceRef: vinylv1.ServiceRef{Name: "svc"}}},
		},
	}
}

func getStatefulSet(t *testing.T, vc *vinylv1.VinylCache) *appsv1.StatefulSet {
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
	vc.Spec.Monitoring.Exporter = &vinylv1.ExporterSpec{Enabled: true}

	ss := getStatefulSet(t, vc)

	var exporter *corev1.Container
	for i := range ss.Spec.Template.Spec.Containers {
		if ss.Spec.Template.Spec.Containers[i].Name == "vinyl-exporter" {
			exporter = &ss.Spec.Template.Spec.Containers[i]
		}
	}
	require.NotNil(t, exporter, "exporter sidecar must be present when enabled")
	assert.Equal(t, "ghcr.io/bluedynamics/varnish-exporter:1.6.1-varnish9.1.0", exporter.Image)
	assert.Equal(t, []string{"-n", "/var/lib/varnish/varnishd", "-web.listen-address", ":9131"}, exporter.Args,
		"varnishstat must read varnishd's explicit -n, not its build default")

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

// spec.monitoring.exporter.port used to change only the containerPort and the
// NetworkPolicy, while the exporter kept listening on 9131.
func TestReconcileStatefulSet_ExporterListensOnConfiguredPort(t *testing.T) {
	vc := exporterBaseVC()
	vc.Spec.Monitoring.Exporter = &vinylv1.ExporterSpec{Enabled: true, Port: 9200}
	ss := getStatefulSet(t, vc)
	for _, c := range ss.Spec.Template.Spec.Containers {
		if c.Name != "vinyl-exporter" {
			continue
		}
		assert.Equal(t, []string{"-n", "/var/lib/varnish/varnishd", "-web.listen-address", ":9200"}, c.Args)
		require.Len(t, c.Ports, 1)
		assert.Equal(t, int32(9200), c.Ports[0].ContainerPort)
		return
	}
	require.Fail(t, "exporter sidecar must be present when enabled")
}

func TestReconcileStatefulSet_NoExporterByDefault(t *testing.T) {
	ss := getStatefulSet(t, exporterBaseVC())
	for _, c := range ss.Spec.Template.Spec.Containers {
		assert.NotEqual(t, "vinyl-exporter", c.Name)
	}
}

func tracingVC(name string) *vinylv1.VinylCache {
	vc := exporterBaseVC()
	vc.Name = name
	vc.Spec.Tracing = vinylv1.TracingSpec{
		Enabled: true,
		OTLP: vinylv1.OTLPSpec{
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
	assert.Equal(t, "/var/lib/varnish/varnishd", env["VARNISH_WORKDIR"],
		"varnishlog must read varnishd's explicit -n; with -t off a wrong one waits silently")

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
	vc := &vinylv1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "my-cache", Namespace: "app"},
		Spec: vinylv1.VinylCacheSpec{
			Replicas: 1,
			Image:    "varnish:9.1.0",
			Backends: []vinylv1.BackendSpec{{
				Name: "app", ServiceRef: vinylv1.ServiceRef{Name: "svc"},
			}},
			Pod: vinylv1.PodSpec{
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
	vc := &vinylv1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "my-cache", Namespace: "app"},
		Spec: vinylv1.VinylCacheSpec{
			Replicas: 2,
			Image:    "varnish:9.1.0",
			Backends: []vinylv1.BackendSpec{{
				Name: "app", ServiceRef: vinylv1.ServiceRef{Name: "svc"},
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
			Pod: vinylv1.PodSpec{
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
	vc := &vinylv1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "my-cache", Namespace: "app"},
		Spec: vinylv1.VinylCacheSpec{
			Replicas: 1,
			Image:    "varnish:9.1.0",
			Backends: []vinylv1.BackendSpec{{
				Name: "app", ServiceRef: vinylv1.ServiceRef{Name: "svc"},
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

// pArgs returns the values of all -p args in order.
func pArgs(args []string) []string {
	var values []string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-p" {
			values = append(values, args[i+1])
		}
	}
	return values
}

func TestReconcileStatefulSet_NoVarnishParams_OnlyHTTP2(t *testing.T) {
	varnish := varnishContainer(t, getStatefulSet(t, exporterBaseVC()))
	assert.Equal(t, []string{"feature=+http2"}, pArgs(varnish.Args),
		"nil varnishParameters must emit no -p beyond the operator's own")

	vc := exporterBaseVC()
	vc.Spec.VarnishParams = map[string]string{}
	varnish = varnishContainer(t, getStatefulSet(t, vc))
	assert.Equal(t, []string{"feature=+http2"}, pArgs(varnish.Args),
		"empty varnishParameters must emit no -p beyond the operator's own")
}

func TestReconcileStatefulSet_UserFeatureComesAfterHTTP2(t *testing.T) {
	vc := exporterBaseVC()
	vc.Spec.VarnishParams = map[string]string{"feature": "-http2"}
	varnish := varnishContainer(t, getStatefulSet(t, vc))
	assert.Equal(t, []string{"feature=+http2", "feature=-http2"}, pArgs(varnish.Args),
		"-p feature values accumulate, so the user's must come last to override")
}

func TestReconcileStatefulSet_VarnishParamsAfterFixedAndStorageArgs(t *testing.T) {
	vc := exporterBaseVC()
	vc.Spec.Storage = []vinylv1.StorageSpec{
		{Name: "mem", Type: "malloc", Size: resource.MustParse("1G")},
	}
	vc.Spec.VarnishParams = map[string]string{
		"thread_pool_min": "100",
	}

	varnish := varnishContainer(t, getStatefulSet(t, vc))

	assert.Equal(t, []string{"varnishd"}, varnish.Command,
		"the operator runs varnishd itself, not the image entrypoint")
	assert.Equal(t, []string{
		"-F",
		"-f", "/etc/varnish/default.vcl",
		"-n", "/var/lib/varnish/varnishd",
		"-a", "http=:8080,HTTP",
		"-j", "none",
		"-T", "127.0.0.1:6082",
		"-S", varnishSecretPath,
		"-p", "feature=+http2",
		"-s", "mem=malloc,1000000000",
		"-p", "thread_pool_min=100",
	}, varnish.Args,
		"fixed args, then -s storage args, then -p varnishParameters args, in that order")
}

// With spec.storage set, those are the only cache storages. The entrypoint
// used to add a hidden -s malloc,$VARNISH_SIZE next to them (#113).
func TestReconcileStatefulSet_StorageOnlyFromSpec(t *testing.T) {
	vc := exporterBaseVC()
	vc.Spec.Storage = []vinylv1.StorageSpec{
		{Name: "cache", Type: "malloc", Size: resource.MustParse("3G")},
	}
	varnish := varnishContainer(t, getStatefulSet(t, vc))
	var storages []string
	for i := 0; i+1 < len(varnish.Args); i++ {
		if varnish.Args[i] == "-s" {
			storages = append(storages, varnish.Args[i+1])
		}
	}
	assert.Equal(t, []string{"cache=malloc,3000000000"}, storages)

	vc.Spec.Storage = nil
	varnish = varnishContainer(t, getStatefulSet(t, vc))
	assert.NotContains(t, varnish.Args, "-s",
		"without spec.storage, varnishd creates its own default storage")
}

func TestReconcileStatefulSet_ProxyListenerOnlyWhenEnabled(t *testing.T) {
	listeners := func(args []string) []string {
		var a []string
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-a" {
				a = append(a, args[i+1])
			}
		}
		return a
	}

	vc := exporterBaseVC()
	varnish := varnishContainer(t, getStatefulSet(t, vc))
	assert.Equal(t, []string{"http=:8080,HTTP"}, listeners(varnish.Args),
		"no PROXY listener unless spec.proxyProtocol is enabled")
	for _, p := range varnish.Ports {
		assert.NotEqual(t, "proxy", p.Name)
	}

	vc.Spec.ProxyProtocol = vinylv1.ProxyProtocolSpec{Enabled: true}
	varnish = varnishContainer(t, getStatefulSet(t, vc))
	assert.Equal(t, []string{"http=:8080,HTTP", "proxy=:8081,PROXY"}, listeners(varnish.Args))

	vc.Spec.ProxyProtocol.Port = 9443
	varnish = varnishContainer(t, getStatefulSet(t, vc))
	assert.Equal(t, []string{"http=:8080,HTTP", "proxy=:9443,PROXY"}, listeners(varnish.Args),
		"the listener must sit on the port the container declares")
	var declared int32
	for _, p := range varnish.Ports {
		if p.Name == "proxy" {
			declared = p.ContainerPort
		}
	}
	assert.Equal(t, int32(9443), declared)
}

// --- varnish container env: MALLOC_CONF default and spec.pod.env (issue #114) ---

func envValue(t *testing.T, c *corev1.Container, name string) (corev1.EnvVar, bool) {
	t.Helper()
	for _, e := range c.Env {
		if e.Name == name {
			return e, true
		}
	}
	return corev1.EnvVar{}, false
}

func TestDefaultMallocConf(t *testing.T) {
	cases := []struct {
		name   string
		limits corev1.ResourceList
		want   string
	}{
		{"no limits", nil, jemallocBaseConf},
		{"memory limit only", corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("3Gi")}, jemallocBaseConf},
		{"half a CPU rounds up", corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m")}, jemallocBaseConf + ",narenas:4"},
		{"whole CPUs", corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}, jemallocBaseConf + ",narenas:8"},
		{"fractional above one", corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1500m")}, jemallocBaseConf + ",narenas:8"},
		{"zero limit ignored", corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("0")}, jemallocBaseConf},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, defaultMallocConf(corev1.ResourceRequirements{Limits: tc.limits}))
		})
	}
}

func TestReconcileStatefulSet_DefaultEnv(t *testing.T) {
	vc := exporterBaseVC()
	vc.Spec.Resources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
		Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m")},
	}
	varnish := varnishContainer(t, getStatefulSet(t, vc))

	require.Len(t, varnish.Env, 1)
	assert.Equal(t, corev1.EnvVar{
		Name:  "MALLOC_CONF",
		Value: "thp:never,background_thread:true,dirty_decay_ms:5000,muzzy_decay_ms:0,narenas:4",
	}, varnish.Env[0], "narenas follows the CPU limit, not the request")
}

func TestReconcileStatefulSet_PodEnvAppended(t *testing.T) {
	vc := exporterBaseVC()
	vc.Spec.Pod.Env = []corev1.EnvVar{
		{Name: "TZ", Value: "Europe/Vienna"},
		{Name: "FROM_SECRET", ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "s"},
				Key:                  "k",
			},
		}},
	}
	varnish := varnishContainer(t, getStatefulSet(t, vc))

	names := make([]string, 0, len(varnish.Env))
	for _, e := range varnish.Env {
		names = append(names, e.Name)
	}
	assert.Equal(t, []string{"MALLOC_CONF", "TZ", "FROM_SECRET"}, names)
	fromSecret, _ := envValue(t, varnish, "FROM_SECRET")
	require.NotNil(t, fromSecret.ValueFrom)
	assert.Equal(t, "k", fromSecret.ValueFrom.SecretKeyRef.Key)
}

func TestReconcileStatefulSet_PodEnvOverridesMallocConf(t *testing.T) {
	for _, value := range []string{"narenas:2", ""} {
		t.Run(value, func(t *testing.T) {
			vc := exporterBaseVC()
			vc.Spec.Pod.Env = []corev1.EnvVar{{Name: "MALLOC_CONF", Value: value}}
			varnish := varnishContainer(t, getStatefulSet(t, vc))

			require.Len(t, varnish.Env, 1, "an override replaces the default instead of adding a second entry")
			assert.Equal(t, corev1.EnvVar{Name: "MALLOC_CONF", Value: value}, varnish.Env[0],
				"the user value replaces the whole default, in place")
		})
	}
}
