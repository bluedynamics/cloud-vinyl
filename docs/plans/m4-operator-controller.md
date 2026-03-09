# M4: Operator Controller

**Status:** Bereit nach M1, M2, M3
**Voraussetzungen:** M1 (CRD-Typen), M2 (VCL-Generator), M3 (vinyl-agent — für Agent-Client-Interface)
**Parallelisierbar mit:** M5 (Purge/BAN-Proxy), M6 (Monitoring)
**Geschätzte Team-Größe:** 2 Personen

---

## Kontext

Der Controller ist das Herzstück des Operators. Er beobachtet `VinylCache`-Objekte und reconcilt den gewünschten Zustand: StatefulSet, Services, EndpointSlice, NetworkPolicies, Secrets anlegen/aktualisieren, VCL generieren und auf alle Pods pushen.

Dies ist das komplexeste Modul — hier kommen alle anderen zusammen. Die Qualität des Reconcile-Loops bestimmt die Produktionsreife des gesamten Operators.

**Relevante Architektur-Abschnitte:**
- §3.2 — Operator-Komponenten (Controller-Manager, VCL-Generator, Proxy — Überblick)
- §6.2 — EndpointSlice: cross-namespace Service + manuell gepflegter EndpointSlice (kritisches Architektur-Detail)
- §7.1 — Watches: welche Ressourcen beobachtet werden und warum
- §7.2 — Reconcile-Loop: vollständiges Flussdiagramm der 11 Schritte
- §7.3 — Fehlerbehandlung: VCL-Push-Fehler, Kompilierungsfehler, Backend-Down
- §7.4 — Debouncing: Implementierungsdetail, Interaktion mit Shard-Director
- §7.5 — Leader-Election: welche Komponenten unter Leader-Election stehen
- §7.7 — Blast-Radius / Produktionssicherheit: canary, pause, rollback Annotations
- §7.8 — Drift-Erkennung: periodischer Reconcile + VCLConsistent Condition
- §9.7 — StatefulSet vs. Deployment: Entscheidung + podManagementPolicy: Parallel

---

## Ziel

Nach M4 existiert:
- Vollständiger `VinylCacheReconciler` der einen `VinylCache` von Erstellung bis Löschung managed
- Alle Kubernetes-Ressourcen werden korrekt erstellt, aktualisiert und gelöscht
- VCL-Push funktioniert mit Retry und Fehlerbehandlung
- Status und Conditions werden korrekt gesetzt
- Envtest-basierte Integrationstests decken den vollständigen Lifecycle ab

---

## Package-Struktur

```
internal/controller/
├── reconciler.go          # VinylCacheReconciler: Haupt-Einstiegspunkt
├── reconciler_test.go     # Integrationstests (envtest): vollständiger Lifecycle
├── statefulset.go         # StatefulSet reconcilen
├── statefulset_test.go
├── service.go             # headless + traffic + invalidation Service
├── service_test.go
├── endpointslice.go       # Cross-Namespace EndpointSlice (§6.2)
├── endpointslice_test.go
├── networkpolicy.go       # NetworkPolicies (§10.4)
├── networkpolicy_test.go
├── secret.go              # Agent-Token Secret (§9.3)
├── secret_test.go
├── vcl_push.go            # VCL generieren + parallel an Pods pushen
├── vcl_push_test.go       # Unit: Retry-Logik, Partial-Failure, Hash-Vergleich
├── status.go              # Status + Conditions berechnen und schreiben
├── status_test.go         # Unit: Phase-Berechnung, Condition-Logik
├── finalizer.go           # Cleanup bei Deletion (cross-namespace Ressourcen)
├── finalizer_test.go
└── agent_client.go        # HTTP-Client für vinyl-agent API (Interface + Impl)
```

---

## Schlüssel-Interfaces

```go
// internal/controller/agent_client.go

// AgentClient ist der HTTP-Client des Operators für die vinyl-agent API.
// Hinter einem Interface damit Controller-Tests keinen echten Agent brauchen.
type AgentClient interface {
    PushVCL(ctx context.Context, podIP string, name, vcl string) error
    ValidateVCL(ctx context.Context, podIP string, vcl string) error
    ActiveVCLHash(ctx context.Context, podIP string) (string, error)
}

// internal/controller/reconciler.go

type VinylCacheReconciler struct {
    client.Client
    Scheme      *runtime.Scheme
    Generator   generator.Generator   // Interface aus M2 — mockbar
    AgentClient AgentClient           // Interface — mockbar
    // Metrics aus M6 werden hier injiziert
}
```

