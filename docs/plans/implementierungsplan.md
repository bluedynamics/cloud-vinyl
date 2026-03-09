# cloud-vinyl Implementierungsplan

**Datum:** 2026-03-08
**Bezug:** architektur.md (alle Entscheidungen getroffen)
**Ziel:** Modularer Implementierungsplan geeignet für parallele Agent-Teams

---

## Überblick

### Prinzipien

- **TDD (Test-Driven Development):** Tests werden vor der Implementierung geschrieben. Kein Produktionscode ohne vorhergehenden fehlschlagenden Test.
- **Coverage-Ziele:** >80% (Muss), >90% (Gewünscht) — gemessen mit `go test -coverprofile` + `codecov`.
- **Dokumentation first-class:** Jedes Modul liefert Sphinx-Dokumentation nach dem Diataxis-Prinzip als Akzeptanzkriterium.
- **Unabhängige Module:** Jedes Modul hat klar definierte Inputs, Outputs und Interfaces — parallele Bearbeitung durch separate Teams möglich.
- **Kein Merge ohne grüne CI:** Lint (golangci-lint + Ruff für Docs), Tests, Coverage-Gate, Docs-Build.

### Technologie-Stack

| Bereich | Wahl | Begründung |
|---------|------|-----------|
| Sprache | Go 1.23+ | controller-runtime Ökosystem |
| Scaffolding | kubebuilder v4 | Generiert Controller, Webhook, CRD-Marker |
| Testing | `go test` + `envtest` + `testcontainers-go` | Unit + Integration ohne echten Cluster |
| E2E | `kind` + `chainsaw` (kyverno) | Deklarative E2E-Tests auf echtem Cluster |
| Linting | `golangci-lint` | staticcheck, errcheck, revive, gosec |
| Coverage | `go test -coverprofile` + `codecov` | Per-Package-Coverage, PR-Gate |
| Docs | Sphinx + MyST + Shibuya + mxmake | Identisch zu plone-pgcatalog Setup |
| CI | GitHub Actions | Lint, Test, Coverage, Docs-Deploy |

### Diataxis-Dokumentationsstruktur

```
docs/
├── Makefile          # mxmake-generiert
├── mx.ini
├── plans/            # Planungsdokumente (dieses Dokument gehört hierher)
└── sources/
    ├── conf.py
    ├── index.md
    ├── _static/
    ├── _templates/
    ├── tutorials/    # Lernorientiert: Quickstart, erstes VinylCache-Objekt
    ├── how-to/       # Aufgabenorientiert: Installation, Monitoring einrichten, Purge konfigurieren
    ├── reference/    # Informationsorientiert: CRD-Felder, Operator-Flags, Metriken, Agent-API
    └── explanation/  # Verständnisorientiert: Architektur, Clustering, VCL-Generierung, Grace/Purge
```

---

## Modul-Abhängigkeiten

```
M0 (Project Setup)
    │
    ├── M1 (CRD & Go Types) ──────────────────────────┐
    │       │                                          │
    │       ├── M2 (VCL Generator)                    │
    │       │                                          │
    │       └── M3 (vinyl-agent) ◄──── unabhängig     │
    │               │                                  │
    │           M4 (Operator Controller) ◄─────────────┘
    │               │
    │           M5 (Purge/BAN-Proxy) ◄──── parallel zu M4 möglich
    │               │
    │           M6 (Monitoring) ◄────────── parallel zu M5 möglich
    │               │
    │           M7 (Helm Chart)
    │               │
    └──────────── M8 (E2E Tests)
```

**Parallelisierungsmöglichkeiten:**
- M1 + M3 können gleichzeitig starten (M3 braucht keine Go-Types, nur das Admin-Protokoll)
- M2 + M3 können nach M1 parallel laufen
- M5 + M6 können parallel zu M4 entwickelt werden (Interfaces bekannt nach M1)
- Docs-Arbeit läuft kontinuierlich parallel zur Implementierung

---

## M0: Project Setup & Tooling

**Team-Größe:** 1 Person
**Voraussetzungen:** keine
**Output:** Lauffähiges Repo-Skeleton, CI grün, Docs-Build grün

### Aufgaben

#### M0.1 Repository-Grundstruktur

```
cloud-vinyl/
├── .github/
│   └── workflows/
│       ├── ci.yml          # lint, test, coverage, docs
│       └── release.yml     # goreleaser, container image
├── cmd/
│   ├── operator/
│   │   └── main.go
│   └── agent/
│       └── main.go
├── internal/
│   ├── controller/
│   ├── generator/
│   ├── proxy/
│   ├── agent/
│   └── monitoring/
├── api/
│   └── v1alpha1/
├── config/              # kubebuilder-generierte Kustomize-Manifeste
├── docs/                # Sphinx-Setup (identisch plone-pgcatalog)
├── hack/                # Skripte (generate, lint, test)
├── go.mod
├── go.sum
├── Makefile
└── pyproject.toml       # Für Docs-Tooling (Sphinx, mxmake)
```

