# Varnish/Cache-Monitoring (Issue #48) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `spec.monitoring` actually work — emit `vinyl_*` metrics, generate+apply ServiceMonitor/PrometheusRule, and ship a varnish-exporter sidecar so Grafana can show the Varnish cache hit rate.

**Architecture:** Three opt-in layers. (1) Inject a nil-safe `*monitoring.Metrics` into the reconciler and proxy and increment counters at the real call sites. (2) A new `reconcileMonitoring` step converts the existing minimal SM/PromRule structs to `unstructured.Unstructured` and applies them, gated on `spec.monitoring.*.enabled` AND on the `monitoring.coreos.com` CRDs being installed. (3) A `prometheus_varnish_exporter` sidecar on the Varnish StatefulSet exposes native `varnish_*` metrics, which become the source of truth for hit-ratio/backend-health; the redundant `vinyl_cache_hit_ratio`/`vinyl_backend_health` gauges are removed.

**Tech Stack:** Go, `sigs.k8s.io/controller-runtime` v0.23.1, `github.com/prometheus/client_golang` v1.23.2, `k8s.io/apimachinery` v0.35.0, controller-runtime envtest + `sigs.k8s.io/controller-runtime/pkg/client/fake`.

## Global Constraints

- Module path: `github.com/bluedynamics/cloud-vinyl`.
- All metric access MUST be nil-safe: `if m != nil { m.X.WithLabelValues(...).Inc() }`. Tests pass `nil` and must stay green.
- Metrics register into the controller-runtime registry `sigs.k8s.io/controller-runtime/pkg/metrics`.`Registry` — never the global prometheus registry, never a second metrics server.
- `vinyl_*` operator-domain counters are always recorded (not per-cache gated); `spec.monitoring.*.enabled` gates ONLY the generated CRs and the exporter sidecar.
- Existing constants (do not redefine): `agentPort = 9090`, `varnishPort = 8080` (controller pkg, `agent_client.go`); `invalidationPort = 8090` (`service.go`); label key `labelVinylCacheName = "vinyl.bluedynamics.eu/cache-name"`; pod selector label `"app": vc.Name`.
- Every change (incl. tests/tooling) gets a CHANGES entry in the same PR.
- Run `make test` from the repo root (`sources/cloud-vinyl`); regenerate CRD/deepcopy with `make generate manifests`.
- **Always operate on this repo with `git -C /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl …`** (cwd is unreliable between calls; this is a `sources/` sub-repo).

---

### Task 1: Remove redundant hit-ratio / backend-health gauges

The exporter is the source for these (design decision). Drop the dead gauges so the metric surface stays honest.

**Files:**
- Modify: `internal/monitoring/metrics.go` (struct fields `HitRatio`, `BackendHealth` + their registration blocks)
- Test: `internal/monitoring/metrics_test.go`

**Interfaces:**
- Produces: `monitoring.Metrics` struct WITHOUT `HitRatio`/`BackendHealth` fields. Remaining fields unchanged: `VCLPushTotal`, `VCLPushDuration`, `InvalidationTotal`, `InvalidationDuration`, `BroadcastTotal`, `PartialFailureTotal`, `VCLVersionsLoaded`, `ReconcileTotal`, `ReconcileDuration`.

- [ ] **Step 1: Write/adjust the failing test**

In `internal/monitoring/metrics_test.go`, ensure the registered-names test asserts the two names are GONE:

```go
func TestMetrics_HitRatioAndBackendHealthRemoved(t *testing.T) {
	reg := prometheus.NewRegistry()
	_ = NewMetrics(reg)
	mfs, err := reg.Gather()
	require.NoError(t, err)
	names := map[string]bool{}
	for _, mf := range mfs {
		names[mf.GetName()] = true
	}
	assert.False(t, names["vinyl_cache_hit_ratio"], "hit ratio gauge should be removed (exporter is the source)")
	assert.False(t, names["vinyl_backend_health"], "backend health gauge should be removed (exporter is the source)")
}
```

Also delete any existing assertions/usages that reference `m.HitRatio` or `m.BackendHealth` in this test file.

- [ ] **Step 2: Run test to verify it fails**

Run: `git -C /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl test ./internal/monitoring/ -run TestMetrics -v` (use `go test`; see note)
Note: use `cd` is unreliable — invoke `go test` with an explicit package path from the repo root, e.g. `(cd /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl && go test ./internal/monitoring/ -run TestMetrics -v)`.
Expected: FAIL — `m.HitRatio`/`m.BackendHealth` still compile-referenced OR the gauges still registered.

- [ ] **Step 3: Remove the fields and registrations**

In `internal/monitoring/metrics.go`: delete the struct fields

```go
	// Cache state
	HitRatio          *prometheus.GaugeVec // labels: cache, namespace
	BackendHealth     *prometheus.GaugeVec // labels: cache, namespace, backend
```

(keep `VCLVersionsLoaded`), and delete the two `m.HitRatio = prometheus.NewGaugeVec(... "vinyl_cache_hit_ratio" ...)` / `reg.MustRegister(m.HitRatio)` and `m.BackendHealth = ... "vinyl_backend_health" ... ` / `reg.MustRegister(m.BackendHealth)` blocks.

- [ ] **Step 4: Run tests to verify pass**

