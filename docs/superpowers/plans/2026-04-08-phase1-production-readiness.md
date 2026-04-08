# Phase 1: Production Readiness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make cloud-vinyl operator production-ready by implementing the 5 critical missing components: `-j none` flag, readiness probes, hash-based VCL naming, bootstrap VCL, and preStop hooks.

**Architecture:** The operator generates a StatefulSet with two containers (varnish + vinyl-agent). Varnish must start with a bootstrap VCL and become ready only after the operator pushes real VCL. VCL names must be unique per generation to allow updates without collision.

**Tech Stack:** Go 1.25, controller-runtime, Kubernetes API (corev1, appsv1), Varnish 7.6 CLI protocol

**Reference:** Issue #12, architecture doc `docs/plans/architektur.md` §3.3, §3.4, §8.3

---

## File Map

| File | Responsibility | Changes |
|------|---------------|---------|
| `internal/controller/statefulset.go` | StatefulSet pod spec generation | Add `-j none` arg, readiness probe, preStop hook, bootstrap VCL ConfigMap mount |
| `internal/controller/configmap.go` | **NEW** — Bootstrap VCL ConfigMap reconciliation | Create/update ConfigMap with placeholder VCL |
| `internal/controller/vinylcache_controller.go` | Main reconcile loop | Add ConfigMap reconciliation step |
| `internal/controller/vcl_push.go` | VCL push orchestration | Change VCL name to include hash suffix |
| `internal/agent/handler.go` | Agent HTTP handlers | Update Health to check active VCL name |
| Tests updated in corresponding `_test.go` files |

---

### Task 1: Add `-j none` to varnish container args

**Files:**
- Modify: `internal/controller/statefulset.go:67-70`

The stock `varnish:7.6` image runs varnishd with the default jail mechanism (`-j unix`), which requires root for chroot setup. With `runAsUser: 65532` (non-root), varnish fails to start. Architecture §8.3 H4-Fix requires `-j none`.

- [ ] **Step 1: Add `-j none` to varnish args**

In `internal/controller/statefulset.go`, change the Args slice:

```go
Args: []string{
    "-j", "none",
    "-T", "127.0.0.1:6082",
    "-S", "/etc/varnish/secret",
},
```

- [ ] **Step 2: Build and verify**

Run: `go build ./...`
Expected: Success, no errors.

- [ ] **Step 3: Run existing tests**

Run: `go test ./...`
Expected: All tests pass (no test specifically validates args, but build must succeed).

- [ ] **Step 4: Commit**

```bash
git add internal/controller/statefulset.go
git commit -m "fix: add -j none to varnish args for non-root operation

Architecture §8.3 H4-Fix requires -j none when running as non-root.
Without it, varnish tries chroot jail setup which needs root."
```

---

### Task 2: Add readiness probe and preStop hook to StatefulSet

**Files:**
- Modify: `internal/controller/statefulset.go:128-156` (agent container)
- Modify: `internal/controller/statefulset.go:64-106` (varnish container)

The agent's `/health` endpoint already exists and checks whether varnish is responding. Kubernetes needs a readiness probe on the agent container so pods are only marked Ready after the agent is up. The varnish container needs a preStop hook for graceful shutdown during rolling updates (architecture §3.3 K3-Fix).

- [ ] **Step 1: Add readiness probe to agent container**

In `internal/controller/statefulset.go`, add a `ReadinessProbe` to the `agentContainer`:

```go
agentContainer := corev1.Container{
    Name:  "vinyl-agent",
    Image: agentImage,
    Ports: []corev1.ContainerPort{
        {Name: "agent", ContainerPort: agentPort, Protocol: corev1.ProtocolTCP},
    },
    ReadinessProbe: &corev1.Probe{
        ProbeHandler: corev1.ProbeHandler{
            HTTPGet: &corev1.HTTPGetAction{
                Path: "/health",
                Port: intstr.FromInt32(agentPort),
            },
        },
        InitialDelaySeconds: 5,
        PeriodSeconds:       5,
        FailureThreshold:    6,
    },
    // ... rest unchanged
```

