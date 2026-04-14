# Per-Backend Directors with EndpointSlice Expansion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix guru-meditation during backend rollouts (issue #11) by expanding `serviceRef` into per-pod backends, grouping them under a per-backend Varnish director, and reconciling on EndpointSlice changes — so Varnish sees per-pod health and can fail over when kube-proxy lags during rolling deploys.

**Architecture:** Two coupled changes. (1) The controller resolves `serviceRef` by listing `EndpointSlice` objects in the backend's namespace (label `kubernetes.io/service-name=<svc>`), filtering `Ready=true && Terminating=false`, and returning one `generator.Endpoint` per pod. A new `EndpointSlice` watch fires reconciliation when pods change. In-memory debouncing (default 1s) prevents VCL thrashing during rollouts. (2) The generator groups backends by backend-spec name, and for each group emits a Varnish director (default `shard`) in `vcl_init` that aggregates the per-pod backends. VCL snippets reference `<backend-name>.backend()` instead of the old `<backend-name>_0`.

**Tech Stack:** Go 1.25, controller-runtime, Kubebuilder markers + CRDs, Varnish VCL 4.1 + vmod `directors`, Chainsaw for E2E.

**Breaking change**: existing VCL snippets that hardcode `<name>_0` break at rollout (the `_0` pod may not exist). Release notes must call this out and show the migration (`<name>.backend()`).

**Referenced upstream context:**
- GitHub issue: https://github.com/bluedynamics/cloud-vinyl/issues/11
- Related memory: `kube-httpcache` #66 "Kein Debouncing bei Endpoint-Churn" — avoid repeating.

---

## File Structure

### Create
- `internal/controller/endpoints.go` — per-backend EndpointSlice resolution + Terminating filter.
- `internal/controller/debounce.go` — in-memory debounce state per `NamespacedName`.
- `internal/controller/endpoints_test.go` — unit tests with fake client for resolution + filters.
- `internal/controller/debounce_test.go` — unit tests for debounce state machine.
- `e2e/tests/backend-rollout/chainsaw-test.yaml` — E2E: 6-pod backend rollout under load.
- `e2e/tests/backend-rollout/*` — chainsaw fixtures (VinylCache, backend Deployment/Service, probe loop).
- `docs/source/user-guide/backend-directors.md` — user docs for the new behaviour + snippet migration.
- `docs/source/release-notes.md` entry (append) — breaking-change callout.

### Modify
- `api/v1alpha1/vinylcache_types.go` — add `BackendSpec.Director *DirectorSpec`.
- `api/v1alpha1/zz_generated_deepcopy.go` — regenerated via `make generate`.
- `config/crd/bases/vinyl.bluedynamics.eu_vinylcaches.yaml` — regenerated via `make manifests`.
- `internal/generator/generator.go` — replace flat `BackendDefs` with `BackendGroups []BackendGroup`.
- `internal/generator/templates/backends.vcl.tmpl` — iterate `.BackendGroups[].Backends`.
- `internal/generator/templates/vcl_init.vcl.tmpl` — emit per-backend director + `add_backend` calls.
- `internal/generator/generator_test.go` — update existing tests, add group + director tests.
- `internal/generator/testdata/*.expected.vcl` — refresh golden files.
- `internal/controller/vinylcache_controller.go` — drop the Service-DNS single-endpoint path, call new resolver; add `EndpointSlice` watch + mapping function.
- `internal/controller/vcl_push.go` — replace stub `debounceRemaining` with real implementation backed by `debounce.go`; note new endpoint-change timestamp entry point.
- `internal/controller/vinylcache_controller_test.go` — extend envtest to cover EndpointSlice-driven reconciliation.
- `internal/webhook/vinylcache_validator.go` — validate per-backend director enum + shard params (mirror existing top-level validation).
- `internal/webhook/vinylcache_validator_test.go` — add test cases.

### Delete
- None.

### Rationale for decomposition

Per-backend director data (CRD + generator types + templates) is one vertical slice. Endpoint resolution (controller + watch + debounce) is another. We implement them bottom-up: CRD → generator types → templates → generator tests; then controller resolver → watch → debounce → controller tests; then webhook, E2E, docs. Each commit leaves `main` green.

---

## Task 1: CRD — add per-backend `Director` field

**Files:**
- Modify: `api/v1alpha1/vinylcache_types.go:113-140` (BackendSpec struct)
- Modify (regenerated): `api/v1alpha1/zz_generated_deepcopy.go`
- Modify (regenerated): `config/crd/bases/vinyl.bluedynamics.eu_vinylcaches.yaml`

- [ ] **Step 1: Add the field on `BackendSpec`**

Open `api/v1alpha1/vinylcache_types.go` and add the `Director` field at the end of `BackendSpec` (after `ConnectionParameters`):

```go
// director overrides the cluster-wide director for this backend only.
// If nil, a shard director with defaults is generated, grouping all resolved
// per-pod backends for this serviceRef. Use "round_robin" or "random" if
// consistent hashing is undesirable (e.g. stateless backends).
// +optional
Director *DirectorSpec `json:"director,omitempty"`
```

- [ ] **Step 2: Regenerate deepcopy + manifests**

Run:

```bash
make generate manifests
```

Expected: `zz_generated_deepcopy.go` updated (`BackendSpec.DeepCopyInto` now copies `Director`), and `config/crd/bases/vinyl.bluedynamics.eu_vinylcaches.yaml` gains the nested schema for `.spec.backends[].director`.

- [ ] **Step 3: Verify regeneration**

```bash
go build ./...
```

Expected: clean build.

- [ ] **Step 4: Commit**

```bash
git add api/v1alpha1/vinylcache_types.go api/v1alpha1/zz_generated_deepcopy.go config/crd
git commit -m "feat(api): add per-backend director override field

Adds optional spec.backends[].director (reusing DirectorSpec) so each
backend can select its own director algorithm. Default (nil) will be
rendered as a shard director. Paves the way for per-endpoint backend
expansion in the generator.

Refs #11"
```

---

## Task 2: Generator — introduce `BackendGroup` type