#### M0.2 kubebuilder-Scaffolding

```bash
kubebuilder init --domain bluedynamics.eu --repo github.com/bluedynamics/cloud-vinyl
kubebuilder create api --group vinyl --version v1alpha1 --kind VinylCache --resource --controller
kubebuilder create webhook --group vinyl --version v1alpha1 --kind VinylCache --defaulting --programmatic-validation
```

#### M0.3 CI/CD Pipeline (GitHub Actions)

```yaml
# .github/workflows/ci.yml
jobs:
  lint:       # golangci-lint
  test:       # go test ./... -coverprofile=coverage.out
  coverage:   # codecov upload, fail if <80%
  docs:       # sphinx-build -W (Warnings als Fehler)
  build:      # go build ./cmd/operator ./cmd/agent
```

#### M0.4 Docs-Skeleton

- Sphinx-Setup identisch zu plone-pgcatalog (conf.py, Shibuya-Theme, MyST, mermaid, sphinx_design, sphinx_copybutton)
- Alle vier Diataxis-Verzeichnisse mit Platzhalter-index.md
- `docs/plans/implementierungsplan.md` (dieses Dokument)
- `make docs` und `make docs-live` funktionieren

#### M0.5 Makefile-Targets

```makefile
make install       # go mod download + docs deps
make generate      # controller-gen (CRD manifests, deepcopy, RBAC)
make lint          # golangci-lint run
make test          # go test ./... -race -coverprofile=coverage.out
make test-int      # Integrationstests (envtest)
make test-e2e      # E2E (kind)
make coverage      # go tool cover -html=coverage.out
make docs          # sphinx-build
make docs-live     # sphinx-autobuild
make build         # go build operator + agent
make docker-build  # docker buildx
```

### Akzeptanzkriterien

- [ ] `make lint` ohne Fehler
- [ ] `make test` grün (auch wenn noch keine Tests existieren)
- [ ] `make docs` grün (Sphinx-Build ohne Warnungen)
- [ ] GitHub Actions CI läuft durch bei jedem Push
- [ ] `go build ./cmd/...` kompiliert

---

## M1: CRD & Go Types

**Team-Größe:** 1–2 Personen
**Voraussetzungen:** M0
**Output:** Vollständige Go-Typen, CRD-YAML, Webhook, CEL-Validierung

### TDD-Ansatz

Tests werden für folgende Bereiche geschrieben, bevor der Code existiert:

1. **Webhook-Unit-Tests** (validating + mutating): Fehlerfall-Matrix (verbotene Parameter, ungültige CIDRs, leere Backends-Liste, verbotene Storage-Typen)
2. **CEL-Integrationstests** via envtest: ungültige Objekte werden abgelehnt
3. **Defaulting-Tests**: Felder mit Default-Werten werden korrekt gesetzt

### Aufgaben

#### M1.1 Go-Typen (`api/v1alpha1/`)

- `vinylcache_types.go`: vollständige Spec + Status nach architektur.md §2.2
  - `metav1.Condition` für alle Status-Conditions (mit `observedGeneration`)
  - `resource.Quantity` für Storage-Größen
  - `metav1.Duration` für alle Duration-Felder
- `vinylcache_webhook.go`: Validating + Mutating Webhook
- `groupversion_info.go`: API-Gruppe `vinyl.bluedynamics.eu/v1alpha1`
- `zz_generated.deepcopy.go`: via `controller-gen`

#### M1.2 kubebuilder-Marker

```go
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:subresource:scale:specpath=.spec.replicas,...
// +kubebuilder:resource:shortName=vc,categories=vinyl
// +kubebuilder:printcolumn:...
```

#### M1.3 CEL-Validierungsregeln (im CRD-Schema)

- Mindestens ein Backend
- Backend-Namen: VCL-konforme Bezeichner (`^[a-zA-Z][a-zA-Z0-9_]*$`)
- varnishParameters-Blocklist
- Snippet-Größenlimit (64KB)
- Director-Union-Konsistenz

#### M1.4 Admission Webhook (Validating)

Blocklists:
- `spec.varnishParameters`: `vcc_allow_inline_c`, `cc_command`, `feature +esi_disable_xml_check`
- `spec.storage[].type`: `persistent`, `umem`, `default`
- `spec.backends[].serviceRef.namespace`: muss leer sein (kein Cross-Namespace)
- `spec.invalidation.*.allowedSources`: CIDR-Syntax

#### M1.5 Admission Webhook (Mutating/Defaulting)

- `spec.invalidation.purge.soft` → `true` wenn nicht gesetzt
- `spec.xkey.softPurge` → `true` wenn nicht gesetzt
- `spec.director.shard.warmup` → `0.1`
- `spec.director.shard.rampup` → `30s`
- `spec.cluster.peerRouting.type` → `shard`
- `podManagementPolicy` → `Parallel`

### Tests

