# M1: CRD & Go Types

**Status:** Bereit zur Implementierung
**Voraussetzungen:** M0
**Parallelisierbar mit:** M3 (vinyl-agent braucht keine CRD-Typen)
**Geschätzte Team-Größe:** 1–2 Personen

---

## Kontext

M1 definiert die öffentliche API von cloud-vinyl — das `VinylCache`-CRD. Alles andere (Generator, Controller, Proxy) baut auf diesen Go-Typen auf. Fehler hier (falsche Feldnamen, fehlende Validierung, falsche Defaults) sind teuer zu korrigieren sobald die API in Benutzung ist.

**Relevante Architektur-Abschnitte:**
- §2.1 — kubebuilder-Marker (vollständige Liste der `+kubebuilder:`-Annotationen)
- §2.2 — Vollständige CRD-Spec und Status-YAML (kanonische Referenz für alle Feldnamen, Typen, Defaults)
- §2.3 — CRD-Felder-Übersicht (tabellarische Übersicht Pflicht/Optional)
- §9.3 — Secret-Management-Entscheidung (beeinflusst Secret-Typen im Status)
- §10.6 — Admission Webhook und CEL-Validierungsregeln (vollständige Liste)
- §11.4 — Deprecation-Strategie (beeinflusst wie Felder annotiert werden)

---

## Ziel

Nach M1 existieren:
- Vollständige Go-Typen für `VinylCacheSpec` und `VinylCacheStatus`
- Generiertes CRD-YAML (`make generate`)
- Validating + Mutating Webhook
- CEL-Validierungsregeln im CRD-Schema
- Unit- und Integrationstests für Webhook-Logik

---

## Package-Struktur

```
api/
└── v1alpha1/
    ├── vinylcache_types.go         # Spec, Status, alle Sub-Structs
    ├── vinylcache_webhook.go       # Validating + Mutating Webhook-Logik
    ├── groupversion_info.go        # API-Gruppe, SchemeBuilder (kubebuilder-generiert)
    └── zz_generated.deepcopy.go   # generiert via controller-gen, nie manuell bearbeiten

internal/webhook/
    ├── vinylcache_validator.go     # Validierungs-Business-Logik (von webhook.go aufgerufen)
    ├── vinylcache_validator_test.go # Unit-Tests: Fehlerfall-Matrix
    ├── vinylcache_defaulter.go     # Defaulting-Logik
    └── vinylcache_defaulter_test.go
```

**Warum Business-Logik in `internal/webhook/`?** Die Datei `api/v1alpha1/vinylcache_webhook.go` ist kubebuilder-Scaffolding — sie ruft nur durch. Die eigentliche Logik liegt in `internal/webhook/` und ist damit einfacher zu testen (kein Kubernetes-Client-Overhead).

---

## Schlüssel-Typen (Sketch)

Die vollständigen Feldnamen und -typen stehen in **architektur.md §2.2** — das ist die kanonische Referenz. Hier nur die Struktur-Übersicht:

```go
// api/v1alpha1/vinylcache_types.go

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:subresource:scale:specpath=.spec.replicas,statuspath=.status.replicas,selectorpath=.status.selector
// +kubebuilder:resource:shortName=vc,categories=vinyl
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Replicas",type="string",JSONPath=".status.readyReplicas"
// +kubebuilder:printcolumn:name="VCL",type="string",JSONPath=".status.conditions[?(@.type=='VCLSynced')].status"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase",priority=1
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type VinylCache struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`
    Spec   VinylCacheSpec   `json:"spec,omitempty"`
    Status VinylCacheStatus `json:"status,omitempty"`
}

