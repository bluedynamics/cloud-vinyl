# Pod Volumes and VolumeClaimTemplates — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let `VinylCache` users plumb their own `corev1.Volume`s, `corev1.VolumeMount`s, and `PersistentVolumeClaim` templates into the generated StatefulSet, so `spec.storage[].type=file` paths can land on user-provided PVCs (StorageClass-controlled SSD / NVMe) or on EmptyDir with `sizeLimit`, instead of the one hardcoded EmptyDir under `/var/lib/varnish`.

**Architecture:** Three new optional fields on the CRD — `spec.pod.volumes`, `spec.pod.volumeMounts`, `spec.volumeClaimTemplates`. The controller appends them to the operator-managed defaults when building the StatefulSet. An admission webhook rejects collisions with reserved names (`agent-token`, `varnish-secret`, `varnish-workdir`, `varnish-tmp`, `bootstrap-vcl`) and reserved mount paths (`/run/vinyl`, `/etc/varnish/secret`, `/var/lib/varnish`, `/tmp`, `/etc/varnish/default.vcl`), rejects duplicate volume names across `pod.volumes` and `volumeClaimTemplates`, rejects unresolvable `pod.volumeMounts[].name`, and rejects `spec.storage[].path` values that would write into the operator-owned mounts. The existing `spec.storage[]` -s arg emission is unchanged — users choose the path, the operator trusts it.

**Tech Stack:** Go (controller-runtime), Kubernetes corev1 API, kubebuilder, testify.