```
internal/
└── webhook/
    ├── vinylcache_webhook_test.go    # Unit: Validierungslogik
    └── vinylcache_defaulting_test.go # Unit: Default-Werte

api/v1alpha1/
└── vinylcache_cel_test.go           # Integration (envtest): CEL-Regeln
```

**Coverage-Ziel:** >90% für webhook-Package

### Dokumentation

- `docs/sources/reference/crd-fields.md`: Vollständige Feldreferenz (generiert aus Go-Typen via go-doc + manuell erweitert)
- `docs/sources/explanation/api-design.md`: Warum die API so strukturiert ist (aus architektur.md §2 destilliert)

### Akzeptanzkriterien

- [ ] `make generate` erzeugt valides CRD-YAML
- [ ] Webhook lehnt ungültige Objekte mit klarer Fehlermeldung ab
- [ ] Defaulting setzt alle definierten Defaults
- [ ] CEL-Regeln greifen ohne Webhook-Roundtrip
- [ ] Coverage webhook-Package >90%
- [ ] CRD-Feldreferenz in Docs vollständig

---

## M2: VCL-Generator

**Team-Größe:** 1–2 Personen
**Voraussetzungen:** M1
**Output:** Deterministischer, vollständig testbarer VCL-Generator

### TDD-Ansatz

**Golden-File-Tests:** Für jede Kombination aus Spec-Konfiguration wird eine Referenz-VCL-Datei eingecheckt. Der Test generiert VCL und vergleicht mit der Golden-File. Bei Abweichung schlägt der Test fehl — kein stiller Regressionsschutz.

```
internal/generator/
├── testdata/
│   ├── single-backend-no-cluster.vcl       # Golden File
│   ├── multi-backend-shard-cluster.vcl
│   ├── xkey-enabled.vcl
│   ├── esi-enabled.vcl
│   ├── soft-purge-enabled.vcl
│   ├── proxy-protocol-enabled.vcl
│   ├── full-override-mode.vcl
│   └── ...
├── generator.go
├── generator_test.go      # Golden-File-Tests
├── templates/
│   ├── header.vcl.tmpl
│   ├── vcl_init.vcl.tmpl
│   ├── vcl_recv.vcl.tmpl
│   ├── vcl_hash.vcl.tmpl
│   ├── vcl_hit.vcl.tmpl
│   ├── vcl_miss.vcl.tmpl
│   ├── vcl_pass.vcl.tmpl
│   ├── vcl_backend_fetch.vcl.tmpl
│   ├── vcl_backend_response.vcl.tmpl
│   ├── vcl_deliver.vcl.tmpl
│   ├── vcl_pipe.vcl.tmpl
│   ├── vcl_purge.vcl.tmpl
│   ├── vcl_synth.vcl.tmpl
│   └── vcl_fini.vcl.tmpl
└── hash.go                # SHA-256 des generierten VCL-Strings
```

### Aufgaben

#### M2.1 Generator-Interface

```go
type Generator interface {
    Generate(spec *v1alpha1.VinylCacheSpec, peers []PeerBackend, endpoints map[string][]Endpoint) (*Result, error)
}

type Result struct {
    VCL  string
    Hash string  // SHA-256
}
```

#### M2.2 Template-Implementierung

Jede Subroutine als eigenes Template, zusammengesetzt durch den Generator. Reihenfolge nach architektur.md §4.1 (Generierungsreihenfolge).

Garantien des Generators (architektur.md §4.7):
- Niemals `beresp.ttl = 0s`
- Immer `std.ban()` statt `ban()`
- Immer explizites `return()` am Ende jeder Subroutine
- Immer `unset req.http.proxy` an erster Stelle in `vcl_recv`
- Immer Host-Normalisierung + `std.querysort()`
- Immer `beresp.http.x-url` + `x-host` in `vcl_backend_response`
- Immer interne Header in `vcl_deliver` entfernen

#### M2.3 Konfigurations-Matrix für Golden Files

| Szenario | Felder |
|----------|--------|
| Minimal | 1 Backend, kein Cluster, kein xkey, kein ESI |
| Standard | 2 Backends, Cluster aktiviert, Shard-Director |
| xkey | xkey.enabled=true, softPurge=true |
| ESI | esi.enabled=true, threadPoolStack=80KB |
| Soft-Purge | purge.soft=true (vcl_hit + vcl_miss) |
| PROXY-Protocol | proxyProtocol.enabled=true |
| Single-Replica | replicas=1 (kein Cluster-Block) |
| Full-Override | fullOverride gesetzt (nur Kommentar-Block) |
| Alle Features | Kombination aller aktivierbaren Features |

#### M2.4 Hash-Berechnung

SHA-256 über den generierten VCL-String. Identische Spec + identische Endpoints → identischer Hash. Test: gleicher Input, zwei Aufrufe, gleicher Hash.

### Tests