type VinylCacheSpec struct {
    Replicas      int32              `json:"replicas"`
    Image         string             `json:"image"`
    Backends      []BackendSpec      `json:"backends"`
    Director      DirectorSpec       `json:"director,omitempty"`
    Cluster       ClusterSpec        `json:"cluster,omitempty"`
    VarnishParams map[string]string  `json:"varnishParameters,omitempty"`
    Storage       []StorageSpec      `json:"storage,omitempty"`
    VCL           VCLSpec            `json:"vcl,omitempty"`
    Invalidation  InvalidationSpec   `json:"invalidation,omitempty"`
    ProxyProtocol ProxyProtocolSpec  `json:"proxyProtocol,omitempty"`
    Service       ServiceSpec        `json:"service,omitempty"`
    Debounce      DebounceSpec       `json:"debounce,omitempty"`
    Retry         RetrySpec          `json:"retry,omitempty"`
    Pod           PodSpec            `json:"pod,omitempty"`
    Monitoring    MonitoringSpec     `json:"monitoring,omitempty"`
    Resources     corev1.ResourceRequirements `json:"resources,omitempty"`
}

type VinylCacheStatus struct {
    Phase            string             `json:"phase,omitempty"`    // Pending|Ready|Degraded|Error
    Message          string             `json:"message,omitempty"`
    ActiveVCL        *ActiveVCLStatus   `json:"activeVCL,omitempty"`
    Replicas         int32              `json:"replicas,omitempty"`
    ReadyReplicas    int32              `json:"readyReplicas,omitempty"`
    UpdatedReplicas  int32              `json:"updatedReplicas,omitempty"`
    AvailableReplicas int32             `json:"availableReplicas,omitempty"`
    Selector         string             `json:"selector,omitempty"`
    Backends         []BackendStatus    `json:"backends,omitempty"`
    ClusterPeers     []ClusterPeerStatus `json:"clusterPeers,omitempty"`
    ReadyPeers       int32              `json:"readyPeers,omitempty"`
    TotalPeers       int32              `json:"totalPeers,omitempty"`
    Conditions       []metav1.Condition `json:"conditions,omitempty"`
}

// Conditions: Ready, VCLSynced, BackendsAvailable, Progressing, VCLConsistent
// Alle mit observedGeneration — siehe architektur.md §2.2
```

**Wichtige Typ-Entscheidungen:**
- Alle Duration-Felder: `metav1.Duration` (nicht `time.Duration`, nicht `string`)
- Alle Storage-Größen: `resource.Quantity` (nicht `string`)
- Status-Conditions: `[]metav1.Condition` (nicht custom structs)
- `phase` ist ein **abgeleitetes Feld** (berechnet aus Conditions, nicht unabhängig gesetzt) — siehe §2.2

---

## TDD-Workflow

### Schritt 1: Webhook-Fehlerfall-Matrix zuerst

Bevor die Typen existieren, die Testmatrix aufschreiben:

```go
// internal/webhook/vinylcache_validator_test.go

func TestValidate_VarnishParameters_Blocklist(t *testing.T) {
    cases := []struct {
        param    string
        wantErr  bool
    }{
        {"vcc_allow_inline_c", true},   // RCE-Risiko
        {"cc_command", true},           // beliebiger Compiler-Aufruf
        {"thread_pool_min", false},     // erlaubt
        {"ban_lurker_sleep", false},    // erlaubt
    }
    // ...
}

func TestValidate_StorageType_Blocklist(t *testing.T) {
    // persistent, umem, default → verboten (§10.6)
    // malloc, file → erlaubt
}

func TestValidate_CrossNamespaceBackend_Rejected(t *testing.T) {
    // spec.backends[].serviceRef.namespace != "" → Fehler (§9.4)
}

func TestValidate_BackendNames_VCLConformant(t *testing.T) {
    // "my-backend" → Fehler (Bindestrich nicht erlaubt in VCL-Identifiern)
    // "my_backend" → ok
    // "123backend" → Fehler (muss mit Buchstabe beginnen)
}

func TestValidate_AllowedSources_CIDRSyntax(t *testing.T) {
    // "10.0.0.1/32" → ok
    // "10.0.0.256/32" → Fehler
    // "not-an-ip" → Fehler
}
```

### Schritt 2: Defaulting-Tests

```go
// internal/webhook/vinylcache_defaulter_test.go