Run: `(cd /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl && go test ./internal/monitoring/ -v)`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git -C /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl add internal/monitoring/metrics.go internal/monitoring/metrics_test.go
git -C /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl commit -m "refactor(monitoring): drop vinyl_cache_hit_ratio/backend_health gauges

Exporter (varnish_*) is the source for these per design; remove the
dead operator-side gauges."
```

---

### Task 2: Instantiate metrics + instrument Reconcile

**Files:**
- Modify: `cmd/operator/main.go` (create metrics, inject into reconciler + proxy)
- Modify: `internal/controller/vinylcache_controller.go` (add `Metrics` field; wrap `Reconcile`)
- Test: `internal/controller/vinylcache_controller_test.go`

**Interfaces:**
- Consumes: `monitoring.NewMetrics(reg prometheus.Registerer) *monitoring.Metrics` (Task 1).
- Produces: `VinylCacheReconciler.Metrics *monitoring.Metrics` field (nil-safe). Reconcile records `ReconcileTotal{cache,namespace,result}` and `ReconcileDuration` on every return.

- [ ] **Step 1: Write the failing test**

Add to `internal/controller/vinylcache_controller_test.go`:

```go
func TestReconcile_RecordsReconcileMetric(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := monitoring.NewMetrics(reg)

	sch := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(sch))
	require.NoError(t, corev1.AddToScheme(sch))
	require.NoError(t, appsv1.AddToScheme(sch))

	vc := &v1alpha1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "ns1"},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(vc).
		WithStatusSubresource(vc).Build()
	r := &VinylCacheReconciler{Client: cli, Scheme: sch, Generator: generator.New(), Metrics: m}

	_, _ = r.Reconcile(context.Background(),
		ctrl.Request{NamespacedName: client.ObjectKey{Name: "c1", Namespace: "ns1"}})

	got := testutil.ToFloat64(m.ReconcileTotal.WithLabelValues("c1", "ns1", "error")) +
		testutil.ToFloat64(m.ReconcileTotal.WithLabelValues("c1", "ns1", "success"))
	assert.Equal(t, float64(1), got, "exactly one reconcile should be counted")
}
```

Add imports as needed: `"github.com/prometheus/client_golang/prometheus"`, `"github.com/prometheus/client_golang/prometheus/testutil"`, `"github.com/bluedynamics/cloud-vinyl/internal/monitoring"`.

- [ ] **Step 2: Run test to verify it fails**

Run: `(cd /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl && go test ./internal/controller/ -run TestReconcile_RecordsReconcileMetric -v)`
Expected: FAIL — `VinylCacheReconciler` has no field `Metrics`.

- [ ] **Step 3: Add the field and instrument Reconcile**

In `internal/controller/vinylcache_controller.go`, add to the struct:

```go
	// Metrics is nil-safe; nil disables metric recording (used in tests).
	Metrics *monitoring.Metrics
```

Add import `"github.com/bluedynamics/cloud-vinyl/internal/monitoring"`. At the very top of `Reconcile`, after `log := logf.FromContext(ctx)`:

```go
	start := time.Now()
	result := "success"
	defer func() {
		if r.Metrics != nil {
			r.Metrics.ReconcileTotal.WithLabelValues(req.Name, req.Namespace, result).Inc()
			r.Metrics.ReconcileDuration.Observe(time.Since(start).Seconds())
		}
	}()