```go
func TestGenerator_GoldenFiles(t *testing.T) {
    cases := loadTestCases("testdata/")
    for _, tc := range cases {
        t.Run(tc.Name, func(t *testing.T) {
            result, err := generator.Generate(tc.Spec, tc.Peers, tc.Endpoints)
            require.NoError(t, err)
            golden.Assert(t, result.VCL, tc.GoldenFile)
        })
    }
}

func TestGenerator_Determinism(t *testing.T) { ... }
func TestGenerator_HashStability(t *testing.T) { ... }
func TestGenerator_NeverTTLZero(t *testing.T) { ... }      // Invarianten-Tests
func TestGenerator_AlwaysReturnStatement(t *testing.T) { ... }
```

**Coverage-Ziel:** >90% für generator-Package

### Dokumentation

- `docs/sources/explanation/vcl-generation.md`: Wie VCL generiert wird, Template-Struktur, Garantien
- `docs/sources/reference/vcl-templates.md`: Referenz aller generierten Subroutinen
- `docs/sources/how-to/custom-vcl-snippets.md`: Snippets schreiben und einhängen

### Akzeptanzkriterien

- [ ] Alle Golden-File-Tests grün
- [ ] Generator deterministisch (identischer Output bei identischem Input)
- [ ] Alle Qualitätsinvarianten (§4.7) durch eigene Tests abgedeckt
- [ ] Coverage >90%
- [ ] `varnishd -Cf` validiert generierten VCL ohne Fehler (Smoke-Test im CI mit echtem varnishd via Docker)

---

## M3: vinyl-agent

**Team-Größe:** 1 Person
**Voraussetzungen:** M0 (kein M1 nötig — agent ist unabhängig vom CRD)
**Output:** Produktionsreifer Agent als schlankes Go-Binary

### TDD-Ansatz

- HTTP-Handler werden mit Mock-Admin-Client getestet (Interface-basiert)
- Integrationstests laufen gegen echten `varnishd` via `testcontainers-go`

### Aufgaben

#### M3.1 Admin-Client-Interface

```go
type AdminClient interface {
    PushVCL(name, vcl string) error
    ValidateVCL(name, vcl string) error
    ActiveVCL() (string, error)
    Ban(expression string) error
}
```

Implementierung: `go-varnish-client` Library oder eigene Minimalimplementierung des Varnish Admin-Protokolls.

#### M3.2 HTTP-Handler

| Endpunkt | Methode | Auth | Beschreibung |
|----------|---------|------|-------------|
| `/vcl/push` | POST | Bearer | VCL pushen + aktivieren |
| `/vcl/validate` | POST | Bearer | VCL validieren (kein Push) |
| `/vcl/active` | GET | Bearer | Aktive VCL abfragen |
| `/ban` | POST | Bearer | Ban-Expression pushen |
| `/purge/xkey` | POST | Bearer | xkey-Purge via localhost:8080 |
| `/health` | GET | **kein Auth** | Readiness-Probe |
| `/metrics` | GET | **kein Auth** | Prometheus-Metriken |

#### M3.3 Bearer-Token-Middleware

- Token aus `/run/vinyl/agent-token` beim Start lesen
- Alle Auth-Pflicht-Endpunkte durch Middleware schützen
- `/health` und `/metrics` explizit ausgenommen

#### M3.4 xkey-Purge via localhost

Sendet internen HTTP-PURGE an `127.0.0.1:8080` mit `X-Xkey-Purge`-Header. Kein Admin-Protokoll für xkey (ist VCL-Funktion, nicht Admin-Befehl).

#### M3.5 Startup-Sequenz

1. Warten bis varnishd Admin-Port erreichbar (Polling mit Backoff, max. 60s)
2. Token einlesen
3. HTTP-Server starten
4. Readiness-Signal (Datei `/run/vinyl/agent-ready` oder direkt via /health)

### Tests

```
internal/agent/
├── handler_test.go          # Unit: alle Handler mit MockAdminClient
├── middleware_test.go        # Unit: Token-Auth, Fehlerfall (falsches Token, kein Token)
├── admin_client_test.go      # Integration: gegen echten varnishd (testcontainers-go)
└── xkey_purge_test.go        # Integration: xkey via localhost (testcontainers-go)
```

**Coverage-Ziel:** >90% für agent-Package (Integration-Tests gegen varnishd zählen)

### Dokumentation

- `docs/sources/reference/agent-api.md`: Vollständige API-Referenz aller Endpunkte (Request/Response-Format, Auth, Fehlercodes)
- `docs/sources/explanation/agent-design.md`: Warum Agent als Sidecar, Sicherheitsmodell
- `docs/sources/how-to/agent-auth.md`: Token-Rotation, Secret-Management

### Akzeptanzkriterien

- [ ] Alle Handler-Tests grün
- [ ] Token-Middleware lehnt ungültige/fehlende Tokens mit 401 ab
- [ ] `/health` ohne Auth erreichbar
- [ ] Integrations-Test gegen echten varnishd: VCL-Push + Validate + Active + Ban
- [ ] Binary kompiliert für `linux/amd64` und `linux/arm64`
- [ ] Coverage >90%