**Files:**
- Modify: `internal/generator/generator.go:60-208`
- Modify: `internal/generator/generator_test.go` (adjust test fixtures that reference `BackendDefs`)

- [ ] **Step 1: Write a failing test for group emission**

Append to `internal/generator/generator_test.go`:

```go
func TestGenerate_BackendGroups_PerBackendDirector(t *testing.T) {
	g := newGenerator(t)
	input := generator.Input{
		Namespace: "ns",
		Name:      "cache",
		Spec: &vinylv1alpha1.VinylCacheSpec{
			Replicas: 1,
			Image:    "vinyl:test",
			Backends: []vinylv1alpha1.BackendSpec{
				{Name: "plone", ServiceRef: vinylv1alpha1.ServiceRef{Name: "plone-svc"}},
			},
		},
		Endpoints: map[string][]generator.Endpoint{
			"plone": {
				{IP: "10.0.0.1", Port: 8080},
				{IP: "10.0.0.2", Port: 8080},
				{IP: "10.0.0.3", Port: 8080},
			},
		},
	}
	r, err := g.Generate(input)
	require.NoError(t, err)

	// Per-pod backend blocks.
	assert.Contains(t, r.VCL, `backend plone_0 {`)
	assert.Contains(t, r.VCL, `backend plone_1 {`)
	assert.Contains(t, r.VCL, `backend plone_2 {`)

	// Director init for this backend group.
	assert.Contains(t, r.VCL, "new plone = directors.shard();",
		"default director must be shard")
	assert.Contains(t, r.VCL, "plone.add_backend(plone_0);")
	assert.Contains(t, r.VCL, "plone.add_backend(plone_1);")
	assert.Contains(t, r.VCL, "plone.add_backend(plone_2);")
	assert.Contains(t, r.VCL, "plone.reconfigure();")
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/generator/ -run TestGenerate_BackendGroups_PerBackendDirector -v
```

Expected: FAIL — `new plone = directors.shard();` not found (no per-backend director emitted yet).

- [ ] **Step 3: Add `BackendGroup` type and builder**

In `internal/generator/generator.go`, replace the `BackendDefs` field on `TemplateData` and the builder logic:

```go
// BackendGroup is one CRD backend (spec.backends[i]) expanded to its per-pod backends,
// with the director algorithm that groups them in vcl_init.
type BackendGroup struct {
	Name     string       // VCL identifier; matches BackendSpec.Name (sanitized).
	Director DirectorInfo // Director algorithm + params for this group.
	Backends []BackendDef // One per resolved Endpoint; Name is "<Group.Name>_<idx>".
}

// DirectorInfo captures the resolved director config for a backend group.
// It reflects the v1alpha1.DirectorSpec but is template-friendly.
type DirectorInfo struct {
	Type   string  // "shard" (default), "round_robin", "random", "fallback".
	Warmup float64 // 0.0 if unset; only for shard.
	Rampup string  // empty if unset; formatted via fmtDuration; only for shard.
	By     string  // "HASH" (default) or "URL"; only for shard.
}
```

Update `TemplateData`:

```go
type TemplateData struct {
	Input
	HasCluster       bool
	HasESI           bool
	HasXkey          bool
	HasSoftPurge     bool
	HasProxyProtocol bool
	HasFullOverride  bool
	VCLName          string
	BackendGroups    []BackendGroup // NEW — replaces BackendDefs.
	PeerDefs         []BackendDef
	UseShardDirector bool
	DirectorName     string // cluster-peer director name (unchanged).
}
```

Rewrite the "Build backend defs" block inside `buildTemplateData`:

```go
for _, b := range input.Spec.Backends {
	group := BackendGroup{
		Name:     sanitizeName(b.Name),
		Director: resolveDirectorInfo(b.Director),
	}
	for i, ep := range input.Endpoints[b.Name] {
		def := BackendDef{
			Name: fmt.Sprintf("%s_%d", group.Name, i),
			IP:   ep.IP,
			Port: ep.Port,
		}
		if b.Probe != nil && b.Probe.URL != "" {
			def.ProbeURL = b.Probe.URL
		}
		if b.ConnectionParameters != nil {
			cp := b.ConnectionParameters
			if cp.ConnectTimeout.Duration > 0 {
				def.ConnectTimeout = fmtDuration(cp.ConnectTimeout.Duration)
			}
			if cp.FirstByteTimeout.Duration > 0 {
				def.FirstByteTimeout = fmtDuration(cp.FirstByteTimeout.Duration)
			}
			if cp.BetweenBytesTimeout.Duration > 0 {
				def.BetweenBytesTimeout = fmtDuration(cp.BetweenBytesTimeout.Duration)
			}
			if cp.IdleTimeout.Duration > 0 {
				def.IdleTimeout = fmtDuration(cp.IdleTimeout.Duration)
			}
			def.MaxConnections = cp.MaxConnections
		}
		group.Backends = append(group.Backends, def)
	}
	data.BackendGroups = append(data.BackendGroups, group)
}
```

Add the helper at the bottom of the file (above `fmtDuration`):

```go
// resolveDirectorInfo collapses a nullable per-backend DirectorSpec into a template-ready
// DirectorInfo with defaults (shard / HASH / empty warmup/rampup).
func resolveDirectorInfo(ds *vinylv1alpha1.DirectorSpec) DirectorInfo {
	out := DirectorInfo{Type: "shard", By: "HASH"}
	if ds == nil {
		return out
	}
	if ds.Type != "" {
		out.Type = ds.Type
	}
	if ds.Shard != nil {
		if ds.Shard.Warmup != nil {
			out.Warmup = *ds.Shard.Warmup
		}
		if ds.Shard.Rampup.Duration > 0 {
			out.Rampup = fmtDuration(ds.Shard.Rampup.Duration)
		}
		if ds.Shard.By != "" {
			out.By = ds.Shard.By
		}
	}
	return out
}
```

- [ ] **Step 4: Fix compilation — remove stale `BackendDefs` references**

The tests and existing templates still reference `BackendDefs`. Run:

```bash
go build ./... 2>&1 | head -30
```

Update every `.BackendDefs` / `data.BackendDefs` compile error in non-template Go to use `.BackendGroups` (templates are handled in Task 3). In tests, change `input.Endpoints["foo"] = ...` fixtures as needed — no test currently uses `BackendDefs` directly, but verify with:

```bash
grep -n "BackendDefs" internal/generator/*.go
```

Expected after fix: only references in generated `BackendGroup.Backends` iteration.

- [ ] **Step 5: Commit (tests still failing; templates come next)**

```bash
git add internal/generator/generator.go internal/generator/generator_test.go
git commit -m "refactor(generator): group backends by spec name with director info

BackendGroup holds all resolved per-pod backends for one spec.backends[i],
plus the director algorithm to emit in vcl_init. Templates updated in
the following commit. Tests still red pending template work.

Refs #11"
```

---

## Task 3: Templates — emit per-backend director + iterate groups

**Files:**
- Modify: `internal/generator/templates/backends.vcl.tmpl`
- Modify: `internal/generator/templates/vcl_init.vcl.tmpl`
- Modify: `internal/generator/testdata/*.expected.vcl` (golden files)

- [ ] **Step 1: Rewrite `backends.vcl.tmpl`**

Replace the whole file with:

```
{{- range .BackendGroups }}
{{- range .Backends }}
backend {{ .Name }} {
    .host = "{{ .IP }}";
    .port = "{{ .Port }}";
{{- if .ProbeURL }}
    .probe = {
        .url = "{{ .ProbeURL }}";
        .interval = 5s;
        .timeout = 2s;
        .window = 5;
        .threshold = 3;
    }
{{- end }}
{{- if .ConnectTimeout }}
    .connect_timeout = {{ .ConnectTimeout }};
{{- end }}
{{- if .FirstByteTimeout }}
    .first_byte_timeout = {{ .FirstByteTimeout }};
{{- end }}
{{- if .BetweenBytesTimeout }}
    .between_bytes_timeout = {{ .BetweenBytesTimeout }};
{{- end }}
{{- if .MaxConnections }}
    .max_connections = {{ .MaxConnections }};
{{- end }}
}
{{ end }}
{{- end }}
{{- if .HasCluster }}
{{ range .PeerDefs }}
backend {{ .Name }} {
    .host = "{{ .IP }}";
    .port = "{{ .Port }}";
}
{{ end }}
{{- end }}
```

- [ ] **Step 2: Rewrite `vcl_init.vcl.tmpl` to emit per-backend directors**

Replace with:

```
sub vcl_init {
{{- range .BackendGroups }}
{{- if eq .Director.Type "shard" }}
    new {{ .Name }} = directors.shard();
{{- range .Backends }}
    {{ $.CurrentGroupName }}{{ /* placeholder-not-used, see below */ -}}
{{- end }}
{{- end }}
{{- end }}
    return(ok);
}
```

That won't work because `range` changes the `.` context — we need the outer group name inside inner range. Use a `{{ with }}` + explicit variables:

Replace the whole file with:

```
sub vcl_init {
{{- range $g := .BackendGroups }}
{{- if eq $g.Director.Type "shard" }}
    new {{ $g.Name }} = directors.shard();
{{- range $b := $g.Backends }}
    {{ $g.Name }}.add_backend({{ $b.Name }});
{{- end }}
{{- if $g.Director.Warmup }}
    {{ $g.Name }}.set_warmup({{ $g.Director.Warmup }});
{{- end }}
{{- if $g.Director.Rampup }}
    {{ $g.Name }}.set_rampup({{ $g.Director.Rampup }});
{{- end }}
    {{ $g.Name }}.reconfigure();
{{- else if eq $g.Director.Type "round_robin" }}
    new {{ $g.Name }} = directors.round_robin();
{{- range $b := $g.Backends }}
    {{ $g.Name }}.add_backend({{ $b.Name }});
{{- end }}
{{- else if eq $g.Director.Type "random" }}
    new {{ $g.Name }} = directors.random();
{{- range $b := $g.Backends }}
    {{ $g.Name }}.add_backend({{ $b.Name }}, 1.0);
{{- end }}
{{- else if eq $g.Director.Type "fallback" }}
    new {{ $g.Name }} = directors.fallback();
{{- range $b := $g.Backends }}
    {{ $g.Name }}.add_backend({{ $b.Name }});
{{- end }}
{{- end }}
{{- end }}
{{- if .HasCluster }}
    new {{ .DirectorName }} = directors.shard();
{{- range .PeerDefs }}
    {{ $.DirectorName }}.add_backend({{ .Name }});
{{- end }}
{{- if .Spec.Director.Shard }}
{{- if .Spec.Director.Shard.Warmup }}
    {{ $.DirectorName }}.set_warmup({{ deref .Spec.Director.Shard.Warmup }});
{{- end }}
{{- if .Spec.Director.Shard.Rampup.Duration }}
    {{ $.DirectorName }}.set_rampup({{ fmtDuration .Spec.Director.Shard.Rampup.Duration }});
{{- end }}
{{- end }}
    {{ .DirectorName }}.reconfigure();
{{- end }}
{{ if .Spec.VCL.Snippets.VCLInit }}
    # --- custom vcl_init snippet ---
    {{ .Spec.VCL.Snippets.VCLInit }}
    # --- end custom vcl_init snippet ---
{{ end }}
    return(ok);
}
```

- [ ] **Step 3: Run the new test and the existing suite**

```bash
go test ./internal/generator/ -v -run TestGenerate_BackendGroups_PerBackendDirector
```

Expected: PASS.

```bash
go test ./internal/generator/ -v
```

Expected: some golden-file tests fail with diff. Refresh each golden file by reading the actual output and updating `internal/generator/testdata/*.expected.vcl`. Use:

```bash
go test ./internal/generator/ -v 2>&1 | grep -E "(FAIL|expected|actual)" | head -40
```

For each diff, paste the new expected VCL into the corresponding `.expected.vcl` file. Rerun until green.

- [ ] **Step 4: Add a round_robin + shard params test**

Append to `generator_test.go`:

```go
func TestGenerate_BackendGroups_RoundRobin(t *testing.T) {
	g := newGenerator(t)
	input := generator.Input{
		Namespace: "ns", Name: "cache",
		Spec: &vinylv1alpha1.VinylCacheSpec{
			Replicas: 1, Image: "vinyl:test",
			Backends: []vinylv1alpha1.BackendSpec{{
				Name:       "api",
				ServiceRef: vinylv1alpha1.ServiceRef{Name: "api-svc"},
				Director:   &vinylv1alpha1.DirectorSpec{Type: "round_robin"},
			}},
		},
		Endpoints: map[string][]generator.Endpoint{
			"api": {{IP: "10.0.0.1", Port: 80}, {IP: "10.0.0.2", Port: 80}},
		},
	}
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "new api = directors.round_robin();")
	assert.Contains(t, r.VCL, "api.add_backend(api_0);")
	assert.Contains(t, r.VCL, "api.add_backend(api_1);")
	assert.NotContains(t, r.VCL, "api.reconfigure();",
		"round_robin: no reconfigure() call")
}
```

```bash
go test ./internal/generator/ -v -run TestGenerate_BackendGroups_RoundRobin
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/generator/templates internal/generator/generator_test.go internal/generator/testdata
git commit -m "feat(generator): emit per-backend director in vcl_init

Each backend group now renders its own director (shard/round_robin/
random/fallback) that groups the per-pod backends. Default is shard
with HASH-by, matching Plone-friendly sticky caching. User VCL
snippets reference <backend>.backend() instead of <backend>_0.

Refs #11"
```

---

## Task 4: Controller — resolve endpoints via EndpointSlice

**Files:**
- Create: `internal/controller/endpoints.go`
- Create: `internal/controller/endpoints_test.go`
- Modify: `internal/controller/vinylcache_controller.go` (remove old `resolveBackendEndpoints`, call new resolver)

- [ ] **Step 1: Write the failing test**

Create `internal/controller/endpoints_test.go`:

```go
package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
)

func ptrBool(b bool) *bool    { return &b }
func ptrInt32(i int32) *int32 { return &i }
func ptrString(s string) *string { return &s }

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, discoveryv1.AddToScheme(s))
	require.NoError(t, v1alpha1.AddToScheme(s))
	return s
}

func TestResolveBackendEndpoints_MultipleReadyPods(t *testing.T) {
	sch := newScheme(t)
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "plone-svc", Namespace: "app"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 8080}}},
	}
	es := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "plone-svc-abc",
			Namespace: "app",
			Labels:    map[string]string{"kubernetes.io/service-name": "plone-svc"},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Ports: []discoveryv1.EndpointPort{
			{Name: ptrString("http"), Port: ptrInt32(8080)},
		},
		Endpoints: []discoveryv1.Endpoint{
			{Addresses: []string{"10.0.0.1"}, Conditions: discoveryv1.EndpointConditions{Ready: ptrBool(true)}},
			{Addresses: []string{"10.0.0.2"}, Conditions: discoveryv1.EndpointConditions{Ready: ptrBool(true)}},
			{Addresses: []string{"10.0.0.3"}, Conditions: discoveryv1.EndpointConditions{Ready: ptrBool(false)}},
			{Addresses: []string{"10.0.0.4"}, Conditions: discoveryv1.EndpointConditions{Ready: ptrBool(true), Terminating: ptrBool(true)}},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(svc, es).Build()
	r := &VinylCacheReconciler{Client: cli, Scheme: sch}
	vc := &v1alpha1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "cache", Namespace: "app"},
		Spec: v1alpha1.VinylCacheSpec{
			Backends: []v1alpha1.BackendSpec{{
				Name: "plone", ServiceRef: v1alpha1.ServiceRef{Name: "plone-svc"},
			}},
		},
	}
	out, err := r.resolveBackendEndpoints(context.Background(), vc)
	require.NoError(t, err)
	require.Len(t, out["plone"], 2, "only Ready=true && Terminating=false endpoints")
	ips := []string{out["plone"][0].IP, out["plone"][1].IP}
	assert.ElementsMatch(t, []string{"10.0.0.1", "10.0.0.2"}, ips)
	assert.Equal(t, 8080, out["plone"][0].Port)
}

func TestResolveBackendEndpoints_NoEndpointsReturnsEmpty(t *testing.T) {
	sch := newScheme(t)
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "plone-svc", Namespace: "app"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 8080}}},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(svc).Build()
	r := &VinylCacheReconciler{Client: cli, Scheme: sch}
	vc := &v1alpha1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "cache", Namespace: "app"},
		Spec: v1alpha1.VinylCacheSpec{
			Backends: []v1alpha1.BackendSpec{{
				Name: "plone", ServiceRef: v1alpha1.ServiceRef{Name: "plone-svc"},
			}},
		},
	}
	out, err := r.resolveBackendEndpoints(context.Background(), vc)
	require.NoError(t, err)
	assert.Empty(t, out["plone"])
}

func TestResolveBackendEndpoints_PortOverride(t *testing.T) {
	sch := newScheme(t)
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "plone-svc", Namespace: "app"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 8080}}},
	}
	es := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "plone-svc-a",
			Namespace: "app",
			Labels:    map[string]string{"kubernetes.io/service-name": "plone-svc"},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Ports: []discoveryv1.EndpointPort{{Name: ptrString("http"), Port: ptrInt32(8080)}},
		Endpoints: []discoveryv1.Endpoint{
			{Addresses: []string{"10.0.0.1"}, Conditions: discoveryv1.EndpointConditions{Ready: ptrBool(true)}},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(svc, es).Build()
	r := &VinylCacheReconciler{Client: cli, Scheme: sch}
	vc := &v1alpha1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "cache", Namespace: "app"},
		Spec: v1alpha1.VinylCacheSpec{
			Backends: []v1alpha1.BackendSpec{{
				Name: "plone", Port: 9999,
				ServiceRef: v1alpha1.ServiceRef{Name: "plone-svc"},
			}},
		},
	}
	out, err := r.resolveBackendEndpoints(context.Background(), vc)
	require.NoError(t, err)
	require.Len(t, out["plone"], 1)
	assert.Equal(t, 9999, out["plone"][0].Port,
		"spec.backends[].port must override EndpointSlice port")
}
```

- [ ] **Step 2: Run — should fail to compile (new resolver not yet written)**

```bash
go test ./internal/controller/ -run TestResolveBackendEndpoints -v
```

Expected: FAIL (compilation error or missing behaviour, depending on current signature).

- [ ] **Step 3: Implement `endpoints.go`**

Create `internal/controller/endpoints.go`:

```go
/*
Copyright 2026. Licensed under the Apache License, Version 2.0.
*/

package controller

import (
	"context"
	"fmt"

	discoveryv1 "k8s.io/api/discovery/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
	"github.com/bluedynamics/cloud-vinyl/internal/generator"
)

// resolveBackendEndpoints returns, for each spec.backend, the list of Ready and
// non-Terminating per-pod endpoints discovered via EndpointSlice.
//
// Returning an empty slice for a backend is valid — the generator will emit
// a director with no add_backend() calls, which Varnish flags as "no healthy
// backends" until the next endpoint change triggers a reconcile. Callers must
// not treat missing endpoints as an error.
func (r *VinylCacheReconciler) resolveBackendEndpoints(
	ctx context.Context,
	vc *v1alpha1.VinylCache,
) (map[string][]generator.Endpoint, error) {
	out := make(map[string][]generator.Endpoint, len(vc.Spec.Backends))
	for _, b := range vc.Spec.Backends {
		eps, err := r.listBackendEndpoints(ctx, vc.Namespace, b)
		if err != nil {
			return nil, fmt.Errorf("backend %q: %w", b.Name, err)
		}
		out[b.Name] = eps
	}
	return out, nil
}

func (r *VinylCacheReconciler) listBackendEndpoints(
	ctx context.Context,
	namespace string,
	b v1alpha1.BackendSpec,
) ([]generator.Endpoint, error) {
	list := &discoveryv1.EndpointSliceList{}
	if err := r.List(ctx, list,
		client.InNamespace(namespace),
		client.MatchingLabels{discoveryv1.LabelServiceName: b.ServiceRef.Name},
	); err != nil {
		return nil, fmt.Errorf("listing EndpointSlices for service %s: %w", b.ServiceRef.Name, err)
	}

	var endpoints []generator.Endpoint
	for _, slice := range list.Items {
		port := pickPort(slice.Ports, b)
		if port == 0 {
			continue
		}
		for _, ep := range slice.Endpoints {
			if !endpointReady(ep) {
				continue
			}
			for _, addr := range ep.Addresses {
				endpoints = append(endpoints, generator.Endpoint{IP: addr, Port: port})
			}
		}
	}
	return endpoints, nil
}

// endpointReady returns true only when Ready=true and Terminating is not true.
// A nil Ready pointer is treated as ready (pre-1.22 fallback).
func endpointReady(ep discoveryv1.Endpoint) bool {
	if ep.Conditions.Terminating != nil && *ep.Conditions.Terminating {
		return false
	}
	if ep.Conditions.Ready == nil {
		return true
	}
	return *ep.Conditions.Ready
}

// pickPort selects the port for a backend: spec.backends[].port overrides the
// slice port; otherwise the slice's first named/IPv4 port is used. If neither
// is set, 0 is returned and the caller should skip the slice.
func pickPort(ports []discoveryv1.EndpointPort, b v1alpha1.BackendSpec) int {
	if b.Port > 0 {
		return int(b.Port)
	}
	for _, p := range ports {
		if p.Port != nil {
			return int(*p.Port)
		}
	}
	return 0
}
```

- [ ] **Step 4: Remove old resolver from `vinylcache_controller.go:190-220`**

Delete the old `resolveBackendEndpoints` block (the Service-DNS one) and its leading comment. The new one lives in `endpoints.go`. Also remove unused imports that no longer apply (e.g. `types` for `NamespacedName` if nothing else uses it — check `goimports`).

Run:

```bash
goimports -w internal/controller/vinylcache_controller.go
go build ./...
```

Expected: clean build.

- [ ] **Step 5: Run the resolver tests**

```bash
go test ./internal/controller/ -run TestResolveBackendEndpoints -v
```

Expected: all three PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/controller/endpoints.go internal/controller/endpoints_test.go internal/controller/vinylcache_controller.go
git commit -m "feat(controller): resolve backends via EndpointSlice

Replaces the Service-DNS single-endpoint fallback with per-pod
resolution using EndpointSlice. Filters out Terminating and !Ready
endpoints so Varnish stops routing to pods before kube-proxy does.
Empty result is valid (director emits with no backends until next
endpoint-change reconcile).

Refs #11"
```

---

## Task 5: Controller — watch EndpointSlice and map to VinylCache

**Files:**
- Modify: `internal/controller/vinylcache_controller.go` (add Watch + mapping func)
- Modify: `internal/controller/vinylcache_controller_test.go` (envtest case)

- [ ] **Step 1: Write the envtest failing test**

Add a test to `internal/controller/vinylcache_controller_test.go` (or a new `endpoints_envtest.go` if the existing suite is tidy). Skip placeholder code for the suite plumbing; use the existing `BeforeSuite` helpers. Core assertion:

```go
var _ = Describe("EndpointSlice-driven reconcile", func() {
	It("reconciles when backend EndpointSlice changes", func() {
		// Create VinylCache referencing backend svc "plone-svc".
		// Initially no EndpointSlice — expect status to report empty backends.
		// Create EndpointSlice with 2 ready endpoints — expect VCL hash to change
		// within 3s (debounce-accounting buffer).
	})
})
```

Write the full Ginkgo block following the style in the existing suite. Use `Eventually(...).WithTimeout(5*time.Second)` for assertions.

- [ ] **Step 2: Run — expected fail**

```bash
go test ./internal/controller/ -run TestControllers -v
```

Expected: FAIL — the reconciler doesn't observe EndpointSlice events yet.

- [ ] **Step 3: Add mapping function + watch registration**

In `vinylcache_controller.go`, add at package scope:

```go
// endpointSliceToVinylCache maps an EndpointSlice event to every VinylCache
// in the same namespace that references the slice's Service via a backend.
func (r *VinylCacheReconciler) endpointSliceToVinylCache(ctx context.Context, obj client.Object) []reconcile.Request {
	es, ok := obj.(*discoveryv1.EndpointSlice)
	if !ok {
		return nil
	}
	svcName := es.Labels[discoveryv1.LabelServiceName]
	if svcName == "" {
		return nil
	}
	list := &v1alpha1.VinylCacheList{}
	if err := r.List(ctx, list, client.InNamespace(es.Namespace)); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for i := range list.Items {
		vc := &list.Items[i]
		for _, b := range vc.Spec.Backends {
			if b.ServiceRef.Name == svcName {
				reqs = append(reqs, reconcile.Request{
					NamespacedName: client.ObjectKey{Name: vc.Name, Namespace: vc.Namespace},
				})
				break
			}
		}
	}
	return reqs
}
```

In `SetupWithManager`, add a Watches call for `EndpointSlice` alongside the existing Pod watch:

```go
func (r *VinylCacheReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.VinylCache{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.Secret{}).
		Owns(&corev1.ConfigMap{}).
		Watches(
			&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(r.podToVinylCache),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Watches(
			&discoveryv1.EndpointSlice{},
			handler.EnqueueRequestsFromMapFunc(r.endpointSliceToVinylCache),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Named("vinylcache").
		Complete(r)
}
```

Add the import `discoveryv1 "k8s.io/api/discovery/v1"` at the top.

- [ ] **Step 4: Run envtest**

```bash
go test ./internal/controller/ -v -timeout 3m
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/controller/vinylcache_controller.go internal/controller/vinylcache_controller_test.go
git commit -m "feat(controller): watch EndpointSlice and reconcile owning VinylCache

Adds a Watches binding for discoveryv1.EndpointSlice that maps every
slice event to VinylCache objects in the same namespace that reference
the slice's Service. Together with the per-pod resolver this means
backend Pod rollouts / scale / crashes trigger VCL regeneration
automatically.

Refs #11"
```

---

## Task 6: Debouncing — coalesce endpoint-change storms

**Files:**
- Create: `internal/controller/debounce.go`
- Create: `internal/controller/debounce_test.go`
- Modify: `internal/controller/vcl_push.go:160-165` (replace stub)
- Modify: `internal/controller/vinylcache_controller.go` (init the debouncer on the reconciler)

- [ ] **Step 1: Write failing tests for the debouncer**

Create `internal/controller/debounce_test.go`:

```go
package controller

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/types"
)

func TestDebouncer_FirstCallReadyImmediately(t *testing.T) {
	d := newDebouncer()
	key := types.NamespacedName{Name: "a", Namespace: "ns"}
	assert.Equal(t, time.Duration(0), d.remaining(key, 500*time.Millisecond))
}

func TestDebouncer_TouchThenReadAfterWindow(t *testing.T) {
	d := newDebouncer()
	key := types.NamespacedName{Name: "a", Namespace: "ns"}
	d.touch(key)
	assert.Greater(t, d.remaining(key, 500*time.Millisecond), time.Duration(0))
	time.Sleep(550 * time.Millisecond)
	assert.Equal(t, time.Duration(0), d.remaining(key, 500*time.Millisecond))
}

func TestDebouncer_TouchExtends(t *testing.T) {
	d := newDebouncer()
	key := types.NamespacedName{Name: "a", Namespace: "ns"}
	d.touch(key)
	time.Sleep(300 * time.Millisecond)
	d.touch(key) // churn — extends window
	remaining := d.remaining(key, 500*time.Millisecond)
	assert.Greater(t, remaining, 400*time.Millisecond,
		"second touch must restart the window")
}
```

- [ ] **Step 2: Run — expected fail**

```bash
go test ./internal/controller/ -run TestDebouncer -v
```

Expected: FAIL (type `debouncer` doesn't exist).

- [ ] **Step 3: Implement `debounce.go`**

Create `internal/controller/debounce.go`:

```go
/*
Copyright 2026. Licensed under the Apache License, Version 2.0.
*/

package controller

import (
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"
)

// debouncer tracks the last time a VinylCache saw an endpoint-change event, so
// the reconciler can coalesce bursts of EndpointSlice updates during a rollout
// into a single VCL push. It's a per-operator in-memory map; restarts lose the
// timestamps, which is fine — the next reconcile pass just runs immediately.
type debouncer struct {
	mu     sync.Mutex
	lastCh map[types.NamespacedName]time.Time
	now    func() time.Time
}

func newDebouncer() *debouncer {
	return &debouncer{
		lastCh: make(map[types.NamespacedName]time.Time),
		now:    time.Now,
	}
}

// touch records that an endpoint change happened "now" for this VinylCache.
func (d *debouncer) touch(key types.NamespacedName) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastCh[key] = d.now()
}

// remaining returns the duration the reconciler should wait before pushing,
// given a target window. Returns 0 when the window has elapsed since the last
// touch (or when no touch has been recorded at all).
func (d *debouncer) remaining(key types.NamespacedName, window time.Duration) time.Duration {
	if window <= 0 {
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	last, ok := d.lastCh[key]
	if !ok {
		return 0
	}
	elapsed := d.now().Sub(last)
	if elapsed >= window {
		delete(d.lastCh, key)
		return 0
	}
	return window - elapsed
}
```

- [ ] **Step 4: Wire the debouncer into the reconciler**

In `vinylcache_controller.go`, add field:

```go
type VinylCacheReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	Generator   generator.Generator
	AgentClient AgentClient
	OperatorIP  string
	ProxyRouter *proxy.RegisteredRouter
	ProxyPodMap *proxy.PodMap
	debouncer   *debouncer // lazy-init in SetupWithManager
}
```

In `SetupWithManager`, before `return ctrl.NewControllerManagedBy(...)`:

```go
if r.debouncer == nil {
	r.debouncer = newDebouncer()
}
```

In `endpointSliceToVinylCache` (added in Task 5), call `r.debouncer.touch(req.NamespacedName)` for each enqueued request before returning:

```go
for _, req := range reqs {
	if r.debouncer != nil {
		r.debouncer.touch(req.NamespacedName)
	}
}
return reqs
```

Replace the stub in `vcl_push.go`:

```go
// debounceRemaining returns the duration the reconciler should wait before
// pushing VCL. Zero means "push now". Uses the reconciler-level debouncer,
// which is primed by EndpointSlice events.
func (r *VinylCacheReconciler) debounceRemaining(vc *v1alpha1.VinylCache) time.Duration {
	if r.debouncer == nil {
		return 0
	}
	window := vc.Spec.Debounce.Duration.Duration
	if window <= 0 {
		window = 1 * time.Second
	}
	key := types.NamespacedName{Name: vc.Name, Namespace: vc.Namespace}
	return r.debouncer.remaining(key, window)
}
```

Add the `"k8s.io/apimachinery/pkg/types"` import to `vcl_push.go` if missing.

- [ ] **Step 5: Run all controller tests**

```bash
go test ./internal/controller/ -v -timeout 3m
```

Expected: PASS (debouncer tests + envtest still green; envtest already tolerated up to 3s for reconciliation).

- [ ] **Step 6: Commit**

```bash
git add internal/controller/debounce.go internal/controller/debounce_test.go internal/controller/vinylcache_controller.go internal/controller/vcl_push.go
git commit -m "feat(controller): debounce endpoint-change reconciles

Coalesces EndpointSlice event bursts (seen during rollouts) into a
single VCL push using an in-memory per-VinylCache timestamp map.
Window is spec.debounce.duration (default 1s). Restart-safe because
the next reconcile pass runs immediately.

Avoids the VCL-thrash pattern seen in kube-httpcache issue #66.

Refs #11"
```

---

## Task 7: Webhook — validate per-backend director

**Files:**
- Modify: `internal/webhook/vinylcache_validator.go`
- Modify: `internal/webhook/vinylcache_validator_test.go`

- [ ] **Step 1: Find where top-level `spec.director` is validated**

```bash
grep -n "Director" internal/webhook/vinylcache_validator.go
```

Record the enum list used for the top-level validation (should be `shard`, `round_robin`, `random`, `hash`). The per-backend field should accept the same set **minus `hash`** (hash director is a single-backend lookup by header, nonsensical for per-pod expansion) and plus `fallback` for explicit standby semantics — confirm by reading upstream Varnish docs if uncertain and adjust this plan, but default to same enum as top-level.

- [ ] **Step 2: Write failing webhook test**

Add test case to `internal/webhook/vinylcache_validator_test.go`:

```go
func TestValidate_BackendDirectorEnum(t *testing.T) {
	base := validVinylCache() // existing helper
	base.Spec.Backends[0].Director = &v1alpha1.DirectorSpec{Type: "bogus"}
	_, err := validator().Validate(context.Background(), base)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.backends[0].director.type")
}

func TestValidate_BackendDirectorShard(t *testing.T) {
	base := validVinylCache()
	base.Spec.Backends[0].Director = &v1alpha1.DirectorSpec{
		Type:  "shard",
		Shard: &v1alpha1.ShardSpec{By: "URL"},
	}
	_, err := validator().Validate(context.Background(), base)
	require.NoError(t, err)
}
```

(Adjust the helper names to match whatever exists in the package — run `go test` to see the failure.)

- [ ] **Step 3: Run the test**

```bash
go test ./internal/webhook/ -run TestValidate_BackendDirector -v
```

Expected: FAIL.

- [ ] **Step 4: Add validation**

In `vinylcache_validator.go`, extend the backend loop to validate `b.Director` when non-nil. Mirror the enum check used for the top-level `spec.director.type`. Keep the error field path `spec.backends[<i>].director.type`.

- [ ] **Step 5: Run tests**

```bash
go test ./internal/webhook/ -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/webhook
git commit -m "feat(webhook): validate per-backend director type enum

Refs #11"
```

---

## Task 8: E2E — chainsaw rollout test

**Files:**
- Create: `e2e/tests/backend-rollout/chainsaw-test.yaml`
- Create: `e2e/tests/backend-rollout/vinylcache.yaml`
- Create: `e2e/tests/backend-rollout/backend-deployment.yaml`
- Create: `e2e/tests/backend-rollout/backend-service.yaml`

- [ ] **Step 1: Write the chainsaw test (backend Deployment with 3 replicas; rolling update; VinylCache fronting it; assert no 5xx during rollout)**

Create `e2e/tests/backend-rollout/chainsaw-test.yaml`:

```yaml
apiVersion: chainsaw.kyverno.io/v1alpha1
kind: Test
metadata:
  name: backend-rollout
spec:
  timeouts:
    apply: 30s
    assert: 120s
    delete: 60s
  steps:
    - name: create backend + cache
      try:
        - apply: {file: backend-service.yaml}
        - apply: {file: backend-deployment.yaml}
        - apply: {file: vinylcache.yaml}
        - assert: {file: vinylcache-ready.yaml}
    - name: probe cache pre-rollout
      try:
        - script:
            content: |
              for i in $(seq 1 20); do
                kubectl exec -n $NAMESPACE deploy/curl -- curl -sf http://rollout-cache-traffic/health || exit 1
              done
    - name: trigger backend rollout
      try:
        - script:
            content: |
              kubectl -n $NAMESPACE set env deploy/backend ROLL=$(date +%s)
              kubectl -n $NAMESPACE rollout status deploy/backend --timeout=90s
    - name: probe cache during-and-after rollout
      try:
        - script:
            content: |
              fail=0
              for i in $(seq 1 60); do
                if ! kubectl exec -n $NAMESPACE deploy/curl -- curl -sf http://rollout-cache-traffic/health; then
                  fail=$((fail + 1))
                fi
                sleep 0.5
              done
              if [ "$fail" -gt 2 ]; then
                echo "Too many failures ($fail) during rollout — per-pod directors not effective"
                exit 1
              fi
```

Create `e2e/tests/backend-rollout/backend-deployment.yaml`, `backend-service.yaml`, `vinylcache.yaml` (copy shapes from an existing chainsaw test under `e2e/tests/` for the exact operator-install-free style), ensuring:

- Backend Deployment has 3 replicas, rollingUpdate strategy with maxUnavailable=1, serves HTTP 200 `/health`.
- Service targets the backend.
- VinylCache references the Service via `serviceRef`, snippet sets `set req.backend_hint = backend.backend();`, no other routing.
- A helper `curl` Deployment is in the same namespace for probing.

- [ ] **Step 2: Run the new test locally**

```bash
make e2e TEST=backend-rollout
```

(Check `e2e/setup/` for the correct make target; if none exists use `chainsaw test e2e/tests/backend-rollout`.)

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add e2e/tests/backend-rollout/
git commit -m "test(e2e): backend rollout does not cause 5xx through cache

Asserts that during a rolling update of a 3-replica backend, requests
through the VinylCache see fewer than 3 failures out of 60 probes.
Validates per-pod directors + EndpointSlice watch + debounce end-to-end.

Refs #11"
```

---

## Task 9: Docs + release notes

**Files:**
- Create: `docs/source/user-guide/backend-directors.md`
- Modify: `docs/source/release-notes.md` (append entry)
- Modify: `docs/source/user-guide/index.md` (add nav entry)

- [ ] **Step 1: Write the user guide**

Create `docs/source/user-guide/backend-directors.md` covering:

- Conceptual model: one `spec.backends[]` entry → N per-pod backends + one director.
- Default (shard, HASH-by) and when to override (round_robin for stateless, random rarely useful, fallback for primary/standby).
- Snippet migration from `<name>_0` to `<name>.backend()`; call out that hardcoded indexes break at rollout.
- Example YAML with two backends (one shard, one round_robin).
- Debounce semantics and how to raise `spec.debounce.duration` when the backend's own ramp-up is slow.

Follow the project's Diataxis "How-to" style (user-guide is task-oriented). Use MyST admonitions (`{important}`, `{note}`) per the plone-docs skill conventions — this project already uses shibuya + myst.

- [ ] **Step 2: Append release-notes entry**

In `docs/source/release-notes.md`, add under "Unreleased":

```markdown
### Added
- Per-backend directors: each `spec.backends[]` now renders its own Varnish director grouping every per-pod backend (issue #11).
- EndpointSlice-driven reconciliation: backend Pod rollouts, scale, and restarts update VCL automatically.
- `spec.backends[].director` override for non-default director algorithms.

### Changed (breaking)
- `spec.backends[].serviceRef` is now expanded per-pod via EndpointSlice. Custom VCL snippets that hardcode `<backend>_0` must switch to `<backend>.backend()`. Example migration in the new backend-directors guide.
- `spec.debounce.duration` is now honoured (was a no-op). Default is 1s. Set to `0s` to push on every endpoint change (not recommended).
```

- [ ] **Step 3: Build docs locally**

```bash
cd docs && make html
```

Expected: no Sphinx warnings (the CI job builds with `-W`).

- [ ] **Step 4: Commit**

```bash
git add docs/source
git commit -m "docs: per-backend directors guide + release notes

Documents the new per-pod backend expansion, director defaults, the
snippet migration, and debounce semantics. Adds breaking-change
callout in release notes.

Refs #11"
```

---

## Task 10: Cleanup + CI verification

- [ ] **Step 1: Run the full verification gate**

```bash
make generate manifests
go build ./...
go test ./... -timeout 5m
make lint
make e2e TEST=backend-rollout
cd docs && make html
```

Each step must succeed. If `make generate` produces a diff, commit it ("chore: regenerated manifests").

- [ ] **Step 2: Update `/home/jensens/.claude/projects/-home-jensens-ws-bda-cloud-vinyl/memory/` if a new architectural fact emerged**

If during implementation anything surprised you (e.g. EndpointSlice label key differs on older Kubernetes versions in CI, or the debouncer needs a different default), add a short memory entry under the project's MEMORY.md index — **only facts that will matter next session**, not implementation diary.

- [ ] **Step 3: Push and open PR**

```bash
git push -u origin HEAD
gh pr create --title "feat: per-backend directors + EndpointSlice expansion" --body "$(cat <<'EOF'
## Summary

- Expands each `spec.backends[]` serviceRef to per-pod backends via EndpointSlice.
- Groups them under a per-backend Varnish director (default: shard).
- Watches EndpointSlice for rollout-aware reconciliation.
- Adds in-memory debounce (default 1s) to coalesce rollout event storms.

Fixes guru-meditation during backend rolling deploys (#11).

## Breaking change

VCL snippets hardcoding `<backend>_0` must migrate to `<backend>.backend()`. See `docs/source/user-guide/backend-directors.md`.

## Test plan

- [ ] Unit: `go test ./internal/generator ./internal/controller ./internal/webhook`
- [ ] E2E: `make e2e TEST=backend-rollout` (backend rollout with 3 replicas, <3/60 failed probes)
- [ ] Manual: Plone backend with 6 pods, rolling update, watch `varnishlog -g raw -q 'Backend_health'` → terminating pods leave before kube-proxy catches up.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

## Self-review results

- **Spec coverage**: CRD field, generator types, templates, resolver, watch, debounce, webhook, E2E, docs, release notes — every agreed point from the conversation is represented.
- **Placeholder scan**: Task 7 Step 1 asks implementer to confirm enum; this is a deliberate verification step, not a placeholder. Task 8 `e2e/tests/backend-rollout/*.yaml` fixture shapes reference "existing chainsaw test" — this is because the repo has multiple style conventions; the engineer must copy the closest match. Acceptable.
- **Type consistency**: `BackendGroup.Name`, `BackendGroup.Director`, `BackendGroup.Backends`, `DirectorInfo.{Type,Warmup,Rampup,By}` are consistent across Tasks 2, 3, and templates. `debouncer.{touch,remaining}` consistent across Tasks 6 wiring.

No gaps found. Plan ready.

---

## Execution Handoff

**Plan complete and saved to `docs/superpowers/plans/2026-04-14-per-backend-directors.md`. Two execution options:**

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

**Note:** current branch is `fix/vcl-push-idempotent`. Consider creating a dedicated worktree / branch `feat/per-backend-directors` before starting.

**Which approach?**