```

Then set `result = "error"` in the deferred closure when the function returns a non-nil error. Simplest robust approach: introduce a named return `(res ctrl.Result, retErr error)` on `Reconcile` and in the defer do `if retErr != nil { result = "error" }`. Update the signature:

```go
func (r *VinylCacheReconciler) Reconcile(ctx context.Context, req ctrl.Request) (res ctrl.Result, retErr error) {
```

and place the `if retErr != nil { result = "error" }` line as the first statement inside the deferred func.

- [ ] **Step 4: Run test to verify pass**

Run: `(cd /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl && go test ./internal/controller/ -run TestReconcile_RecordsReconcileMetric -v)`
Expected: PASS.

- [ ] **Step 5: Wire into main.go**

In `cmd/operator/main.go`, add import `ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"` and `"github.com/bluedynamics/cloud-vinyl/internal/monitoring"`. Before constructing the reconciler:

```go
	vinylMetrics := monitoring.NewMetrics(ctrlmetrics.Registry)
```

Add `Metrics: vinylMetrics,` to the `&controller.VinylCacheReconciler{...}` literal.

- [ ] **Step 6: Build + commit**

```bash
(cd /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl && go build ./... && go test ./internal/controller/ ./internal/monitoring/)
git -C /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl add cmd/operator/main.go internal/controller/vinylcache_controller.go internal/controller/vinylcache_controller_test.go
git -C /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl commit -m "feat(monitoring): register metrics and record reconcile metric"
```

---

### Task 3: Instrument pushVCL

**Files:**
- Modify: `internal/controller/vcl_push.go` (`pushVCL`)
- Test: `internal/controller/vcl_push_test.go`

**Interfaces:**
- Consumes: `VinylCacheReconciler.Metrics` (Task 2).
- Produces: per-peer `VCLPushTotal{cache,namespace,result}` increments; one `VCLPushDuration.Observe` per `pushVCL` call.

- [ ] **Step 1: Write the failing test**

Add to `internal/controller/vcl_push_test.go` (mirror the existing AgentClient fake there):

```go
func TestPushVCL_RecordsMetricsPerPeer(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := monitoring.NewMetrics(reg)
	r := &VinylCacheReconciler{
		Metrics:     m,
		AgentClient: &stubAgentClient{}, // existing test stub that returns nil on PushVCL
	}
	vc := &v1alpha1.VinylCache{ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "ns1"}}
	peers := []generator.PeerBackend{
		{Name: "p0", IP: "10.0.0.1", Port: varnishPort},
		{Name: "p1", IP: "10.0.0.2", Port: varnishPort},
	}
	err := r.pushVCL(context.Background(), vc, &generator.Result{Hash: "abcdef1234", VCL: "vcl 4.1;"}, peers)
	require.NoError(t, err)
	assert.Equal(t, float64(2), testutil.ToFloat64(m.VCLPushTotal.WithLabelValues("c1", "ns1", "success")))
}
```

If no `stubAgentClient` exists, reuse whatever fake `AgentClient` the existing `vcl_push_test.go` already defines (check the file and match its name).

- [ ] **Step 2: Run test to verify it fails**

Run: `(cd /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl && go test ./internal/controller/ -run TestPushVCL_RecordsMetricsPerPeer -v)`
Expected: FAIL — counter stays 0.

- [ ] **Step 3: Instrument pushVCL**

In `internal/controller/vcl_push.go`, add `"time"` already imported. At the start of `pushVCL`, after the `len(peers) == 0` guard:

```go
	pushStart := time.Now()
	defer func() {
		if r.Metrics != nil {
			r.Metrics.VCLPushDuration.Observe(time.Since(pushStart).Seconds())
		}
	}()
```

After `wg.Wait()`, when tallying results, record per-peer outcome:

```go
	for _, pr := range results {
		res := "success"
		if pr.err != nil {
			res = "error"
		}
		if r.Metrics != nil {
			r.Metrics.VCLPushTotal.WithLabelValues(vc.Name, vc.Namespace, res).Inc()
		}
	}
```

Place this loop alongside the existing `failCount` tally (you can merge them into one loop).

- [ ] **Step 4: Run test to verify pass**

Run: `(cd /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl && go test ./internal/controller/ -run TestPushVCL -v)`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git -C /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl add internal/controller/vcl_push.go internal/controller/vcl_push_test.go
git -C /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl commit -m "feat(monitoring): record vinyl_vcl_push metrics per peer"
```

---

### Task 4: Instrument proxy invalidation/broadcast

**Files:**
- Modify: `internal/proxy/server.go` (add `metrics` field + `SetMetrics`; thread `cacheName` to handlers)
- Modify: `internal/proxy/handler.go` (record metrics in `handlePurge`/`handleBAN`/`handleXkey`)
- Modify: `cmd/operator/main.go` (call `proxyServer.SetMetrics(vinylMetrics)`)
- Test: `internal/proxy/handler_test.go` (or the existing proxy test file)

**Interfaces:**
- Consumes: `monitoring.Metrics` (Task 1).
- Produces: `proxy.Server.SetMetrics(*monitoring.Metrics)`. Handlers signature becomes `handlePurge(w, r, namespace, cacheName, pods)`, `handleBAN(w, r, namespace, cacheName, pods)`, `handleXkey(w, r, namespace, cacheName, pods)`. Records `InvalidationTotal{cache,namespace,type,result}`, `InvalidationDuration`, `BroadcastTotal{pod,result}` per `PodResult`, `PartialFailureTotal{cache,namespace}` when `0 < Succeeded < Total`.

- [ ] **Step 1: Write the failing test**

Add to the proxy test file (use the existing pattern with a fake `Broadcaster`, `Router`, `PodIPProvider`):

```go
func TestHandlePurge_RecordsInvalidationMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := monitoring.NewMetrics(reg)

	s := NewServer(":0", staticRouter{ns: "ns1", cache: "c1"},
		staticPods{ips: []string{"10.0.0.1", "10.0.0.2"}},
		okBroadcaster{}, nil)
	s.SetMetrics(m)

	req := httptest.NewRequest("PURGE", "http://c1.example/", nil)
	req.Host = "c1.example"
	s.ServeHTTP(httptest.NewRecorder(), req)

	assert.Equal(t, float64(1),
		testutil.ToFloat64(m.InvalidationTotal.WithLabelValues("c1", "ns1", "purge", "success")))
}
```

Define minimal fakes inline if not already present: `staticRouter` (returns ns/cache/true), `staticPods` (returns the IPs), `okBroadcaster` (returns `BroadcastResult{Status:"ok", Total:2, Succeeded:2, Results: []PodResult{{Pod:"10.0.0.1:8080",Status:200},{Pod:"10.0.0.2:8080",Status:200}}}`).

- [ ] **Step 2: Run test to verify it fails**

Run: `(cd /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl && go test ./internal/proxy/ -run TestHandlePurge_RecordsInvalidationMetrics -v)`
Expected: FAIL — `Server` has no `SetMetrics`.

- [ ] **Step 3: Add metrics to Server + thread cacheName**

In `internal/proxy/server.go`: add field `metrics *monitoring.Metrics` to `Server`, add import `"github.com/bluedynamics/cloud-vinyl/internal/monitoring"`, and:

```go
// SetMetrics installs a nil-safe metrics recorder.
func (s *Server) SetMetrics(m *monitoring.Metrics) { s.metrics = m }
```

In `ServeHTTP`, update the dispatch calls to pass `namespace, cacheName, pods`:

```go
	case r.Method == "PURGE":
		s.handlePurge(w, r, namespace, cacheName, pods)
	case r.Method == "BAN":
		s.handleBAN(w, r, namespace, cacheName, pods)
	case r.Method == http.MethodPost && r.URL.Path == "/ban":
		s.handleBAN(w, r, namespace, cacheName, pods)
	case r.Method == http.MethodPost && r.URL.Path == "/purge/xkey":
		s.handleXkey(w, r, namespace, cacheName, pods)