---

## M4: Operator Controller

**Team-Größe:** 2 Personen
**Voraussetzungen:** M1, M2, M3
**Output:** Vollständiger VinylCacheReconciler inkl. aller Kubernetes-Ressourcen

### TDD-Ansatz

- Unit-Tests mit gefakten Kubernetes-Clients (`sigs.k8s.io/controller-runtime/pkg/client/fake`)
- Integrationstests mit `envtest` (echte API-Server-Instanz, kein etcd nötig für Logik-Tests)
- Jeder Reconcile-Schritt einzeln testbar

### Aufgaben

#### M4.1 Reconcile-Loop-Struktur

```go
func (r *VinylCacheReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    // 1. VinylCache laden
    // 2. Deletion-Check + Finalizer
    // 3. StatefulSet reconcilen
    // 4. Services reconcilen (headless, traffic, invalidation)
    // 5. EndpointSlice reconcilen (cross-namespace)
    // 6. NetworkPolicy reconcilen
    // 7. Secret reconcilen (agent-token)
    // 8. Debouncing prüfen
    // 9. VCL generieren + Hash vergleichen
    // 10. VCL pushen (parallel an alle Pods)
    // 11. Status schreiben
}
```

Jeder Schritt als eigene Funktion — einzeln testbar.

#### M4.2 Ressourcen-Reconciler

Jede Kubernetes-Ressource als eigenes Sub-Package:

```
internal/controller/
├── reconciler.go          # Haupt-Reconciler
├── statefulset.go         # StatefulSet reconcilen
├── service.go             # headless + traffic + invalidation Service
├── endpointslice.go       # Cross-Namespace EndpointSlice
├── networkpolicy.go       # NetworkPolicies
├── secret.go              # Agent-Token Secret
├── vcl_push.go            # VCL-Push an Pods (parallel, Retry)
├── status.go              # Status + Conditions schreiben
└── finalizer.go           # Cleanup bei Deletion
```

#### M4.3 Watch-Konfiguration

```go
ctrl.NewControllerManagedBy(mgr).
    For(&v1alpha1.VinylCache{}).
    Owns(&appsv1.StatefulSet{}).
    Owns(&corev1.Service{}).
    Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(...)).
    Watches(&corev1.Endpoints{}, handler.EnqueueRequestsFromMapFunc(...)).
    WithEventFilter(predicate.GenerationChangedPredicate{}).
    Complete(r)
```

#### M4.4 VCL-Push-Logik

- Parallel an alle ready Pods (Worker-Pool)
- Retry mit exponential Backoff (aus `spec.retry`)
- Timeout pro Pod (konfigurierbar)
- Partial-Failure: Condition `VCLSynced=False` mit Reason + Message
- VCL-Hash pro Pod in `status.clusterPeers[].activeVCLHash`

#### M4.5 Debouncing

In-Memory `lastChangeTime` pro VinylCache-Namespace/Name. `Requeue(after=remainingDebounce)` wenn noch nicht abgelaufen.

#### M4.6 Periodic Reconcile (VCL-Drift-Erkennung)

`ctrl.Result{RequeueAfter: 5 * time.Minute}` am Ende jedes erfolgreichen Reconcile. Vergleicht aktive VCL-Hashes der Pods mit `status.activeVCL.hash`.

### Tests

```
internal/controller/
├── reconciler_test.go         # Integration (envtest): vollständiger Lifecycle
├── statefulset_test.go        # Unit: StatefulSet-Spec korrekt
├── endpointslice_test.go      # Unit: EndpointSlice korrekt
├── vcl_push_test.go           # Unit: Retry-Logik, Partial-Failure (Mock-Agent)
├── status_test.go             # Unit: Condition-Logik, Phase-Berechnung
└── finalizer_test.go          # Unit: Cleanup-Reihenfolge
```

**Test-Szenarien (envtest):**
- VinylCache erstellen → StatefulSet, Services, EndpointSlice, NetworkPolicy, Secret erscheinen
- VinylCache löschen → Finalizer bereinigt alle cross-namespace Ressourcen
- Pod nicht erreichbar → Retry, `VCLSynced=False`, Backoff
- VCL-Hash identisch → kein Push
- Spec-Änderung → Debounce → Push

**Coverage-Ziel:** >80% (controller-Package mit envtest ist aufwändig), >90% für Unit-testbare Sub-Packages

### Dokumentation

- `docs/sources/explanation/reconcile-loop.md`: Reconcile-Loop-Ablauf mit Flussdiagramm (Mermaid)
- `docs/sources/reference/operator-flags.md`: Alle `--flag`-Optionen des Operators
- `docs/sources/how-to/ha-setup.md`: Leader-Election, mehrere Replicas

### Akzeptanzkriterien