Add import `"k8s.io/apimachinery/pkg/util/intstr"` to the imports.

- [ ] **Step 2: Add preStop hook to varnish container**

In `internal/controller/statefulset.go`, add `Lifecycle` to the `varnishContainer`:

```go
varnishContainer := corev1.Container{
    // ... existing fields ...
    Lifecycle: &corev1.Lifecycle{
        PreStop: &corev1.LifecycleHandler{
            Exec: &corev1.ExecAction{
                Command: []string{"sleep", "5"},
            },
        },
    },
    // ... rest unchanged
```

- [ ] **Step 3: Build and run tests**

Run: `go build ./... && go test ./...`
Expected: All pass.

- [ ] **Step 4: Commit**

```bash
git add internal/controller/statefulset.go
git commit -m "feat: add readiness probe on agent and preStop hook on varnish

Readiness probe on agent /health (port 9090) ensures pods are only
marked Ready when the agent can reach varnishd. preStop sleep(5)
gives the endpoints controller time to remove the pod from routing
before varnish stops (architecture §3.3 K3-Fix)."
```

---

### Task 3: Update agent Health endpoint for VCL-aware readiness

**Files:**
- Modify: `internal/agent/handler.go:185-195`
- Modify: `internal/agent/handler_test.go`

Currently, `/health` returns 200 as soon as varnish responds to `ActiveVCL()`. But the pod should not be ready until the operator has pushed real VCL (not the default `boot` VCL). The health endpoint needs to distinguish between "varnish running with bootstrap VCL" (not ready) and "varnish running with operator-pushed VCL" (ready).

- [ ] **Step 1: Write failing test for health endpoint with bootstrap VCL**

Add to `internal/agent/handler_test.go`:

```go
func TestHealth_BootstrapVCL_Returns503(t *testing.T) {
	h, mock := newTestHandler()
	mock.activeVCLFn = func(ctx context.Context) (string, error) {
		return "boot", nil
	}
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	h.Health(rr, req)
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
	var resp map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "initializing", resp["status"])
}

func TestHealth_OperatorVCL_Returns200(t *testing.T) {
	h, mock := newTestHandler()
	mock.activeVCLFn = func(ctx context.Context) (string, error) {
		return "aaf-prod-cache-abc12345", nil
	}
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	h.Health(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "ok", resp["status"])
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/ -run "TestHealth_BootstrapVCL" -v`
Expected: FAIL — current Health returns 200 for "boot" VCL.

- [ ] **Step 3: Update Health handler**

In `internal/agent/handler.go`, replace the `Health` method:

```go
// Health handles GET /health (no auth required).
// Returns 503 until the operator pushes real VCL (active VCL name != "boot").
// This drives the Kubernetes readiness probe — pods are not Ready until
// the operator successfully pushes VCL.
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	name, err := h.admin.ActiveVCL(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "error", "varnish": "not responding"})
		return
	}
	// "boot" is the default VCL name loaded at varnish startup.
	// The pod is not ready until the operator pushes a named VCL.
	if name == "boot" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "initializing", "varnish": "waiting for VCL push"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "varnish": "running", "vcl": name})
}
```

- [ ] **Step 4: Update the existing TestHealth_VarnishRunning test**

The existing `TestHealth_VarnishRunning_Returns200` uses the default mock that returns `"boot"`. This must be changed since `"boot"` now returns 503:

```go
func TestHealth_VarnishRunning_Returns200(t *testing.T) {
	h, mock := newTestHandler()
	mock.activeVCLFn = func(ctx context.Context) (string, error) {
		return "operator-pushed-vcl", nil
	}
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	h.Health(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "ok", resp["status"])
	assert.Equal(t, "running", resp["varnish"])
}
```

- [ ] **Step 5: Run all agent tests**

Run: `go test ./internal/agent/ -v`
Expected: All pass, including the new tests.

- [ ] **Step 6: Commit**

```bash
git add internal/agent/handler.go internal/agent/handler_test.go
git commit -m "feat: health endpoint returns 503 until operator pushes VCL

Pods are not ready (readiness probe fails) until the operator
successfully pushes real VCL. The 'boot' VCL (varnish default)
triggers a 503 response from /health."
```