```

- [ ] **Step 4: Record metrics in handlers**

In `internal/proxy/handler.go`, add a shared helper and update the three handlers. Helper:

```go
// recordInvalidation records invalidation + broadcast + partial-failure metrics.
func (s *Server) recordInvalidation(namespace, cacheName, typ string, start time.Time, res BroadcastResult) {
	if s.metrics == nil {
		return
	}
	outcome := "success"
	if res.Succeeded == 0 {
		outcome = "error"
	}
	s.metrics.InvalidationTotal.WithLabelValues(cacheName, namespace, typ, outcome).Inc()
	s.metrics.InvalidationDuration.Observe(time.Since(start).Seconds())
	for _, pr := range res.Results {
		r := "success"
		if pr.Status < 200 || pr.Status >= 300 {
			r = "error"
		}
		s.metrics.BroadcastTotal.WithLabelValues(pr.Pod, r).Inc()
	}
	if res.Succeeded > 0 && res.Succeeded < res.Total {
		s.metrics.PartialFailureTotal.WithLabelValues(cacheName, namespace).Inc()
	}
}
```

Update each handler signature and add the call. For `handlePurge`:

```go
func (s *Server) handlePurge(w http.ResponseWriter, r *http.Request, namespace, cacheName string, pods []string) {
	start := time.Now()
	// ... existing body unchanged up to the broadcast ...
	result := s.broadcaster.Broadcast(ctx, podAddrs, req)
	s.recordInvalidation(namespace, cacheName, "purge", start, result)
	WriteResult(w, result)
}
```

Do the same for `handleBAN` (`typ = "ban"`, drop the now-duplicate `namespace` param — it already had one; keep a single `namespace` and add `cacheName`) and `handleXkey` (`typ = "xkey"`, record once after the loop using the accumulated `result`).

- [ ] **Step 5: Run test + wire main.go**

Run: `(cd /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl && go test ./internal/proxy/ -v)` → PASS.
In `cmd/operator/main.go`, after `proxyServer := proxy.NewServer(...)`, add:

```go
	proxyServer.SetMetrics(vinylMetrics)