- [ ] Vollständiger Create/Update/Delete-Lifecycle in envtest-Tests grün
- [ ] Finalizer räumt cross-namespace Ressourcen korrekt auf
- [ ] VCL-Push-Retry mit Backoff verifiziert
- [ ] Status-Conditions korrekt gesetzt (Ready, VCLSynced, BackendsAvailable, Progressing)
- [ ] Debouncing verifiziert (kein sofortiger Push bei frequenten Events)
- [ ] Coverage controller-Package >80%, Sub-Packages >90%

---

## M5: Purge/BAN-Proxy

**Team-Größe:** 1 Person
**Voraussetzungen:** M1, M4 (für Pod-IP-Map Interface)
**Output:** Produktionsreifer Invalidierungs-Proxy

### TDD-Ansatz

- Broadcast-Logik mit Mock-Varnish-HTTP-Server getestet
- Response-Format (200/207/503) vollständig abgedeckt
- ACL-Logik unit-getestet

### Aufgaben

#### M5.1 Pod-IP-Map Interface

```go
type PodIPProvider interface {
    GetPodIPs(namespace, cacheName string) []string
}
```

Implementierung: Watch auf Pods via controller-runtime-Informer (unabhängig vom Leader-Controller).

#### M5.2 HTTP-Handler

| Endpunkt | Methode | Beschreibung |
|----------|---------|-------------|
| `PURGE /*` | PURGE | URL-basierter Purge, Broadcast |
| `BAN /` | BAN | BAN via HTTP-Methode (→ Admin-Protokoll) |
| `POST /ban` | POST | BAN via REST (JSON-Body) |
| `POST /purge/xkey` | POST | xkey-Invalidierung |

Host-Header-Routing: `<cache-name>-invalidation.<namespace>` → VinylCache lookup.

#### M5.3 Broadcast-Logik

```go
type BroadcastResult struct {
    Status    string  // "ok" | "partial" | "failed"
    Total     int
    Succeeded int
    Results   []PodResult
}

type PodResult struct {
    Pod    string
    Status int    // HTTP-Status oder 0 bei Fehler
    Error  string `json:",omitempty"`
}
```

HTTP-Response-Codes: 200 (alle ok), 207 (partial), 503 (alle fehlgeschlagen).

#### M5.4 Sicherheit

- Source-IP gegen `spec.invalidation.*.allowedSources` prüfen
- Host-Header-Lookup: unbekannter Host → 404
- BAN-Expression-Allowlist (nur `obj.http.*` LHS)
- Rate-Limiting (Token-Bucket, konfigurierbar)

### Tests

```
internal/proxy/
├── handler_test.go       # Unit: PURGE, BAN, xkey Handler
├── broadcast_test.go     # Unit: Broadcast-Logik, Response-Format (Mock-HTTP)
├── acl_test.go           # Unit: Source-IP-Check, Host-Header-Routing
├── ban_allowlist_test.go # Unit: Expression-Validierung
└── ratelimit_test.go     # Unit: Rate-Limiting
```

**Coverage-Ziel:** >90%

### Dokumentation

- `docs/sources/reference/invalidation-api.md`: Vollständige Protokoll-Referenz (PURGE, BAN, xkey, Response-Format)
- `docs/sources/how-to/configure-purge.md`: allowedSources, ACL, BAN-Allowlist konfigurieren
- `docs/sources/how-to/invalidation-clients.md`: Client-Implementierung, Retry bei 207

### Akzeptanzkriterien

- [ ] Broadcast an N Pods, M Fehler → korrekter 207-Body
- [ ] Broadcast alle fehlgeschlagen → 503
- [ ] Unbekannter Host → 404
- [ ] Source-IP außerhalb allowedSources → 403
- [ ] BAN-Expression mit nicht-erlaubtem LHS → 400
- [ ] Coverage >90%

---

## M6: Monitoring

**Team-Größe:** 0.5 Personen (parallel zu M4/M5)
**Voraussetzungen:** M1
**Output:** Prometheus-Metriken, PrometheusRule, ServiceMonitor

### Aufgaben

#### M6.1 Metriken-Registry

```go
// internal/monitoring/metrics.go
var (
    VCLPushTotal       *prometheus.CounterVec   // label: cache, namespace, result
    VCLPushDuration    prometheus.Histogram
    InvalidationTotal  *prometheus.CounterVec   // label: cache, namespace, type, result
    BroadcastTotal     *prometheus.CounterVec   // label: pod, result
    PartialFailureTotal *prometheus.CounterVec
    HitRatio           *prometheus.GaugeVec
    BackendHealth      *prometheus.GaugeVec
    VCLVersionsLoaded  *prometheus.GaugeVec
    // ... alle aus architektur.md §8.4
)
```

#### M6.2 PrometheusRule-Generator

Generiert `PrometheusRule`-Objekt (monitoring.coreos.com/v1) aus den 10 Alert-Definitionen (architektur.md §8.5). Opt-in via `spec.monitoring.prometheusRules.enabled`.