**Warum Interfaces für Generator und AgentClient?** Der Controller-Test testet ausschließlich die Controller-Logik (Reconcile-Ablauf, Status-Setzung, Kubernetes-Ressourcen). Er mockt Generator und AgentClient — er braucht keine echte VCL und keinen echten Varnish.

---

## Reconcile-Loop-Struktur

Der vollständige Ablauf ist in **architektur.md §7.2** als Flussdiagramm dokumentiert. Implementierungs-Skelett:

```go
// internal/controller/reconciler.go

func (r *VinylCacheReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    // 1. VinylCache laden — bei NotFound: fertig (wurde gelöscht)
    vc := &v1alpha1.VinylCache{}
    if err := r.Get(ctx, req.NamespacedName, vc); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }

    // 2. Deletion-Check: wenn DeletionTimestamp gesetzt → Finalizer-Handler
    if !vc.DeletionTimestamp.IsZero() {
        return r.handleDeletion(ctx, vc)
    }

    // 3. Finalizer sicherstellen
    if err := r.ensureFinalizer(ctx, vc); err != nil {
        return ctrl.Result{}, err
    }

    // 4–6. Kubernetes-Ressourcen reconcilen
    if err := r.reconcileStatefulSet(ctx, vc); err != nil { ... }
    if err := r.reconcileServices(ctx, vc); err != nil { ... }
    if err := r.reconcileEndpointSlice(ctx, vc); err != nil { ... }
    if err := r.reconcileNetworkPolicies(ctx, vc); err != nil { ... }
    if err := r.reconcileSecret(ctx, vc); err != nil { ... }

    // 7. Debouncing prüfen (§7.4)
    if remaining := r.debounceRemaining(vc); remaining > 0 {
        return ctrl.Result{RequeueAfter: remaining}, nil
    }

    // 8. Aktuelle Pod-IPs sammeln
    peers, err := r.collectReadyPeers(ctx, vc)

    // 9. VCL generieren
    result, err := r.Generator.Generate(generator.Input{Spec: &vc.Spec, Peers: peers, ...})

    // 10. Hash vergleichen — bei Übereinstimmung: kein Push nötig
    if result.Hash != vc.Status.ActiveVCL.Hash {
        if err := r.pushVCL(ctx, vc, result, peers); err != nil { ... }
    }

    // 11. Status schreiben + periodischer Requeue für Drift-Erkennung (§7.8)
    r.updateStatus(ctx, vc, result, peers)
    return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}
```

---

## TDD-Workflow

### Schritt 1: Status-Logik unit-testen (keine Kubernetes-Abhängigkeit)

```go
// internal/controller/status_test.go

func TestCalculatePhase_AllReady_ReturnsReady(t *testing.T) { ... }
func TestCalculatePhase_VCLNotSynced_ReturnsDegraded(t *testing.T) { ... }
func TestCalculatePhase_NoPodsReady_ReturnsPending(t *testing.T) { ... }
func TestCondition_VCLSynced_PartialFailure(t *testing.T) { ... }
func TestCondition_ObservedGeneration_AlwaysSet(t *testing.T) { ... }
// Phase-Berechnungslogik: architektur.md §2.2 (Kommentar-Block über status.phase)
```

### Schritt 2: VCL-Push-Logik unit-testen (Mock-AgentClient)

```go
// internal/controller/vcl_push_test.go

func TestPushVCL_AllPodsSuccess_SetsVCLSyncedTrue(t *testing.T) { ... }
func TestPushVCL_OnePodsFailure_SetsVCLSyncedFalse(t *testing.T) { ... }
func TestPushVCL_AllPodsFailure_RequeuesWithBackoff(t *testing.T) { ... }
func TestPushVCL_SameHash_NoPushOccurs(t *testing.T) { ... }
func TestPushVCL_CompilationError_NoRetry(t *testing.T) { ... }
// Fehlerbehandlung: architektur.md §7.3
```

### Schritt 3: Vollständiger Lifecycle (envtest)

```go
// internal/controller/reconciler_test.go
//go:build integration

func TestReconcile_CreateVinylCache_AllResourcesAppear(t *testing.T) {
    // VinylCache erstellen → StatefulSet, Services, EndpointSlice, NetworkPolicy, Secret erscheinen
}

func TestReconcile_DeleteVinylCache_FinalizerCleansUp(t *testing.T) {
    // Löschen → Finalizer räumt EndpointSlice + InvalidationService auf
}

func TestReconcile_SpecChange_TriggersVCLPush(t *testing.T) { ... }
func TestReconcile_PodNotReady_NoVCLPush(t *testing.T) { ... }
func TestReconcile_SameHash_NoPushTriggered(t *testing.T) { ... }
func TestReconcile_DriftDetection_RequeuesAfter5min(t *testing.T) { ... }
```