```

- [ ] **Step 6: Build + commit**

```bash
(cd /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl && go build ./... && go test ./internal/proxy/)
git -C /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl add internal/proxy/server.go internal/proxy/handler.go internal/proxy/*_test.go cmd/operator/main.go
git -C /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl commit -m "feat(monitoring): record invalidation/broadcast/partial-failure metrics in proxy"
```

---

### Task 5: Set VCLVersionsLoaded gauge from vcl.list

**Files:**
- Modify: `internal/controller/status.go` (`updateStatus`) — set the gauge from the active VCL count
- Test: `internal/controller/status_test.go`

**Interfaces:**
- Consumes: `VinylCacheReconciler.Metrics`.
- Produces: `VCLVersionsLoaded{cache,namespace}` set to the number of loaded VCL versions during `updateStatus`.

- [ ] **Step 1: Decide the source value**

`updateStatus` already runs after a successful generate/push. The simplest reliable count available without a new agent call is the number of VCL versions the operator believes are loaded. Check `status.go` / `vc.Status` for an existing field (e.g. `ActiveVCL` plus previous). If a precise live count requires an agent `vcl.list` round-trip, set the gauge to `1` when an `ActiveVCL` is present (one active version) — do NOT add a new agent call in this task (YAGNI; a richer drift count belongs to a follow-up). Confirm by reading `status.go` lines around `updateStatus` before writing the test.

- [ ] **Step 2: Write the failing test**

Add to `internal/controller/status_test.go`:

```go
func TestUpdateStatus_SetsVCLVersionsGauge(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := monitoring.NewMetrics(reg)
	sch := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(sch))
	vc := &v1alpha1.VinylCache{ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "ns1"}}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(vc).WithStatusSubresource(vc).Build()
	r := &VinylCacheReconciler{Client: cli, Scheme: sch, Metrics: m}

	r.updateStatus(context.Background(), vc, &generator.Result{Hash: "abcdef1234"}, nil)

	assert.Equal(t, float64(1), testutil.ToFloat64(m.VCLVersionsLoaded.WithLabelValues("c1", "ns1")))
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `(cd /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl && go test ./internal/controller/ -run TestUpdateStatus_SetsVCLVersionsGauge -v)`
Expected: FAIL — gauge stays 0.

- [ ] **Step 4: Set the gauge in updateStatus**

In `internal/controller/status.go`, inside `updateStatus`, after `vc.Status.ActiveVCL` is set:

```go
	if r.Metrics != nil {
		r.Metrics.VCLVersionsLoaded.WithLabelValues(vc.Name, vc.Namespace).Set(1)
	}
```

- [ ] **Step 5: Run test to verify pass + commit**

Run: `(cd /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl && go test ./internal/controller/ -run TestUpdateStatus -v)` → PASS.

```bash
git -C /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl add internal/controller/status.go internal/controller/status_test.go
git -C /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl commit -m "feat(monitoring): set vinyl_vcl_versions_loaded gauge in status update"
```

---

### Task 6: Rewrite hit-ratio / backend-health alerts to exporter metrics

**Files:**
- Modify: `internal/monitoring/prometheusrule.go` (`allAlerts`)
- Test: `internal/monitoring/prometheusrule_test.go`

**Interfaces:**
- Produces: `GeneratePrometheusRule` still returns 10 alerts; `VinylCacheLowHitRatio` and `VinylCacheBackendUnhealthy` now reference `varnish_*` exporter metrics.

- [ ] **Step 1: Verify the exporter metric names**

Run the exporter image and capture its metric names so the alert exprs are correct:

```bash
docker run --rm --entrypoint sh ghcr.io/bluedynamics/varnish-exporter:1.6.1 -c 'prometheus_varnish_exporter --help' 2>&1 | head -40 || true
```

If the image cannot run without a VSM, confirm names from the prometheus_varnish_exporter README. Expected names: `varnish_main_cache_hit`, `varnish_main_cache_miss` (counters), and a per-backend health gauge (commonly `varnish_backend_up`; if the version exposes `varnish_backend_happy` instead, use that). Record the confirmed names and use them in Step 3.

- [ ] **Step 2: Write the failing test**

In `internal/monitoring/prometheusrule_test.go`:

```go
func TestPrometheusRule_HitRatioUsesExporterMetric(t *testing.T) {
	rule := GeneratePrometheusRule("cloud-vinyl")
	a := findAlert(rule, "VinylCacheLowHitRatio")
	require.NotNil(t, a)
	assert.Contains(t, a.Expr.String(), "varnish_main_cache_hit")
	assert.NotContains(t, a.Expr.String(), "vinyl_cache_hit_ratio")
}

func TestPrometheusRule_BackendHealthUsesExporterMetric(t *testing.T) {
	rule := GeneratePrometheusRule("cloud-vinyl")
	a := findAlert(rule, "VinylCacheBackendUnhealthy")
	require.NotNil(t, a)
	assert.NotContains(t, a.Expr.String(), "vinyl_backend_health")
}
```

(Use the existing `findAlert` helper if present; otherwise add a small one that loops `rule.Spec.Groups[0].Rules`.)

- [ ] **Step 3: Run test to verify it fails, then rewrite the two alerts**

Run: `(cd /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl && go test ./internal/monitoring/ -run TestPrometheusRule -v)` → FAIL.

In `allAlerts()`, replace the `VinylCacheLowHitRatio` expr:

```go
			Expr: intstr.FromString(`(sum(rate(varnish_main_cache_hit[5m])) by (namespace, pod)) / (sum(rate(varnish_main_cache_hit[5m])) by (namespace, pod) + sum(rate(varnish_main_cache_miss[5m])) by (namespace, pod)) < 0.5`),
```

and `VinylCacheBackendUnhealthy` expr (use the name confirmed in Step 1; `varnish_backend_up` shown):

```go
			Expr: intstr.FromString(`varnish_backend_up == 0`),
```

- [ ] **Step 4: Run test to verify pass + commit**

Run: `(cd /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl && go test ./internal/monitoring/ -v)` → PASS.

```bash
git -C /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl add internal/monitoring/prometheusrule.go internal/monitoring/prometheusrule_test.go
git -C /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl commit -m "feat(monitoring): point hit-ratio/backend alerts at varnish exporter metrics"
```

---

### Task 7: reconcileMonitoring — apply SM/PromRule as unstructured, CRD-gated

**Files:**
- Create: `internal/controller/monitoring.go` (`reconcileMonitoring`, `toUnstructured`, `crdInstalled`)
- Modify: `internal/controller/vinylcache_controller.go` (call `reconcileMonitoring` in `Reconcile`; add RBAC marker)
- Test: `internal/controller/monitoring_test.go`

**Interfaces:**
- Consumes: `monitoring.GenerateServiceMonitor(name, namespace)`, `monitoring.GeneratePrometheusRule(namespace)`.
- Produces: `(r *VinylCacheReconciler) reconcileMonitoring(ctx, vc) error` — applies a ServiceMonitor when `vc.Spec.Monitoring.ServiceMonitor != nil && .Enabled` and a PrometheusRule when `vc.Spec.Monitoring.PrometheusRules != nil && .Enabled`, each only if its CRD GVK is known to the RESTMapper; sets an OwnerReference; never errors when CRDs are absent.

- [ ] **Step 1: Write the failing test**

`internal/controller/monitoring_test.go`:

```go
func TestReconcileMonitoring_SkipsWhenCRDAbsent(t *testing.T) {
	sch := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(sch))
	vc := &v1alpha1.VinylCache{
		ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "ns1"},
		Spec: v1alpha1.VinylCacheSpec{
			Monitoring: v1alpha1.MonitoringSpec{
				ServiceMonitor:  &v1alpha1.ServiceMonitorSpec{Enabled: true},
				PrometheusRules: &v1alpha1.PrometheusRulesSpec{Enabled: true},
			},
		},
	}
	// fake client's RESTMapper does not know monitoring.coreos.com → must skip, no error.
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(vc).Build()
	r := &VinylCacheReconciler{Client: cli, Scheme: sch}
	require.NoError(t, r.reconcileMonitoring(context.Background(), vc))
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `(cd /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl && go test ./internal/controller/ -run TestReconcileMonitoring -v)`
Expected: FAIL — `reconcileMonitoring` undefined.

- [ ] **Step 3: Implement monitoring.go**

```go
package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
	"github.com/bluedynamics/cloud-vinyl/internal/monitoring"
)

var (
	serviceMonitorGVK = schema.GroupVersionKind{Group: "monitoring.coreos.com", Version: "v1", Kind: "ServiceMonitor"}
	prometheusRuleGVK = schema.GroupVersionKind{Group: "monitoring.coreos.com", Version: "v1", Kind: "PrometheusRule"}
)

// reconcileMonitoring creates/updates the ServiceMonitor and PrometheusRule when
// requested in the spec AND the prometheus-operator CRDs are installed. It never
// returns an error when the CRDs are absent — clusters without prometheus-operator
// must reconcile normally.
func (r *VinylCacheReconciler) reconcileMonitoring(ctx context.Context, vc *v1alpha1.VinylCache) error {
	log := logf.FromContext(ctx)

	if sm := vc.Spec.Monitoring.ServiceMonitor; sm != nil && sm.Enabled {
		if !r.crdInstalled(serviceMonitorGVK) {
			log.Info("ServiceMonitor requested but monitoring.coreos.com CRD not installed; skipping")
		} else {
			obj, err := toUnstructured(monitoring.GenerateServiceMonitor(vc.Name, vc.Namespace), serviceMonitorGVK)
			if err != nil {
				return err
			}
			if err := r.applyOwned(ctx, vc, obj); err != nil {
				return err
			}
		}
	}

	if pr := vc.Spec.Monitoring.PrometheusRules; pr != nil && pr.Enabled {
		if !r.crdInstalled(prometheusRuleGVK) {
			log.Info("PrometheusRule requested but monitoring.coreos.com CRD not installed; skipping")
		} else {
			obj, err := toUnstructured(monitoring.GeneratePrometheusRule(vc.Namespace), prometheusRuleGVK)
			if err != nil {
				return err
			}
			obj.SetNamespace(vc.Namespace)
			if err := r.applyOwned(ctx, vc, obj); err != nil {
				return err
			}
		}
	}
	return nil
}

// crdInstalled reports whether the cluster knows the given GVK.
func (r *VinylCacheReconciler) crdInstalled(gvk schema.GroupVersionKind) bool {
	mapper := r.Client.RESTMapper()
	if mapper == nil {
		return false
	}
	_, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	return err == nil
}

// toUnstructured converts any of our minimal monitoring structs to an
// Unstructured carrying the given GVK, so it can be applied without a typed
// prometheus-operator dependency.
func toUnstructured(in any, gvk schema.GroupVersionKind) (*unstructured.Unstructured, error) {
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(in)
	if err != nil {
		return nil, err
	}
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(gvk)
	return u, nil
}

// applyOwned sets the controller owner reference and create-or-updates the object.
func (r *VinylCacheReconciler) applyOwned(ctx context.Context, vc *v1alpha1.VinylCache, obj *unstructured.Unstructured) error {
	if err := controllerutil.SetControllerReference(vc, obj, r.Scheme); err != nil {
		return err
	}
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(obj.GroupVersionKind())
	key := client.ObjectKeyFromObject(obj)
	if err := r.Get(ctx, key, existing); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return err
		}
		return r.Create(ctx, obj)
	}
	obj.SetResourceVersion(existing.GetResourceVersion())
	return r.Update(ctx, obj)
}
```

- [ ] **Step 4: Call it from Reconcile + RBAC marker**

In `internal/controller/vinylcache_controller.go`, after the services step (step 6) add:

```go
	// 6b. Reconcile optional monitoring resources (ServiceMonitor / PrometheusRule).
	if err := r.reconcileMonitoring(ctx, vc); err != nil {
		return ctrl.Result{}, err
	}
```

Add the RBAC kubebuilder marker near the others:

```go
// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=servicemonitors;prometheusrules,verbs=get;list;watch;create;update;patch;delete
```

- [ ] **Step 5: Run test to verify pass**

Run: `(cd /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl && go test ./internal/controller/ -run TestReconcileMonitoring -v)` → PASS.

- [ ] **Step 6: Regenerate RBAC manifests + commit**

```bash
(cd /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl && make manifests && go build ./...)
git -C /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl add internal/controller/monitoring.go internal/controller/monitoring_test.go internal/controller/vinylcache_controller.go config/
git -C /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl commit -m "feat(monitoring): generate+apply ServiceMonitor/PrometheusRule (CRD-gated)"
```

---

### Task 8: CRD ExporterSpec

**Files:**
- Modify: `api/v1alpha1/vinylcache_types.go` (`MonitoringSpec` + new `ExporterSpec`)
- Generate: `api/v1alpha1/zz_generated.deepcopy.go`, `config/crd/...`
- Test: `api/v1alpha1/` compile (deepcopy) + a small defaulting/round-trip test if a types_test exists

**Interfaces:**
- Produces: `MonitoringSpec.Exporter *ExporterSpec`; `type ExporterSpec struct { Enabled bool; Image ExporterImageSpec; Resources corev1.ResourceRequirements; Port int32 }`; `type ExporterImageSpec struct { Repository string; Tag string }`.

- [ ] **Step 1: Add the types**

In `api/v1alpha1/vinylcache_types.go`, extend `MonitoringSpec`:

```go
	// exporter configures the prometheus_varnish_exporter sidecar that exposes
	// native varnish_* metrics (cache hit/miss, backend health) from varnishstat.
	// +optional
	Exporter *ExporterSpec `json:"exporter,omitempty"`
```

Add (ensure `corev1 "k8s.io/api/core/v1"` is imported in this file):

```go
// ExporterSpec configures the varnish metrics exporter sidecar.
type ExporterSpec struct {
	// enabled adds a prometheus_varnish_exporter sidecar to each Varnish pod.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// image overrides the exporter image. Defaults to
	// ghcr.io/bluedynamics/varnish-exporter:1.6.1.
	// +optional
	Image ExporterImageSpec `json:"image,omitempty"`

	// port is the container port the exporter listens on. Defaults to 9131.
	// +optional
	Port int32 `json:"port,omitempty"`

	// resources sets the exporter container resource requirements.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// ExporterImageSpec is the exporter container image reference.
type ExporterImageSpec struct {
	// +optional
	Repository string `json:"repository,omitempty"`
	// +optional
	Tag string `json:"tag,omitempty"`
}
```

- [ ] **Step 2: Generate deepcopy + manifests**

Run:

```bash
(cd /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl && make generate manifests)
```

Expected: `zz_generated.deepcopy.go` gains `DeepCopy` for `ExporterSpec`/`ExporterImageSpec`; `config/crd/bases/*vinylcaches.yaml` gains the `exporter` properties. `go build ./...` succeeds.

- [ ] **Step 3: Verify build + commit**

```bash
(cd /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl && go build ./... && go test ./api/...)
git -C /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl add api/v1alpha1/ config/crd/
git -C /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl commit -m "feat(api): add MonitoringSpec.exporter (varnish-exporter sidecar config)"
```

---

### Task 9: Exporter sidecar in StatefulSet + Service port

**Files:**
- Modify: `internal/controller/statefulset.go` (append exporter container when enabled)
- Modify: `internal/controller/service.go` (add `exporter` port to the headless service)
- Test: `internal/controller/statefulset_test.go`

**Interfaces:**
- Consumes: `vc.Spec.Monitoring.Exporter` (Task 8).
- Produces: a container named `vinyl-exporter` on the StatefulSet pod spec when `Exporter != nil && Exporter.Enabled`, mounting `varnish-workdir` at `/var/lib/varnish` read-only, exposing a port named `exporter`; the headless Service exposes a matching `exporter` `ServicePort`.

- [ ] **Step 1: Write the failing test**

Add to `internal/controller/statefulset_test.go`:

```go
func TestStatefulSet_ExporterSidecarWhenEnabled(t *testing.T) {
	vc := baseVinylCache() // reuse existing test helper that builds a minimal VC
	vc.Spec.Monitoring.Exporter = &v1alpha1.ExporterSpec{Enabled: true}

	sts := buildStatefulSet(vc) // use the same constructor the existing tests use

	var exporter *corev1.Container
	for i := range sts.Spec.Template.Spec.Containers {
		if sts.Spec.Template.Spec.Containers[i].Name == "vinyl-exporter" {
			exporter = &sts.Spec.Template.Spec.Containers[i]
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
	require.NotNil(t, mount)
	assert.True(t, mount.ReadOnly)
}

func TestStatefulSet_NoExporterByDefault(t *testing.T) {
	sts := buildStatefulSet(baseVinylCache())
	for _, c := range sts.Spec.Template.Spec.Containers {
		assert.NotEqual(t, "vinyl-exporter", c.Name)
	}
}
```

If the existing tests call the unexported builder differently (e.g. via `reconcileStatefulSet` + fake client), match that pattern instead — read `statefulset_test.go` first and reuse its helpers/constructor names.

- [ ] **Step 2: Run test to verify it fails**

Run: `(cd /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl && go test ./internal/controller/ -run TestStatefulSet_Exporter -v)`
Expected: FAIL — no `vinyl-exporter` container.

- [ ] **Step 3: Add the sidecar**

In `internal/controller/statefulset.go`, add a constant near the others:

```go
const (
	exporterPort           = int32(9131)
	defaultExporterImage   = "ghcr.io/bluedynamics/varnish-exporter:1.6.1"
)
```

Before the line that builds `Containers: []corev1.Container{varnishContainer, agentContainer}`, assemble the slice and conditionally append the exporter:

```go
	containers := []corev1.Container{varnishContainer, agentContainer}
	if exp := vc.Spec.Monitoring.Exporter; exp != nil && exp.Enabled {
		image := defaultExporterImage
		if exp.Image.Repository != "" {
			tag := exp.Image.Tag
			if tag == "" {
				tag = "latest"
			}
			image = exp.Image.Repository + ":" + tag
		}
		port := exporterPort
		if exp.Port != 0 {
			port = exp.Port
		}
		containers = append(containers, corev1.Container{
			Name:  "vinyl-exporter",
			Image: image,
			Ports: []corev1.ContainerPort{
				{Name: "exporter", ContainerPort: port, Protocol: corev1.ProtocolTCP},
			},
			VolumeMounts: []corev1.VolumeMount{
				{Name: "varnish-workdir", MountPath: "/var/lib/varnish", ReadOnly: true},
			},
			SecurityContext: &corev1.SecurityContext{
				RunAsNonRoot:             boolPtr(true),
				ReadOnlyRootFilesystem:   boolPtr(true),
				AllowPrivilegeEscalation: boolPtr(false),
			},
			Resources: exp.Resources,
		})
	}
```

Then use `containers` in the pod spec (replace the inline `[]corev1.Container{varnishContainer, agentContainer}`).

- [ ] **Step 4: Add the Service port**

In `internal/controller/service.go`, in `reconcileHeadlessService`, append to the `Ports` slice (only meaningful when the exporter is enabled, but a static extra port on the headless service is harmless and keeps the ServiceMonitor selector simple):

```go
				{
					Name:       "exporter",
					Port:       exporterPort,
					TargetPort: intstr.FromInt32(exporterPort),
				},
```

(`exporterPort` is now defined in the controller package from Step 3.)

- [ ] **Step 5: Run tests to verify pass**

Run: `(cd /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl && go test ./internal/controller/ -run TestStatefulSet -v)` → PASS.
Run full controller package: `(cd /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl && go test ./internal/controller/)` → PASS.

- [ ] **Step 6: Commit**

```bash
git -C /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl add internal/controller/statefulset.go internal/controller/service.go internal/controller/statefulset_test.go
git -C /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl commit -m "feat(monitoring): add varnish-exporter sidecar and exporter service port"
```

---

### Task 10: Docs + CHANGES + full suite

**Files:**
- Create: `docs/sources/reference/metrics.md`
- Create: `docs/sources/how-to/setup-monitoring.md`
- Modify: `CHANGES.md` (or repo's changelog file — confirm the exact name)

**Interfaces:** none (docs only).

- [ ] **Step 1: Write the reference doc**

Create `docs/sources/reference/metrics.md` documenting every emitted metric: for each of `vinyl_vcl_push_total`, `vinyl_vcl_push_duration_seconds`, `vinyl_invalidation_total`, `vinyl_invalidation_duration_seconds`, `vinyl_broadcast_total`, `vinyl_partial_failure_total`, `vinyl_vcl_versions_loaded`, `vinyl_reconcile_total`, `vinyl_reconcile_duration_seconds` — name, type, labels, meaning. Add a section "Varnish cache metrics" noting these come from the exporter (`varnish_main_cache_hit`, `varnish_main_cache_miss`, backend health) and that hit ratio is computed in PromQL/Grafana.

- [ ] **Step 2: Write the how-to**

Create `docs/sources/how-to/setup-monitoring.md`: prerequisites (prometheus-operator CRDs), enabling `spec.monitoring` (`serviceMonitor.enabled`, `prometheusRules.enabled`, `exporter.enabled`), and a PromQL snippet for the hit-rate panel:

```promql
sum(rate(varnish_main_cache_hit[5m])) by (namespace)
  / (sum(rate(varnish_main_cache_hit[5m])) by (namespace)
     + sum(rate(varnish_main_cache_miss[5m])) by (namespace))
```

- [ ] **Step 3: CHANGES entry**

Confirm the changelog filename (`(cd /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl && ls CHANGE*)`) and add an entry summarizing: metrics wired into reconciler/proxy; ServiceMonitor/PrometheusRule now generated and applied (CRD-gated); varnish-exporter sidecar added; redundant hit-ratio/backend-health gauges removed (exporter is the source). Reference issue #48.

- [ ] **Step 4: Run the full suite + commit**

```bash
(cd /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl && make test)
git -C /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl add docs/ CHANGES.md
git -C /home/jensens/ws/cdev/cloudbrine/sources/cloud-vinyl commit -m "docs(monitoring): metrics reference + setup how-to; CHANGES for #48"
```

---

## Notes for the implementer

- The proxy `BroadcastResult.Results[].Pod` already carries the `ip:port` string used as the `pod` label — no extra plumbing needed.
- `handleBAN` previously took `namespace`; when adding `cacheName`, keep a single `namespace` param (don't duplicate).
- If `make manifests`/`make generate` require `controller-gen` and it isn't installed, the Makefile's `make manifests` target installs it into `bin/` automatically — run from the repo root.
- E2E (chainsaw): out of scope for this plan beyond unit/envtest. If you add one, keep parallelism low (the repo recently reduced chainsaw parallelism to 2 due to webhook overload under parallel load).