#### M6.3 ServiceMonitor-Generator

Generiert `ServiceMonitor` für den Operator-Pod. Opt-in via `spec.monitoring.serviceMonitor.enabled`.

### Tests

```
internal/monitoring/
├── metrics_test.go         # Unit: Metriken registriert, Labels korrekt
├── prometheusrule_test.go  # Unit: generierte PrometheusRule validieren
└── servicemonitor_test.go  # Unit: generierter ServiceMonitor validieren
```

**Coverage-Ziel:** >90%

### Dokumentation

- `docs/sources/reference/metrics.md`: Alle Metriken mit Labels, Typen, Beschreibungen
- `docs/sources/how-to/setup-monitoring.md`: Prometheus-Operator, PrometheusRule, Grafana-Dashboard

### Akzeptanzkriterien

- [ ] Alle definierten Metriken registriert und in `/metrics` sichtbar
- [ ] PrometheusRule enthält alle 10 Alerts aus architektur.md §8.5
- [ ] Coverage >90%

---

## M7: Helm Chart

**Team-Größe:** 1 Person
**Voraussetzungen:** M4, M5, M6
**Output:** Produktionsreifes Helm Chart für Operator-Deployment

### Aufgaben

#### M7.1 Chart-Struktur

```
charts/cloud-vinyl/
├── Chart.yaml
├── values.yaml
├── values.schema.json      # JSON Schema für values-Validierung
├── templates/
│   ├── deployment.yaml     # Operator-Deployment
│   ├── serviceaccount.yaml
│   ├── clusterrole.yaml
│   ├── clusterrolebinding.yaml
│   ├── service.yaml        # Metriken-Endpunkt
│   ├── webhook.yaml        # ValidatingWebhookConfiguration + MutatingWebhookConfiguration
│   ├── certificate.yaml    # cert-manager Certificate (opt-in)
│   └── crds/               # CRD-Manifeste (oder via --set installCRDs=true)
└── tests/
    └── helm-unittest/      # helm unittest
```

#### M7.2 Values-Schema

Alle konfigurierbaren Felder mit Typen, Defaults, Beschreibungen:
- `replicaCount`, `image.repository`, `image.tag`
- `resources`, `nodeSelector`, `tolerations`, `affinity`
- `leaderElection.enabled`
- `monitoring.enabled`, `monitoring.prometheusRules.enabled`
- `webhook.certManager.enabled` (cert-manager vs. manuelles TLS)
- `installCRDs`

### Tests

```
charts/cloud-vinyl/tests/
└── helm-unittest/
    ├── deployment_test.yaml
    ├── rbac_test.yaml
    └── webhook_test.yaml
```

**Coverage:** 100% der Templates durch helm unittest abgedeckt (Rendering ohne Fehler, Pflichtfelder vorhanden).

### Dokumentation

- `docs/sources/how-to/install.md`: Helm-Installation (mit cert-manager, ohne cert-manager)
- `docs/sources/how-to/upgrade.md`: Upgrade-Prozess, CRD-Migration
- `docs/sources/reference/helm-values.md`: Vollständige Values-Referenz (generiert aus values.schema.json)

### Akzeptanzkriterien

- [ ] `helm install` + `helm upgrade` in CI gegen kind-Cluster
- [ ] `helm unittest` 100% Template-Coverage
- [ ] `values.schema.json` validiert ungültige Values
- [ ] cert-manager-Integration getestet (self-signed Certificate für Webhook)

---

## M8: E2E Tests

**Team-Größe:** 1 Person
**Voraussetzungen:** M0–M7
**Output:** Vollständige E2E-Testsuite als Grundlage für CI-Release-Gate

### Tool: Chainsaw (Kyverno)

Deklarative E2E-Tests mit YAML — kein Go-Testcode für E2E nötig:

```yaml
# e2e/tests/basic-lifecycle/chainsaw-test.yaml
apiVersion: chainsaw.kyverno.io/v1alpha1
kind: Test
metadata:
  name: basic-lifecycle
spec:
  steps:
    - name: create-vinylcache
      try:
        - apply:
            file: vinylcache.yaml
    - name: wait-ready
      try:
        - assert:
            file: expected-ready.yaml
    - name: verify-vcl-push
      try:
        - script:
            content: kubectl exec ... varnishadm vcl.show active | grep "cloud-vinyl"
```

### Test-Szenarien

| Szenario | Beschreibung |
|----------|-------------|
| `basic-lifecycle` | Create → Ready, Update Spec → VCL-Push, Delete → Cleanup |
| `cluster-routing` | 3 Replicas, Shard-Director, URL-Routing verifizieren |
| `purge-broadcast` | PURGE an alle Pods, 207 bei teilweisem Ausfall |
| `xkey-invalidation` | xkey-Purge via Agent-HTTP |
| `vcl-validation` | Syntaxfehler in Snippet → Webhook lehnt ab |
| `ha-operator` | 2 Operator-Replicas, Leader-Election, Failover |
| `scaling` | Scale 1→3→1, VCL-Updates, Debouncing |
| `drift-detection` | Manueller VCL-Discard auf Pod → Operator erkennt und repariert |