**Scope:** single subsystem — pod-volume plumbing + its validation. Out of scope per the issue: multi-tier eviction, cache warming, snapshotting, stock-entrypoint `s0` collision (tracked separately in #44).

---

## File Structure

- [api/v1alpha1/vinylcache_types.go](api/v1alpha1/vinylcache_types.go) — new fields on `PodSpec` and `VinylCacheSpec`.
- [api/v1alpha1/zz_generated.deepcopy.go](api/v1alpha1/zz_generated.deepcopy.go) — regenerated.
- [config/crd/bases/vinyl.bluedynamics.eu_vinylcaches.yaml](config/crd/bases/vinyl.bluedynamics.eu_vinylcaches.yaml) — regenerated.
- [charts/cloud-vinyl/crds/vinylcache.yaml](charts/cloud-vinyl/crds/vinylcache.yaml) — synced from `config/crd/bases/` (manual copy; issue #34 tracks automating this).
- [internal/controller/statefulset.go](internal/controller/statefulset.go) — append user volumes, mounts, claim templates.
- [internal/controller/statefulset_test.go](internal/controller/statefulset_test.go) — new tests covering volume/mount/claim passthrough and interaction with reserved defaults.
- [internal/webhook/vinylcache_validator.go](internal/webhook/vinylcache_validator.go) — new validation rules.
- [internal/webhook/vinylcache_validator_test.go](internal/webhook/vinylcache_validator_test.go) — new test cases.
- Create: `e2e/fixtures/vinylcaches/ssd-backed-storage.yaml` — fixture for the new chainsaw test.
- Create: `e2e/tests/volumes-and-pvc/chainsaw-test.yaml` — E2E for the two scenarios.
- Create: `docs/sources/how-to/ssd-backed-storage.md` — user-facing how-to.
- Modify: `docs/sources/how-to/index.md` — add the new guide to the toctree.
- Modify: `docs/sources/reference/vinylcache-spec.md` — document the new fields.

One file = one clear responsibility. No restructuring of existing code.

---

### Task 1: CRD — add `PodSpec.Volumes`, `PodSpec.VolumeMounts`, `VinylCacheSpec.VolumeClaimTemplates`

**Files:**
- Modify: `api/v1alpha1/vinylcache_types.go`

- [ ] **Step 1: Add fields to `PodSpec`**

In `api/v1alpha1/vinylcache_types.go`, extend the existing `PodSpec` struct (currently ending with `PriorityClass`). Add the two new fields at the end of the struct:

```go
	// volumes are additional pod-level volumes appended to the operator-managed
	// defaults (agent-token, varnish-secret, varnish-workdir, varnish-tmp,
	// bootstrap-vcl). Use to back spec.storage[].path with a PVC, an EmptyDir
	// with sizeLimit, or any VolumeSource supported by Kubernetes. Reserved
	// names collide with operator-managed volumes and are rejected by the
	// admission webhook. Volume names must also be unique across volumes and
	// volumeClaimTemplates.
	// +optional
	Volumes []corev1.Volume `json:"volumes,omitempty"`

	// volumeMounts are additional mounts appended to the varnish container.
	// Each entry must reference a name present in spec.pod.volumes or
	// spec.volumeClaimTemplates. Reserved mount paths (/run/vinyl,
	// /etc/varnish/secret, /var/lib/varnish, /tmp, /etc/varnish/default.vcl)
	// are rejected by the admission webhook.
	// +optional
	VolumeMounts []corev1.VolumeMount `json:"volumeMounts,omitempty"`
```

- [ ] **Step 2: Add `VolumeClaimTemplates` to `VinylCacheSpec`**

In the same file, add to `VinylCacheSpec` — insert the field alphabetically between `VarnishParams` and `VCL` (or immediately above `Pod` if ordering by usage is preferred; pick whichever matches the file's existing ordering):

```go
	// volumeClaimTemplates are appended verbatim to the generated StatefulSet's
	// spec.volumeClaimTemplates. Each template yields one PVC per replica
	// (named <claim>-<statefulset>-<ord>) that persists across pod restarts
	// for that replica. Useful for per-replica SSD-backed file storage.
	// Reference the claim name from spec.pod.volumeMounts; reference the
	// resulting mountPath from spec.storage[].path.
	// +optional
	VolumeClaimTemplates []corev1.PersistentVolumeClaim `json:"volumeClaimTemplates,omitempty"`
```

- [ ] **Step 3: Regenerate deepcopy + CRD manifest**

Run:

```bash
make generate manifests
```

Expected: `api/v1alpha1/zz_generated.deepcopy.go` updates so `PodSpec.DeepCopyInto` copies `Volumes` and `VolumeMounts`, and `VinylCacheSpec.DeepCopyInto` copies `VolumeClaimTemplates`. `config/crd/bases/vinyl.bluedynamics.eu_vinylcaches.yaml` gains the three subschemas. If any unrelated CRDs (`_prometheusrules.yaml`, `_servicemonitors.yaml`) or RBAC files drift due to controller-gen version skew (observed in prior releases), revert them:

```bash
git checkout -- config/crd/bases/_prometheusrules.yaml config/crd/bases/_servicemonitors.yaml config/rbac/role.yaml 2>/dev/null || true
```

- [ ] **Step 4: Sync the Helm-chart CRD copy**

Until #34 automates this, copy the canonical CRD into the chart:

```bash
cp config/crd/bases/vinyl.bluedynamics.eu_vinylcaches.yaml charts/cloud-vinyl/crds/vinylcache.yaml
```

- [ ] **Step 5: Verify build + commit**

```bash
go build ./...
```
Expected: no output, exit 0.

```bash
git add api/v1alpha1/vinylcache_types.go api/v1alpha1/zz_generated.deepcopy.go config/crd/bases/vinyl.bluedynamics.eu_vinylcaches.yaml charts/cloud-vinyl/crds/vinylcache.yaml
git commit -m "feat(api): add spec.pod.{volumes,volumeMounts} + spec.volumeClaimTemplates

Three new optional fields that let users back spec.storage[].path with
a PVC, a StorageClass-provisioned SSD, or an EmptyDir with sizeLimit
instead of the operator's fixed varnish-workdir EmptyDir.

Generator + controller wiring land in follow-up commits; this change
only extends the type + regenerates manifests.

Refs #43"
```

---

### Task 2: Controller — append user volumes / mounts / claim-templates to the StatefulSet

**Files:**
- Modify: `internal/controller/statefulset.go`
- Modify: `internal/controller/statefulset_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/controller/statefulset_test.go`:

```go
func TestReconcileStatefulSet_UserVolumesAndMountsAppended(t *testing.T) {
	sch := newScheme(t)
	quantity := resource.MustParse("100Mi")
	vc := &v1alpha1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "my-cache", Namespace: "app"},
		Spec: v1alpha1.VinylCacheSpec{
			Replicas: 1,
			Image:    "varnish:7.6",
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
	for _, r := range reserved {
		var found bool
		for _, v := range ss.Spec.Template.Spec.Volumes {
			if v.Name == r {
				found = true
			}
		}
		assert.True(t, found, "reserved volume %q must remain present", r)
	}
}

func TestReconcileStatefulSet_VolumeClaimTemplatesPassthrough(t *testing.T) {
	sch := newScheme(t)
	storageClass := "hcloud-volumes"
	vc := &v1alpha1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "my-cache", Namespace: "app"},
		Spec: v1alpha1.VinylCacheSpec{
			Replicas: 2,
			Image:    "varnish:7.6",
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
			Image:    "varnish:7.6",
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
```

Add any missing imports (likely `"k8s.io/apimachinery/pkg/api/resource"` and `appsv1 "k8s.io/api/apps/v1"`). The `newScheme` helper is in `endpoints_test.go`; reuse it.

- [ ] **Step 2: Run tests to confirm they fail**

```bash
go test ./internal/controller/ -run TestReconcileStatefulSet_ -v -count=1
```

Expected: `TestReconcileStatefulSet_UserVolumesAndMountsAppended` and `TestReconcileStatefulSet_VolumeClaimTemplatesPassthrough` FAIL (user volumes absent from output; claim templates absent). `TestReconcileStatefulSet_NoUserVolumes_DefaultsUnchanged` may PASS already.

- [ ] **Step 3: Wire `PodSpec.Volumes` + `PodSpec.VolumeMounts` into the container build**

Open `internal/controller/statefulset.go`. Locate the `varnishContainer` literal where `VolumeMounts` is set (currently lines ~85-111).

Immediately **after** the `varnishContainer` variable is fully constructed (and **before** the proxy-protocol port append block), add:

```go
	// Append user-declared volume mounts to the varnish container.
	if len(vc.Spec.Pod.VolumeMounts) > 0 {
		varnishContainer.VolumeMounts = append(varnishContainer.VolumeMounts, vc.Spec.Pod.VolumeMounts...)
	}
```

Find the `volumes := []corev1.Volume{ ... }` literal (currently lines ~188-229). Immediately **after** that block (and **before** the `uid := int64(65532)` line), add:

```go
	// Append user-declared pod volumes after the operator-managed defaults.
	if len(vc.Spec.Pod.Volumes) > 0 {
		volumes = append(volumes, vc.Spec.Pod.Volumes...)
	}
```

- [ ] **Step 4: Wire `VolumeClaimTemplates` onto the StatefulSet spec**

Locate the `StatefulSet` construction (search for `appsv1.StatefulSet{` or the `Spec.VolumeClaimTemplates` absence — the existing code never touches that field). Find the StatefulSet's `Spec:` block:

```go
	ss.Spec = appsv1.StatefulSetSpec{
		// ... existing fields: ServiceName, Replicas, Selector, Template, PodManagementPolicy ...
	}
```

(The exact literal name and location depend on how the reconciler writes the spec — it uses `CreateOrUpdate`. Find the mutate function that sets `ss.Spec.Replicas`, `ss.Spec.Template`, etc. Add the claim templates alongside.)

Add the following line right after the `ss.Spec.Template = ...` (or wherever the spec is assembled, alongside the other Spec fields):

```go
		ss.Spec.VolumeClaimTemplates = vc.Spec.VolumeClaimTemplates
```

If the spec is built via a struct literal rather than field-by-field assignment, add the field there instead.

- [ ] **Step 5: Re-run tests — they must pass**

```bash
go test ./internal/controller/ -run TestReconcileStatefulSet_ -v -count=1
```

Expected: all three new tests PASS, plus any pre-existing StatefulSet tests still green.

- [ ] **Step 6: Full package run**

```bash
go test ./internal/controller/ -count=1 -timeout 3m
```

Expected: `ok  github.com/bluedynamics/cloud-vinyl/internal/controller`.

- [ ] **Step 7: Commit**

```bash
git add internal/controller/statefulset.go internal/controller/statefulset_test.go
git commit -m "feat(controller): pass user volumes/mounts/claim-templates to StatefulSet

spec.pod.volumes and spec.pod.volumeMounts are now appended to the
operator-managed defaults when building the varnish container.
spec.volumeClaimTemplates are passed verbatim onto the StatefulSet
spec. Reserved names / paths are still accepted here; the admission
webhook (next commit) enforces the constraints.

Refs #43"
```

---

### Task 3: Webhook — reject reserved names, reserved mount paths, duplicates, and storage-path collisions

**Files:**
- Modify: `internal/webhook/vinylcache_validator.go`
- Modify: `internal/webhook/vinylcache_validator_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/webhook/vinylcache_validator_test.go`:

```go
func TestValidate_PodVolumeName_ReservedIsRejected(t *testing.T) {
	vc := validBaseVinylCache()
	vc.Spec.Pod.Volumes = []corev1.Volume{{
		Name:         "varnish-workdir", // reserved
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	}}
	_, err := webhook.ValidateVinylCache(vc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "varnish-workdir")
	assert.Contains(t, err.Error(), "reserved")
}

func TestValidate_VolumeClaimTemplateName_ReservedIsRejected(t *testing.T) {
	vc := validBaseVinylCache()
	vc.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{{
		ObjectMeta: metav1.ObjectMeta{Name: "bootstrap-vcl"}, // reserved
	}}
	_, err := webhook.ValidateVinylCache(vc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bootstrap-vcl")
}

func TestValidate_VolumeNameDuplicate_AcrossVolumesAndClaims_Rejected(t *testing.T) {
	vc := validBaseVinylCache()
	vc.Spec.Pod.Volumes = []corev1.Volume{{
		Name:         "cache-ssd",
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	}}
	vc.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{{
		ObjectMeta: metav1.ObjectMeta{Name: "cache-ssd"},
	}}
	_, err := webhook.ValidateVinylCache(vc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cache-ssd")
	assert.Contains(t, err.Error(), "duplicate")
}

func TestValidate_VolumeMountPath_ReservedIsRejected(t *testing.T) {
	vc := validBaseVinylCache()
	vc.Spec.Pod.Volumes = []corev1.Volume{{
		Name:         "my-ssd",
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	}}
	vc.Spec.Pod.VolumeMounts = []corev1.VolumeMount{
		{Name: "my-ssd", MountPath: "/var/lib/varnish"}, // reserved
	}
	_, err := webhook.ValidateVinylCache(vc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "/var/lib/varnish")
}

func TestValidate_VolumeMountName_Unresolvable_Rejected(t *testing.T) {
	vc := validBaseVinylCache()
	vc.Spec.Pod.VolumeMounts = []corev1.VolumeMount{
		{Name: "nonexistent", MountPath: "/data"},
	}
	_, err := webhook.ValidateVinylCache(vc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nonexistent")
	assert.Contains(t, err.Error(), "not declared")
}

func TestValidate_StoragePath_UnderReservedMount_Rejected(t *testing.T) {
	vc := validBaseVinylCache()
	vc.Spec.Storage = []v1alpha1.StorageSpec{{
		Name: "disk",
		Type: "file",
		Path: "/var/lib/varnish/spill.bin", // under reserved /var/lib/varnish
		Size: resource.MustParse("10Gi"),
	}}
	_, err := webhook.ValidateVinylCache(vc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "/var/lib/varnish")
	assert.Contains(t, err.Error(), "storage")
}

func TestValidate_StoragePath_UnderUserMount_Accepted(t *testing.T) {
	vc := validBaseVinylCache()
	vc.Spec.Pod.Volumes = []corev1.Volume{{
		Name:         "cache-ssd",
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	}}
	vc.Spec.Pod.VolumeMounts = []corev1.VolumeMount{
		{Name: "cache-ssd", MountPath: "/var/lib/varnish-cache"},
	}
	vc.Spec.Storage = []v1alpha1.StorageSpec{{
		Name: "disk",
		Type: "file",
		Path: "/var/lib/varnish-cache/spill.bin",
		Size: resource.MustParse("10Gi"),
	}}
	_, err := webhook.ValidateVinylCache(vc)
	require.NoError(t, err)
}
```

The existing test file likely does not have a `validBaseVinylCache()` helper. If the defaulter test file has `vcWithBackend()` or similar, reuse it. Otherwise, add at the top of the validator test file:

```go
func validBaseVinylCache() *v1alpha1.VinylCache {
	return &v1alpha1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "vc", Namespace: "ns"},
		Spec: v1alpha1.VinylCacheSpec{
			Replicas: 1,
			Image:    "varnish:7.6",
			Backends: []v1alpha1.BackendSpec{
				{Name: "app", ServiceRef: v1alpha1.ServiceRef{Name: "svc"}},
			},
		},
	}
}
```

Add required imports to the test file (`corev1`, `metav1`, `resource`).

- [ ] **Step 2: Run tests to confirm they fail**

```bash
go test ./internal/webhook/ -v -count=1 -run TestValidate_PodVolume\|TestValidate_VolumeClaim\|TestValidate_VolumeName\|TestValidate_VolumeMountPath\|TestValidate_VolumeMountName\|TestValidate_StoragePath
```

Expected: all 7 new tests FAIL (no validation logic exists yet).

- [ ] **Step 3: Implement the validation**

Open `internal/webhook/vinylcache_validator.go`. At the top of the file (below the existing `forbiddenStorageTypes` map), add:

```go
// reservedVolumeNames collide with operator-managed volumes on every pod.
// Users cannot reuse these names in spec.pod.volumes or
// spec.volumeClaimTemplates.
var reservedVolumeNames = map[string]bool{
	"agent-token":     true,
	"varnish-secret":  true,
	"varnish-workdir": true,
	"varnish-tmp":     true,
	"bootstrap-vcl":   true,
}

// reservedMountPaths are owned by the operator. spec.pod.volumeMounts
// must not mount into these, and spec.storage[].path must not place
// files under them.
var reservedMountPaths = []string{
	"/run/vinyl",
	"/etc/varnish/secret",
	"/etc/varnish/default.vcl",
	"/var/lib/varnish",
	"/tmp",
}

// pathIsReserved reports whether p equals a reserved mount path or sits
// under one (with a trailing slash boundary to avoid false positives
// like "/var/lib/varnish-cache" hitting "/var/lib/varnish").
func pathIsReserved(p string) bool {
	for _, r := range reservedMountPaths {
		if p == r {
			return true
		}
		if strings.HasPrefix(p, r+"/") {
			return true
		}
	}
	return false
}
```

Then in `ValidateVinylCache`, after the existing `allowedSources` CIDR validation block (immediately before the `if len(errs) > 0` tail), insert:

```go
	// Collect all user-declared volume names (pod.volumes + volumeClaimTemplates).
	declared := make(map[string]bool, len(vc.Spec.Pod.Volumes)+len(vc.Spec.VolumeClaimTemplates))

	for _, v := range vc.Spec.Pod.Volumes {
		if reservedVolumeNames[v.Name] {
			errs = append(errs, fmt.Sprintf(
				"spec.pod.volumes[%q]: name is reserved by the operator", v.Name))
			continue
		}
		if declared[v.Name] {
			errs = append(errs, fmt.Sprintf(
				"spec.pod.volumes[%q]: duplicate volume name", v.Name))
			continue
		}
		declared[v.Name] = true
	}

	for _, c := range vc.Spec.VolumeClaimTemplates {
		if reservedVolumeNames[c.Name] {
			errs = append(errs, fmt.Sprintf(
				"spec.volumeClaimTemplates[%q]: name is reserved by the operator", c.Name))
			continue
		}
		if declared[c.Name] {
			errs = append(errs, fmt.Sprintf(
				"spec.volumeClaimTemplates[%q]: duplicate — name already used in spec.pod.volumes or another claim template", c.Name))
			continue
		}
		declared[c.Name] = true
	}

	// Validate mount paths + that each mount resolves to a declared volume.
	for _, m := range vc.Spec.Pod.VolumeMounts {
		if pathIsReserved(m.MountPath) {
			errs = append(errs, fmt.Sprintf(
				"spec.pod.volumeMounts[%q]: mountPath %q is reserved by the operator",
				m.Name, m.MountPath))
		}
		if !declared[m.Name] && !reservedVolumeNames[m.Name] {
			errs = append(errs, fmt.Sprintf(
				"spec.pod.volumeMounts[%q]: volume is not declared in spec.pod.volumes or spec.volumeClaimTemplates",
				m.Name))
		}
	}

	// Validate spec.storage[].path does not write into a reserved mount.
	for _, s := range vc.Spec.Storage {
		if s.Type == "file" && pathIsReserved(s.Path) {
			errs = append(errs, fmt.Sprintf(
				"spec.storage[%q].path %q is reserved by the operator; mount your own volume and place the cache file there",
				s.Name, s.Path))
		}
	}
```

- [ ] **Step 4: Re-run tests — they must pass**

```bash
go test ./internal/webhook/ -count=1 -run TestValidate_PodVolume\|TestValidate_VolumeClaim\|TestValidate_VolumeName\|TestValidate_VolumeMountPath\|TestValidate_VolumeMountName\|TestValidate_StoragePath
```

Expected: 7/7 PASS.

- [ ] **Step 5: Run full webhook package — no regressions**

```bash
go test ./internal/webhook/ -count=1
```

Expected: `ok  github.com/bluedynamics/cloud-vinyl/internal/webhook`.

- [ ] **Step 6: Commit**

```bash
git add internal/webhook/vinylcache_validator.go internal/webhook/vinylcache_validator_test.go
git commit -m "feat(webhook): validate user volumes/mounts/claim-templates

Reject:
- reserved volume names (agent-token, varnish-secret, varnish-workdir,
  varnish-tmp, bootstrap-vcl) in spec.pod.volumes and
  spec.volumeClaimTemplates.
- duplicate names across spec.pod.volumes and spec.volumeClaimTemplates.
- spec.pod.volumeMounts with reserved mount paths (/run/vinyl,
  /etc/varnish/secret, /etc/varnish/default.vcl, /var/lib/varnish, /tmp)
  or with a name that does not resolve to any declared volume.
- spec.storage[type=file].path that writes into a reserved operator mount.

Refs #43"
```

---

### Task 4: E2E chainsaw test — PVC-backed file storage

**Files:**
- Create: `e2e/fixtures/vinylcaches/ssd-backed-storage.yaml`
- Create: `e2e/tests/volumes-and-pvc/chainsaw-test.yaml`

- [ ] **Step 1: Create the fixture**

Write `e2e/fixtures/vinylcaches/ssd-backed-storage.yaml`:

```yaml
apiVersion: vinyl.bluedynamics.eu/v1alpha1
kind: VinylCache
metadata:
  name: ssd-cache
spec:
  replicas: 1
  image: varnish:7.6
  backends:
    - name: app
      port: 80
      serviceRef:
        name: echo-backend
  storage:
    - name: disk
      type: file
      path: /var/lib/varnish-cache/spill.bin
      size: 100Mi
  pod:
    volumes:
      - name: cache-ssd
        emptyDir:
          sizeLimit: 200Mi
      - name: another-vol
        emptyDir: {}
    volumeMounts:
      - name: cache-ssd
        mountPath: /var/lib/varnish-cache
      - name: another-vol
        mountPath: /var/cache/extra
```

(Using `emptyDir` rather than a real PVC for E2E because Kind may not have a StorageClass. The path-passthrough behaviour is identical.)

- [ ] **Step 2: Create the chainsaw test**

Write `e2e/tests/volumes-and-pvc/chainsaw-test.yaml`:

```yaml
apiVersion: chainsaw.kyverno.io/v1alpha1
kind: Test
metadata:
  name: volumes-and-pvc
spec:
  description: |
    Verify spec.pod.volumes and spec.pod.volumeMounts are passed through
    to the generated StatefulSet and the VinylCache reaches Ready phase
    with a user-declared EmptyDir mounted under a path consumed by
    spec.storage[].path.
  timeouts:
    apply: 10s
    assert: 180s
    delete: 60s
    cleanup: 60s
    exec: 30s
  steps:
    - name: deploy-backend
      try:
        - apply:
            file: ../../fixtures/backends/echo-service.yaml
        - assert:
            resource:
              apiVersion: apps/v1
              kind: Deployment
              metadata:
                name: echo-backend
              status:
                readyReplicas: 1

    - name: deploy-cache-with-ssd-volume
      try:
        - apply:
            file: ../../fixtures/vinylcaches/ssd-backed-storage.yaml
        - assert:
            resource:
              apiVersion: vinyl.bluedynamics.eu/v1alpha1
              kind: VinylCache
              metadata:
                name: ssd-cache
              status:
                phase: Ready

    - name: verify-statefulset-has-user-volumes
      try:
        - assert:
            resource:
              apiVersion: apps/v1
              kind: StatefulSet
              metadata:
                name: ssd-cache
              spec:
                template:
                  spec:
                    (volumes[?name=='cache-ssd']):
                      - name: cache-ssd
                        emptyDir:
                          sizeLimit: 200Mi
                    (containers[?name=='varnish'].volumeMounts[?name=='cache-ssd']):
                      - - name: cache-ssd
                          mountPath: /var/lib/varnish-cache

    - name: cleanup
      try:
        - delete:
            file: ../../fixtures/vinylcaches/ssd-backed-storage.yaml
        - delete:
            file: ../../fixtures/backends/echo-service.yaml
```

- [ ] **Step 3: Quick syntax lint**

```bash
kubectl apply --dry-run=client --validate=false -f e2e/fixtures/vinylcaches/ssd-backed-storage.yaml
```

Expected: no syntax errors. (A full chainsaw run requires a Kind cluster; don't invoke it locally.)

- [ ] **Step 4: Commit**

```bash
git add e2e/fixtures/vinylcaches/ssd-backed-storage.yaml e2e/tests/volumes-and-pvc/chainsaw-test.yaml
git commit -m "test(e2e): exercise spec.pod.volumes + spec.storage interaction

Creates a VinylCache with a user EmptyDir volume mounted at a path that
spec.storage[type=file].path writes into. Asserts the generated
StatefulSet contains the user volume + mount, and the VinylCache reaches
Ready phase under this configuration.

Does not exercise volumeClaimTemplates (Kind has no default SSD
StorageClass in CI) — unit tests cover the passthrough.

Refs #43"
```

---

### Task 5: Documentation — how-to + reference

**Files:**
- Create: `docs/sources/how-to/ssd-backed-storage.md`
- Modify: `docs/sources/how-to/index.md`
- Modify: `docs/sources/reference/vinylcache-spec.md`

- [ ] **Step 1: Create the how-to**

Write `docs/sources/how-to/ssd-backed-storage.md`:

````markdown
# Back spec.storage[].type=file with a user-provisioned volume

By default, `spec.storage[].type=file` lands the cache spill file in the operator's `/var/lib/varnish` EmptyDir. That works for small working sets on nodes with SSD-backed ephemeral storage, but has limits: no persistence across pod restart, no `sizeLimit`, no choice of StorageClass, no isolation from node ephemeral use (logs, image layers).

This guide shows how to back the spill file with (1) an EmptyDir with an explicit size limit, or (2) a StorageClass-provisioned PVC per pod.

## Option 1 — EmptyDir with sizeLimit (simplest)

Useful when you want to cap cache disk use and isolate it from node ephemeral capacity, without per-pod persistence.

```yaml
apiVersion: vinyl.bluedynamics.eu/v1alpha1
kind: VinylCache
metadata:
  name: my-cache
spec:
  replicas: 2
  image: varnish:7.6
  backends:
    - name: app
      serviceRef:
        name: app-service
      port: 8080
  storage:
    - name: mem
      type: malloc
      size: 1Gi
    - name: disk
      type: file
      path: /var/lib/varnish-cache/spill.bin
      size: 50Gi
  pod:
    volumes:
      - name: cache-scratch
        emptyDir:
          sizeLimit: 60Gi
    volumeMounts:
      - name: cache-scratch
        mountPath: /var/lib/varnish-cache
```

The file lives on node ephemeral storage, but capped at 60 GiB regardless of how much the node has.

## Option 2 — PVC per pod via `volumeClaimTemplates`

Useful for persistence across pod restarts (faster warmup after a roll), or for isolating cache I/O onto a dedicated SSD StorageClass (`hcloud-volumes`, `gp3`, a CSI-driver-provisioned NVMe, etc.):

```yaml
apiVersion: vinyl.bluedynamics.eu/v1alpha1
kind: VinylCache
metadata:
  name: my-cache
spec:
  replicas: 2
  image: varnish:7.6
  backends:
    - name: app
      serviceRef:
        name: app-service
      port: 8080
  storage:
    - name: mem
      type: malloc
      size: 1Gi
    - name: disk
      type: file
      path: /var/lib/varnish-cache/spill.bin
      size: 80Gi
  volumeClaimTemplates:
    - metadata:
        name: cache-ssd
      spec:
        accessModes: [ReadWriteOnce]
        storageClassName: hcloud-volumes
        resources:
          requests:
            storage: 100Gi
  pod:
    volumeMounts:
      - name: cache-ssd
        mountPath: /var/lib/varnish-cache
```

The StatefulSet creates one PVC per replica — `cache-ssd-my-cache-0`, `cache-ssd-my-cache-1`, etc. They persist across pod deletion (StatefulSet semantics) so a rolling restart doesn't throw away the warmed cache.

Make sure `spec.storage[].size` is well under the PVC size (Varnish needs filesystem overhead — allow ~20%).

## Reserved names and paths

These names cannot be used for your volumes (they collide with operator-managed volumes):

- `agent-token`, `varnish-secret`, `varnish-workdir`, `varnish-tmp`, `bootstrap-vcl`

These mount paths are reserved by the operator:

- `/run/vinyl`, `/etc/varnish/secret`, `/etc/varnish/default.vcl`, `/var/lib/varnish`, `/tmp`

`spec.storage[].path` cannot live under a reserved mount — you must declare your own `pod.volumeMounts` entry that covers the path.

The admission webhook rejects violations with a clear message.
````

- [ ] **Step 2: Add the new page to the how-to index**

In `docs/sources/how-to/index.md`, add `ssd-backed-storage` to the toctree (alphabetical if the file uses it, otherwise immediately after `create-cache`).

- [ ] **Step 3: Extend the spec reference**

In `docs/sources/reference/vinylcache-spec.md`, locate the `pod` section (it currently documents `annotations`, `labels`, `nodeSelector`, `tolerations`, `affinity`, `priorityClassName`). Append the two new fields:

```markdown
| `volumes` | list | `[]` | Additional pod volumes appended to operator-managed defaults. Reserved names rejected by webhook. See [SSD-backed storage how-to](../how-to/ssd-backed-storage.md). |
| `volumeMounts` | list | `[]` | Additional mounts on the varnish container. Each `name` must reference a `spec.pod.volumes` entry or a `spec.volumeClaimTemplates` claim name. Reserved mount paths rejected by webhook. |
```

In the top-level spec table, add:

```markdown
| `volumeClaimTemplates` | list | `[]` | StatefulSet-native per-replica PVC templates. Reference the claim `name` from `spec.pod.volumeMounts`. |
```

- [ ] **Step 4: Build the docs**

```bash
cd docs && make docs
```

Expected: `build succeeded.` No Sphinx warnings.

- [ ] **Step 5: Commit**

```bash
git add docs/sources/how-to/ssd-backed-storage.md docs/sources/how-to/index.md docs/sources/reference/vinylcache-spec.md
git commit -m "docs: how-to for SSD-backed spec.storage via pod volumes / PVC

New how-to covers EmptyDir-with-sizeLimit and StatefulSet
volumeClaimTemplates (per-pod PVC) as ways to back
spec.storage[type=file].path. Reference table gains entries for
spec.pod.volumes, spec.pod.volumeMounts, and spec.volumeClaimTemplates.

Refs #43"
```

---

### Task 6: End-to-end verification + PR

**Files:** none; commands only.

- [ ] **Step 1: Full test suite**

```bash
go test ./... -count=1 -timeout 5m
```

Expected:

```
ok  github.com/bluedynamics/cloud-vinyl/internal/agent
ok  github.com/bluedynamics/cloud-vinyl/internal/controller
ok  github.com/bluedynamics/cloud-vinyl/internal/generator
ok  github.com/bluedynamics/cloud-vinyl/internal/monitoring
ok  github.com/bluedynamics/cloud-vinyl/internal/proxy
ok  github.com/bluedynamics/cloud-vinyl/internal/webhook
ok  github.com/bluedynamics/cloud-vinyl/internal/webhook/v1alpha1
```

- [ ] **Step 2: Helm chart validation**

```bash
helm lint charts/cloud-vinyl
helm unittest charts/cloud-vinyl
```

Expected: chart lints clean, unit tests pass. Confirms the Helm-chart CRD sync from Task 1 Step 4 didn't break anything.

- [ ] **Step 3: Push branch and open PR**

```bash
git push -u origin feat/pod-volumes-and-pvc
gh pr create --title "feat: spec.pod.volumes + spec.volumeClaimTemplates for SSD-backed storage (#43)" --body "$(cat <<'BODY'
## Summary

Adds three optional CRD fields that let users back \`spec.storage[type=file].path\` with a user-provisioned volume instead of the fixed operator EmptyDir:

- \`spec.pod.volumes []corev1.Volume\` — appended to operator-managed pod volumes.
- \`spec.pod.volumeMounts []corev1.VolumeMount\` — appended to the varnish container.
- \`spec.volumeClaimTemplates []corev1.PersistentVolumeClaim\` — passed verbatim onto the StatefulSet spec, so each replica gets its own PVC.

## Validation

Admission webhook rejects:
- Reserved volume names (\`agent-token\`, \`varnish-secret\`, \`varnish-workdir\`, \`varnish-tmp\`, \`bootstrap-vcl\`) in \`spec.pod.volumes\` or \`spec.volumeClaimTemplates\`.
- Duplicate names across \`spec.pod.volumes\` and \`spec.volumeClaimTemplates\`.
- \`spec.pod.volumeMounts\` entries with reserved paths or names that don't resolve to a declared volume.
- \`spec.storage[type=file].path\` pointing under a reserved operator mount (\`/var/lib/varnish\`, \`/tmp\`, \`/run/vinyl\`, \`/etc/varnish/*\`).

## Tests

- Unit (controller): volume/mount passthrough, PVC template passthrough, no-op path when user doesn't set any of the fields.
- Unit (webhook): 7 cases covering reserved names, reserved mount paths, duplicates, unresolvable mount refs, storage-path collisions (both rejection and the accept-when-covered path).
- E2E chainsaw: creates a VinylCache with a sized EmptyDir backing the spill path; verifies the StatefulSet has the user volume + mount and Phase reaches Ready.

## Docs

New how-to: \`docs/sources/how-to/ssd-backed-storage.md\` covers EmptyDir-with-sizeLimit and PVC-per-pod via \`volumeClaimTemplates\`. Spec reference gains entries for the three fields.

## Out of scope

- Multi-tier eviction, cache warming, snapshotting.
- Automating \`charts/cloud-vinyl/crds/vinylcache.yaml\` sync (tracked in #34).
- The \`s0\` storage-name collision with the stock image entrypoint (tracked in #44).

Closes #43

🤖 Generated with [Claude Code](https://claude.com/claude-code)
BODY
)"
```

Expected: PR URL printed.

---

## Release hook-in

No version bump is cut here. After merge, bundle with other feature work under the next feature release (likely `v0.5.0`, since this adds new CRD surface — kubebuilder enum/defaults are not changed, so strictly additive; could also ship as `v0.4.3` if no other breaking-ish items queue up first).