### Kritisches Detail: EndpointSlice (§6.2)

Der Invalidierungs-Service liegt im Namespace des `VinylCache`. Der EndpointSlice darin zeigt auf den Operator-Pod (anderer Namespace). Cross-Namespace-OwnerReferences sind verboten → Finalizer ist Pflicht.

```go
// internal/controller/endpointslice.go

// reconcileEndpointSlice erstellt/aktualisiert den EndpointSlice im VinylCache-Namespace.
// Der EndpointSlice enthält die IP(s) des Operator-Pods.
// Cleanup erfolgt NICHT via OwnerReference sondern über den Finalizer (handleDeletion).
func (r *VinylCacheReconciler) reconcileEndpointSlice(ctx context.Context, vc *v1alpha1.VinylCache) error {
    // Operator-Pod-IP ermitteln (aus Pod-Spec via Downward API oder Env-Var)
    // EndpointSlice im vc.Namespace anlegen/aktualisieren
    // Label: kubernetes.io/service-name: <vc.Name>-invalidation
}
```

### Blast-Radius-Schutz (§7.7)

Der Controller prüft Annotations für Produktionssicherheit:

```go
// Pause-Annotation: kein VCL-Push wenn gesetzt
if vc.Annotations["vinyl.bluedynamics.eu/pause-vcl-push"] == "true" {
    // Status-Event: "VCL push paused by annotation"
    return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}
```

---

## Watch-Konfiguration

Details in **architektur.md §7.1**:

```go
// internal/controller/reconciler.go

func (r *VinylCacheReconciler) SetupWithManager(mgr ctrl.Manager) error {
    return ctrl.NewControllerManagedBy(mgr).
        For(&v1alpha1.VinylCache{}).
        Owns(&appsv1.StatefulSet{}).
        Owns(&corev1.Service{}).
        Owns(&corev1.Secret{}).
        Watches(
            &corev1.Pod{},
            handler.EnqueueRequestsFromMapFunc(r.podToVinylCache),
            builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
        ).
        Watches(
            &corev1.Endpoints{},
            handler.EnqueueRequestsFromMapFunc(r.endpointsToVinylCache),
        ).
        Complete(r)
}
```

---

## Dokumentations-Deliverables

| Datei | Diataxis | Inhalt | Wann |
|-------|----------|--------|------|
| `docs/sources/explanation/reconcile-loop.md` | Explanation | Reconcile-Ablauf mit Mermaid-Flussdiagramm | Parallel |
| `docs/sources/reference/operator-flags.md` | Reference | Alle `--flag`-Optionen (leader-elect, metrics-addr, ...) | Nach main.go |
| `docs/sources/how-to/ha-setup.md` | How-To | Leader-Election, 2+ Operator-Replicas | Nach M4 |
| `docs/sources/how-to/debug-vcl.md` | How-To | VCL-Probleme diagnostizieren (Status lesen, Events, Logs) | Nach M4 |

---

## Akzeptanzkriterien

- [ ] Vollständiger Create/Update/Delete-Lifecycle in envtest-Tests grün
- [ ] Finalizer räumt cross-namespace Ressourcen korrekt auf (EndpointSlice + Service im VinylCache-Namespace)
- [ ] VCL-Push: Retry mit Backoff bei Verbindungsfehler, kein Retry bei Kompilierungsfehler
- [ ] Status-Conditions korrekt: Ready, VCLSynced, BackendsAvailable, Progressing (alle mit observedGeneration)
- [ ] Debouncing: kein sofortiger Push bei frequenten Events
- [ ] Pause-Annotation wird respektiert
- [ ] Periodischer Requeue nach 5min (Drift-Erkennung)
- [ ] `go test -race` ohne Data-Race-Fehler
- [ ] Coverage `internal/controller` ≥ 80%, Sub-Packages (status, vcl_push) ≥ 90%

---

## Offene Fragen

1. **Operator-Pod-IP ermitteln:** Wie bekommt der Controller seine eigene Pod-IP für den EndpointSlice? Optionen: Downward API (`status.podIP` als Env-Var), Kubernetes API (Get own Pod). Downward API ist einfacher und robuster.
2. **envtest-Setup:** `sigs.k8s.io/controller-runtime/pkg/envtest` braucht `etcd` und `kube-apiserver` Binaries. Diese werden via `setup-envtest` Tool heruntergeladen. In `Makefile` und CI einrichten.
3. **AgentClient-Timeout:** Wie lange wartet der Controller auf eine Antwort vom Agent? Konfigurierbar oder fest? Empfehlung: 30s Timeout, konfigurierbar via Operator-Flag.