### Dokumentation

- `docs/sources/tutorials/quickstart.md`: Erstes VinylCache-Objekt in 5 Minuten (kind + Helm)
- `docs/sources/tutorials/cluster-setup.md`: Multi-Replica-Setup mit Traefik
- `docs/sources/how-to/debug-vcl.md`: VCL-Probleme diagnostizieren

### Akzeptanzkriterien

- [ ] Alle 8 Szenarien in CI grün (kind-Cluster)
- [ ] `basic-lifecycle` < 2 Minuten Laufzeit
- [ ] Quickstart-Tutorial in Docs: Nutzer kann in < 10 Minuten einem funktionierenden VinylCache haben

---

## Dokumentations-Gesamtplan

### Tutorials (lernorientiert)

| Datei | Inhalt |
|-------|--------|
| `tutorials/quickstart.md` | VinylCache in 5 Minuten (kind + Helm) |
| `tutorials/cluster-setup.md` | Multi-Replica mit Traefik + PROXY-Protocol |
| `tutorials/migrate-from-manual.md` | Migration von manuell-verwaltetem Varnish |

### How-To Guides (aufgabenorientiert)

| Datei | Inhalt |
|-------|--------|
| `how-to/install.md` | Helm-Installation |
| `how-to/upgrade.md` | Operator + Varnish-Image upgraden |
| `how-to/configure-purge.md` | PURGE/BAN/xkey konfigurieren |
| `how-to/setup-monitoring.md` | Prometheus + Grafana |
| `how-to/custom-vcl-snippets.md` | VCL-Snippets schreiben |
| `how-to/ha-setup.md` | Operator-HA, Leader-Election |
| `how-to/debug-vcl.md` | VCL debuggen |
| `how-to/agent-auth.md` | Token-Rotation |
| `how-to/invalidation-clients.md` | Client-Implementierung für Purge-API |

### Reference (informationsorientiert)

| Datei | Inhalt |
|-------|--------|
| `reference/crd-fields.md` | Vollständige CRD-Feldreferenz |
| `reference/agent-api.md` | Agent-HTTP-API |
| `reference/invalidation-api.md` | Purge/BAN-Protokoll-Referenz |
| `reference/metrics.md` | Alle Prometheus-Metriken |
| `reference/operator-flags.md` | Operator-Startflags |
| `reference/helm-values.md` | Helm-Values-Referenz |
| `reference/vcl-templates.md` | Generierte VCL-Subroutinen |

### Explanation (verständnisorientiert)

| Datei | Inhalt |
|-------|--------|
| `explanation/architecture.md` | Systemübersicht (aus architektur.md destilliert) |
| `explanation/reconcile-loop.md` | Reconcile-Loop-Ablauf |
| `explanation/vcl-generation.md` | Wie VCL generiert wird |
| `explanation/clustering.md` | Shard-Director, Self-Routing, Warmup/Rampup |
| `explanation/invalidation.md` | PURGE vs. BAN vs. xkey, Grace-Interaktion |
| `explanation/security.md` | Trust-Boundaries, RBAC, NetworkPolicies |
| `explanation/api-design.md` | CRD-Design-Entscheidungen |

---

## Milestones

| Milestone | Enthält | Ziel |
|-----------|---------|------|
| **v0.1.0-alpha** | M0, M1, M2, M3 | CRD + Generator + Agent: Grundlage für lokale Entwicklung |
| **v0.2.0-alpha** | + M4 | Operator funktional: VinylCache kann deployed werden |
| **v0.3.0-alpha** | + M5, M6 | Purge/BAN + Monitoring: produktionsrelevante Features |
| **v0.4.0-alpha** | + M7 | Helm Chart: installierbar via Helm |
| **v0.5.0-alpha** | + M8 | E2E-Tests: Release-Gate vorhanden |
| **v0.1.0** | Docs vollständig, Coverage-Ziele erreicht | Erste öffentliche Alpha |

---

## Coverage-Tracking

| Package | Muss | Gewünscht |
|---------|------|----------|
| `internal/webhook` | 80% | 90% |
| `internal/generator` | 90% | 95% |
| `internal/agent` | 90% | 95% |
| `internal/controller` | 80% | 90% |
| `internal/proxy` | 90% | 95% |
| `internal/monitoring` | 80% | 90% |
| **Gesamt** | **>80%** | **>90%** |

Coverage wird in GitHub Actions bei jedem PR gegen `main` geprüft. PR-Merge geblockt bei Unterschreiten des Muss-Werts.

---

*Dieser Plan wird mit dem Fortschritt der Implementierung aktualisiert. Grundlage: architektur.md (alle Entscheidungen getroffen, Stand 2026-03-08).*