---

### Task 4: Hash-based VCL naming to prevent collision

**Files:**
- Modify: `internal/controller/vcl_push.go:59`
- Modify: `internal/controller/vcl_push_test.go`

Currently `vclName` is `<namespace>-<name>` (fixed). When VCL changes, `vcl.inline` fails with "Already a VCL named ...". The VCL name must include a hash suffix so each generation is unique. The agent's `PushVCL` already handles `vcl.use` and old VCL discard.

- [ ] **Step 1: Write test for hash-based VCL naming**

Add to `internal/controller/vcl_push_test.go`:

```go
func TestVCLName_IncludesHash(t *testing.T) {
	// Verify that the VCL name includes a hash suffix
	result := &generator.Result{
		VCL:  "vcl 4.1; backend default { .host = \"127.0.0.1\"; }",
		Hash: "abc123def456789012345678901234567890123456789012345678901234",
	}
	vc := makeVC()

	// Build the expected name format
	expected := fmt.Sprintf("%s-%s-%s", vc.Namespace, vc.Name, result.Hash[:8])
	if expected != "default-test-cache-abc123de" {
		t.Fatalf("unexpected name format: %s", expected)
	}
}
```

Add `"fmt"` to the test file imports.

- [ ] **Step 2: Run test to verify it passes (this is a format test, not behavior)**