func TestDefault_SoftPurge_DefaultsToTrue(t *testing.T) { ... }
func TestDefault_ShardWarmup_DefaultsTo0_1(t *testing.T) { ... }
func TestDefault_ShardRampup_DefaultsTo30s(t *testing.T) { ... }
func TestDefault_PodManagementPolicy_DefaultsToParallel(t *testing.T) { ... }
func TestDefault_XkeySoftPurge_DefaultsToTrue(t *testing.T) { ... }
// Vollständige Default-Liste: architektur.md §2.2 (Felder mit Default-Angaben)
```

### Schritt 3: CEL-Integrationstests (envtest)

```go
// api/v1alpha1/vinylcache_cel_test.go
// Braucht envtest — läuft nur mit Build-Tag "integration"

//go:build integration

func TestCEL_EmptyBackends_Rejected(t *testing.T) {
    // VinylCache ohne backends → Webhook-freie CEL-Ablehnung
}

func TestCEL_SnippetSizeLimit(t *testing.T) {
    // vclRecv snippet > 64KB → Ablehnung
}
```

---

## CEL-Validierungsregeln

Vollständige Liste in **architektur.md §10.6**. Im CRD-Schema als `x-kubernetes-validations`:

```go
// +kubebuilder:validation:XValidation:rule="size(self.backends) >= 1",message="at least one backend required"
// +kubebuilder:validation:XValidation:rule="self.backends.all(b, b.name.matches('^[a-zA-Z][a-zA-Z0-9_]*$'))",message="backend names must be valid VCL identifiers"
// (weitere Regeln: architektur.md §10.6)
```

## Webhook-Blocklists

Die vollständigen Blocklists stehen in **architektur.md §10.6**:
- `spec.varnishParameters` verbotene Schlüssel
- `spec.storage[].type` verbotene Werte (`persistent`, `umem`, `default`)
- `spec.backends[].serviceRef.namespace` muss leer sein

---

## Dokumentations-Deliverables

| Datei | Diataxis | Inhalt | Wann |
|-------|----------|--------|------|
| `docs/sources/reference/crd-fields.md` | Reference | Vollständige Feldreferenz, generiert aus Go-Kommentaren | Nach Typen |
| `docs/sources/explanation/api-design.md` | Explanation | Warum die API so strukturiert ist (aus §2, §9.3, §11 destilliert) | Parallel |
| `docs/sources/reference/operator-flags.md` | Reference | Stub, wird in M4 ausgebaut | Stub |

---

## Akzeptanzkriterien

- [ ] `make generate` erzeugt valides CRD-YAML ohne Fehler
- [ ] Alle Webhook-Validierungstests grün (vollständige Fehlerfall-Matrix)
- [ ] Alle Defaulting-Tests grün
- [ ] CEL-Integrationstests grün (envtest)
- [ ] Webhook lehnt alle verbotenen varnishParameters ab
- [ ] Webhook lehnt cross-namespace `serviceRef.namespace` ab
- [ ] `kubectl apply` gegen einen laufenden Cluster akzeptiert ein valides VinylCache-Objekt
- [ ] Coverage `internal/webhook` ≥ 90%
- [ ] CRD-Feldreferenz in Docs vollständig und baut ohne Sphinx-Warnungen

---

## Offene Fragen

1. **Kubernetes-Mindestversion:** CEL-Validierung erfordert Kubernetes 1.25+. Im README und Docs kommunizieren.
2. **controller-gen-Version:** Sicherstellen dass controller-gen die verwendeten CEL-Marker kennt (`x-kubernetes-validations`). Ab controller-gen v0.14 stabil.
3. **`metav1.Duration` vs. `string`:** kubebuilder serialisiert `metav1.Duration` als String (`"30s"`) — das entspricht Go-Konventionen. Sicherstellen dass die CRD-Doku das klar erklärt.