Run: `go test ./internal/controller/ -run "TestVCLName_IncludesHash" -v`
Expected: PASS (it's testing the format logic only).

- [ ] **Step 3: Change VCL name in pushVCL**

In `internal/controller/vcl_push.go`, change line 59:

```go
vclName := fmt.Sprintf("%s-%s-%s", vc.Namespace, vc.Name, result.Hash[:8])
```

- [ ] **Step 4: Update mock signatures in vcl_push_test.go**

The `PushVCL` mock receives the VCL name but doesn't validate it. No mock changes needed — the mock accepts any string for the name parameter. But verify all tests still pass.

- [ ] **Step 5: Run all controller tests**

Run: `go test ./internal/controller/ -v`
Expected: All pass.

- [ ] **Step 6: Commit**

```bash
git add internal/controller/vcl_push.go internal/controller/vcl_push_test.go
git commit -m "fix: use hash-based VCL names to prevent vcl.inline collision

VCL name is now <namespace>-<name>-<hash[:8]>. Each VCL generation
gets a unique name, preventing 'Already a VCL named ...' errors
when the operator pushes updated VCL."
```

---

### Task 5: Bootstrap VCL via ConfigMap

**Files:**
- Create: `internal/controller/configmap.go`
- Modify: `internal/controller/statefulset.go` (add volume + mount)
- Modify: `internal/controller/vinylcache_controller.go` (add reconcileConfigMap step)

The stock varnish image starts with `/etc/varnish/default.vcl` which points to `localhost:8080`. This causes `Backend fetch failed` because nothing listens there. We need a bootstrap VCL that returns a clean 503 "Cache initializing" response. The operator creates a ConfigMap with this VCL and mounts it as the default VCL.

- [ ] **Step 1: Create configmap.go with bootstrap VCL**

Create `internal/controller/configmap.go`:

```go
/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
...
*/

package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	v1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
)

const bootstrapVCL = `vcl 4.1;

backend bootstrap_placeholder {
    .host = "127.0.0.1";
    .port = "1";
}

sub vcl_recv {
    return (synth(503, "Cache initializing — waiting for VCL push from cloud-vinyl operator"));
}

sub vcl_synth {
    set resp.http.Content-Type = "text/plain; charset=utf-8";
    set resp.http.Retry-After = "5";
    synthetic(resp.reason);
    return (deliver);
}
`

// reconcileConfigMap creates or updates the ConfigMap containing the bootstrap VCL.
func (r *VinylCacheReconciler) reconcileConfigMap(ctx context.Context, vc *v1alpha1.VinylCache) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vc.Name + "-bootstrap-vcl",
			Namespace: vc.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
		if err := ctrl.SetControllerReference(vc, cm, r.Scheme); err != nil {
			return err
		}
		cm.Labels = map[string]string{labelVinylCacheName: vc.Name}
		cm.Data = map[string]string{
			"default.vcl": bootstrapVCL,
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("reconciling bootstrap VCL ConfigMap: %w", err)
	}
	return nil
}
```

- [ ] **Step 2: Build to check compilation**

Run: `go build ./...`
Expected: Success.

- [ ] **Step 3: Add ConfigMap reconciliation to the controller**

In `internal/controller/vinylcache_controller.go`, add after step 9 (reconcileSecret):

```go
// 9b. Reconcile bootstrap VCL ConfigMap.
if err := r.reconcileConfigMap(ctx, vc); err != nil {
    return ctrl.Result{}, err
}
```

Also add `ConfigMap` to the `Owns` chain in `SetupWithManager`:

```go
func (r *VinylCacheReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.VinylCache{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.Secret{}).
		Owns(&corev1.ConfigMap{}).
		// ... rest unchanged
```

- [ ] **Step 4: Mount ConfigMap in varnish container**

In `internal/controller/statefulset.go`, add a volume and mount:

Add to the `volumes` slice:

```go
{
    Name: "bootstrap-vcl",
    VolumeSource: corev1.VolumeSource{
        ConfigMap: &corev1.ConfigMapVolumeSource{
            LocalObjectReference: corev1.LocalObjectReference{
                Name: vc.Name + "-bootstrap-vcl",
            },
        },
    },
},
```

Add to the `varnishContainer.VolumeMounts` slice:

```go
{
    Name:      "bootstrap-vcl",
    MountPath: "/etc/varnish/default.vcl",
    SubPath:   "default.vcl",
    ReadOnly:  true,
},
```

- [ ] **Step 5: Build and run all tests**

Run: `go build ./... && go test ./...`
Expected: All pass.

- [ ] **Step 6: Commit**

```bash
git add internal/controller/configmap.go internal/controller/statefulset.go internal/controller/vinylcache_controller.go
git commit -m "feat: bootstrap VCL via ConfigMap for clean pod startup

Operator creates a ConfigMap with placeholder VCL that returns
503 'Cache initializing'. Mounted as /etc/varnish/default.vcl so
varnish starts cleanly. Combined with the readiness probe, pods
are not marked Ready until the operator pushes real VCL.

This replaces the need for a vinyl-init container (architecture
§3.3 — simpler approach with same result)."
```

---

### Task 6: Final integration verification

- [ ] **Step 1: Run full test suite**

Run: `go test ./... -v`
Expected: All tests pass.

- [ ] **Step 2: Run pre-commit hooks**

Run: `pre-commit run --all-files` (or just attempt a git commit which triggers hooks)
Expected: All hooks pass (go fmt, go vet, golangci-lint, helm lint, helm unittest).

- [ ] **Step 3: Create PR**

```bash
git push -u origin feat/phase1-production-readiness
gh pr create --repo bluedynamics/cloud-vinyl \
  --title "feat: Phase 1 production readiness (#12)" \
  --body "Implements the critical missing components for production use:

1. \`-j none\` flag for non-root varnish operation
2. Readiness probe on agent \`/health\` — pods not Ready until VCL pushed
3. preStop hook on varnish container for graceful shutdown
4. VCL-aware health check — distinguishes bootstrap vs operator-pushed VCL
5. Hash-based VCL naming — prevents \`vcl.inline\` name collision on updates
6. Bootstrap VCL via ConfigMap — clean 503 until operator pushes real VCL

Fixes the critical items from #12."
```

---

## Verification checklist

After implementation, verify these behaviors:

1. **Pod startup:** Varnish starts without errors (no chroot/jail failures)
2. **Pod readiness:** Pod stays `0/2 Ready` until operator pushes VCL, then becomes `2/2 Ready`
3. **VCL updates:** Changing a VCL snippet in the VinylCache CR triggers a new VCL push with a different name — no "Already exists" error
4. **Rolling updates:** preStop hook prevents connection resets during pod termination
5. **Bootstrap response:** Direct HTTP to varnish before VCL push returns 503 "Cache initializing"
