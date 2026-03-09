# M2: VCL-Generator

**Status:** Bereit zur Implementierung
**Voraussetzungen:** M0 (Project Setup), M1 (CRD & Go Types)
**Parallelisierbar mit:** M3 (vinyl-agent), M5 (Purge/BAN-Proxy)
**Geschätzte Team-Größe:** 1–2 Personen

---

## Kontext

Der VCL-Generator ist das Herzstück von cloud-vinyl. Er nimmt eine `VinylCacheSpec` und die aktuellen Kubernetes-Endpoint-Daten entgegen und erzeugt deterministisch die Varnish-Konfigurationssprache (VCL), die auf alle Varnish-Pods gepusht wird.

**Warum deterministisch?** Der Operator vergleicht den SHA-256-Hash der generierten VCL mit dem zuletzt gepushten Hash. Identischer Hash → kein Push nötig. Nichtdeterminismus (z.B. durch Map-Iteration) würde bei jedem Reconcile einen Push auslösen.

**Relevante Architektur-Abschnitte:**
- §4.1 — Generierungsreihenfolge und kanonische VCL-Struktur
- §4.2 — Beispiel einer vollständig generierten VCL (Referenz für Golden Files)
- §4.3 — Snippet-Hook-System (wie User-Snippets eingebettet werden)
- §4.7 — Qualitätsinvarianten die der Generator MUSS garantieren (Bug-Prevention-Checkliste)
- §5.2 — Shard-Director: Warmup/Rampup, Self-Routing-Pattern
- §5.5 — X-Vinyl-Shard-Header-Pattern (Cluster-Routing-VCL)

Der Generator wird **ausschließlich vom Operator-Controller** aufgerufen (M4). Er hat keinen Kubernetes-Zugriff — er bekommt alle nötigen Daten als Parameter.

---

## Ziel

Ein Go-Package `internal/generator` das:
1. VCL aus `VinylCacheSpec` + Peer-Liste + Endpoint-Map generiert
2. Deterministisch ist (identischer Input → identischer Output)
3. Alle Qualitätsinvarianten aus §4.7 garantiert
4. Vollständig durch Golden-File-Tests abgedeckt ist
5. Den SHA-256-Hash der generierten VCL berechnet

---

## Package-Struktur

```
internal/generator/
├── generator.go          # Haupt-Logik: Generate()-Funktion
├── generator_test.go     # Golden-File-Tests + Invarianten-Tests
├── hash.go               # SHA-256-Berechnung
├── hash_test.go
├── templates/            # text/template Templates, eine Datei pro VCL-Subroutine
│   ├── header.vcl.tmpl           # vcl 4.1; import-Statements; ACL-Definitionen
│   ├── vcl_init.vcl.tmpl         # Director-Initialisierung
│   ├── vcl_recv.vcl.tmpl         # Security, Normalisierung, Routing, PURGE, Bypass
│   ├── vcl_hash.vcl.tmpl
│   ├── vcl_hit.vcl.tmpl          # Soft-Purge-Handler (wenn aktiviert)
│   ├── vcl_miss.vcl.tmpl         # Soft-Purge-Handler (wenn aktiviert)
│   ├── vcl_pass.vcl.tmpl
│   ├── vcl_backend_fetch.vcl.tmpl
│   ├── vcl_backend_response.vcl.tmpl  # TTL-Logik, x-url/x-host-Copy
│   ├── vcl_deliver.vcl.tmpl           # Interne Header strippen, Debug-Header
│   ├── vcl_pipe.vcl.tmpl
│   ├── vcl_purge.vcl.tmpl
│   ├── vcl_synth.vcl.tmpl        # JSON-Fehlerresponse
│   └── vcl_fini.vcl.tmpl
└── testdata/             # Golden Files (eingecheckt, nie manuell bearbeiten)
    ├── minimal.vcl
    ├── standard-cluster.vcl
    ├── xkey-enabled.vcl
    ├── esi-enabled.vcl
    ├── soft-purge.vcl
    ├── proxy-protocol.vcl
    ├── single-replica.vcl
    ├── full-override.vcl
    └── all-features.vcl
```

---

## Schlüssel-Interfaces

Der Agent muss diese Typen kennen bevor er mit dem Schreiben beginnt. Sie entstehen aus M1 (CRD-Typen) und dem internen Bedarf des Generators.

```go
// internal/generator/generator.go

// Input: alle Informationen die der Generator zur VCL-Erzeugung braucht.
// Kommt vom Operator-Controller (M4), nie direkt von Kubernetes.
type Input struct {
    Spec      *v1alpha1.VinylCacheSpec
    Peers     []Peer      // Aktuelle Varnish-Pod-IPs (aus StatefulSet-Watch)
    Endpoints map[string][]Endpoint // Backend-Name → Endpoint-IPs (aus Service-Watch)
}

type Peer struct {
    PodName string
    IP      string
    Port    int
}

type Endpoint struct {
    IP   string
    Port int
}

// Output: generierter VCL-String + Hash.
type Result struct {
    VCL  string
    Hash string // SHA-256 hex, z.B. "sha256:abc123..."
}

// Generator ist das zentrale Interface — ermöglicht Mocking in Controller-Tests (M4).
type Generator interface {
    Generate(input Input) (*Result, error)
}
```

**Warum ein Interface?** Der Controller (M4) testet seine eigene Logik mit einem Mock-Generator — er muss nicht die echte VCL-Generierung kennen. Das Interface ist die Grenze.

---

## TDD-Workflow

### Schritt 1: Golden-File-Infrastruktur aufbauen (vor dem ersten echten Test)

Golden-File-Tests funktionieren so: Der Test generiert VCL und vergleicht sie mit einer eingecheckten Referenzdatei. Beim ersten Mal schreibt man die Golden Files durch Aufruf mit einem Update-Flag.

```go
// internal/generator/generator_test.go

var update = flag.Bool("update", false, "update golden files")

func TestGenerate_GoldenFiles(t *testing.T) {
    cases := []struct {
        name  string
        input Input
    }{
        {name: "minimal", input: minimalInput()},
        {name: "standard-cluster", input: standardClusterInput()},
        // ... weitere Cases
    }

    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            g := NewGenerator()
            result, err := g.Generate(tc.input)
            require.NoError(t, err)

            goldenFile := filepath.Join("testdata", tc.name+".vcl")
            if *update {
                require.NoError(t, os.WriteFile(goldenFile, []byte(result.VCL), 0644))
            }
            expected, err := os.ReadFile(goldenFile)
            require.NoError(t, err)
            assert.Equal(t, string(expected), result.VCL)
        })
    }
}
```

**Workflow:**
1. Test schreiben (schlägt fehl — Golden File existiert nicht)
2. Minimal-Implementierung schreiben bis Test mit `--update` durchläuft und Golden File erzeugt
3. Golden File reviewen (ist das wirklich gültige VCL?)
4. `varnishd -Cf` gegen Golden File laufen lassen (Smoke-Test)
5. Ab jetzt schlägt der Test bei jeder unbeabsichtigten Änderung fehl

### Schritt 2: Invarianten-Tests (vor der Implementierung schreiben)

Diese Tests prüfen Qualitätsgarantien aus §4.7 — unabhängig vom konkreten Output:

```go
func TestGenerate_NeverTTLZero(t *testing.T) {
    // Für alle möglichen Inputs: beresp.ttl = 0s darf nie im Output stehen
    result, _ := NewGenerator().Generate(anyInput())
    assert.NotContains(t, result.VCL, "beresp.ttl = 0s")
}

func TestGenerate_AlwaysStdBan(t *testing.T) {
    // ban() (deprecated) darf nie erscheinen, nur std.ban()
    result, _ := NewGenerator().Generate(anyInput())
    assert.NotRegexp(t, `\bban\(`, result.VCL) // nicht "std.ban(", sondern nacktes "ban("
}

func TestGenerate_AlwaysStripsProxy(t *testing.T) {
    result, _ := NewGenerator().Generate(anyInput())
    assert.Contains(t, result.VCL, "unset req.http.proxy")
}

func TestGenerate_Determinism(t *testing.T) {
    g := NewGenerator()
    input := standardClusterInput()
    r1, _ := g.Generate(input)
    r2, _ := g.Generate(input)
    assert.Equal(t, r1.VCL, r2.VCL)
    assert.Equal(t, r1.Hash, r2.Hash)
}

// ... weitere Invarianten aus §4.7
```

### Schritt 3: Feature-spezifische Tests

Für jedes aktivierbare Feature einen Test:

```go
func TestGenerate_XkeyEnabled_ContainsSoftpurge(t *testing.T) { ... }
func TestGenerate_ESIEnabled_SetsThreadPoolStack(t *testing.T) { ... }
func TestGenerate_SingleReplica_NoClusterBlock(t *testing.T) { ... }
func TestGenerate_SoftPurge_InHitAndMiss(t *testing.T) { ... }
func TestGenerate_ProxyProtocol_AddsListener(t *testing.T) { ... }
```

---

## Template-Struktur

Templates bekommen einen `TemplateData`-Struct. **Keine Map[string]any** — der Compiler prüft Typen.

```go
// internal/generator/generator.go

type TemplateData struct {
    Spec          *v1alpha1.VinylCacheSpec
    Peers         []Peer
    Endpoints     map[string][]Endpoint
    Features      FeatureFlags    // Abgeleitete Flags für Template-Conditions
    OperatorPodIP string          // Wird automatisch in Purge-ACL eingetragen
}

// FeatureFlags: vom Generator aus Spec abgeleitet, vereinfacht Template-Logik
type FeatureFlags struct {
    ClusterEnabled      bool
    XkeyEnabled         bool
    SoftPurgeEnabled    bool
    ESIEnabled          bool
    ProxyProtocolEnabled bool
    SingleReplica       bool  // replicas == 1 || !cluster.enabled → kein Cluster-Block
    PurgeEnabled        bool
    BanEnabled          bool
}
```

Beispiel-Template-Snippet für `vcl_recv.vcl.tmpl`:

```vcl
{{- if .Features.ClusterEnabled}}
    // Cluster-Routing (§5.5)
    if (req.http.X-Vinyl-Shard && !(client.ip ~ vinyl_cluster_peers)) {
        unset req.http.X-Vinyl-Shard;
    }
    if (!req.http.X-Vinyl-Shard) {
        set req.http.X-Vinyl-Shard = "1";
        set req.backend_hint = vinyl_cluster.backend(by=URL);
        return(pass);
    }
    unset req.http.X-Vinyl-Shard;
{{- end}}
```

---

## Konfigurations-Matrix für Golden Files

Jeder Eintrag → eine Golden-File-Datei in `testdata/`:

| Name | Aktivierte Features | Zweck |
|------|-------------------|-------|
| `minimal` | 1 Backend, kein Cluster, kein xkey, kein ESI, kein Soft-Purge | Baseline |
| `standard-cluster` | 2 Backends, Cluster, Shard-Director, 3 Peers | Normalfall |
| `xkey-enabled` | + xkey.enabled=true, softPurge=true | xkey-Handler in vcl_recv |
| `esi-enabled` | + esi.enabled=true | thread_pool_stack in varnishParameters |
| `soft-purge` | + purge.soft=true | vcl_hit + vcl_miss mit purge.soft() |
| `proxy-protocol` | + proxyProtocol.enabled=true | zweiter Listener |
| `single-replica` | replicas=1 | kein Cluster-Block in VCL |
| `full-override` | fullOverride gesetzt | nur Kommentar-Block |
| `all-features` | alle aktivierbaren Features | Regression-Gesamttest |

---

## Varnishd-Smoke-Test im CI

Der CI-Job führt nach den Unit-Tests einen Smoke-Test durch:

```yaml
# .github/workflows/ci.yml
vcl-smoke-test:
  runs-on: ubuntu-latest
  container: varnish:7.6
  steps:
    - run: |
        for f in internal/generator/testdata/*.vcl; do
          varnishd -C -f "$f" || exit 1
        done
```

`varnishd -C` kompiliert die VCL ohne zu starten — fängt Syntaxfehler in Golden Files ab.

---

## Qualitätsinvarianten (aus §4.7)

Der Generator muss diese Garantien durch Tests absichern. Die vollständige Liste steht in architektur.md §4.7. Die kritischsten:

| Invariante | Test-Methode |
|-----------|-------------|
| Nie `beresp.ttl = 0s` | `assert.NotContains` auf Output |
| Immer `std.ban()` statt `ban()` | Regex-Prüfung auf Output |
| Immer `return()` am Ende jeder Sub | Parser-artige Prüfung oder Golden File |
| Immer `unset req.http.proxy` als erstes | `assert.Contains` |
| Host-Normalisierung in vcl_recv | `assert.Contains("std.tolower")` |
| `std.querysort` in vcl_recv | `assert.Contains` |
| `x-url`/`x-host` in vcl_backend_response | `assert.Contains` |
| Interne Header in vcl_deliver gestripped | `assert.Contains("unset resp.http.x-url")` |
| Nie `ban()` in ban-Expressions (nur `std.ban()`) | Regex |

---

## Dokumentations-Deliverables

Diese Docs-Seiten entstehen parallel zur Implementierung (nicht am Ende):

| Datei | Diataxis | Inhalt | Wann |
|-------|----------|--------|------|
| `docs/sources/explanation/vcl-generation.md` | Explanation | Wie der Generator funktioniert, Template-Struktur, Garantien | Während Implementierung |
| `docs/sources/reference/vcl-templates.md` | Reference | Alle generierten Subroutinen mit Erklärung der erzeugten Konstrukte | Nach Golden Files |
| `docs/sources/how-to/custom-vcl-snippets.md` | How-To | Snippets schreiben, Hook-Positionen, Größenlimit, Sicherheitshinweis | Nach M1+M2 |

---

## Akzeptanzkriterien

Ein Pull Request für M2 ist merge-bereit wenn:

- [ ] Alle 9 Golden-File-Tests grün
- [ ] `go test -run TestGenerate_GoldenFiles -update` erzeugt valide Golden Files
- [ ] Alle Invarianten-Tests grün (§4.7-Checkliste vollständig abgedeckt)
- [ ] `varnishd -C` validiert alle Golden Files ohne Fehler (CI-Smoke-Test)
- [ ] Determinismus-Test grün
- [ ] `go test -race ./internal/generator/...` ohne Data-Race-Fehler
- [ ] Coverage `internal/generator` ≥ 90%
- [ ] `Generator`-Interface ist gemockt in einem Beispiel-Test (für M4-Vorbereitung)
- [ ] `docs/sources/explanation/vcl-generation.md` existiert und baut ohne Sphinx-Warnungen

---

## Offene Fragen für den Implementierenden

Bevor mit der Implementierung begonnen wird, folgende Punkte klären:

1. **varnish-modules verfügbar?** xkey und soft-purge kommen aus dem `varnish-modules`-Paket — das Varnish-Image muss `vmod_xkey` und `vmod_purge` enthalten. Im Dockerfile prüfen/sicherstellen.
2. **`go-varnish-client` oder eigene Admin-Implementierung?** Für den Generator selbst irrelevant, aber relevant für M3/M4. Nicht blockierend für M2.
3. **`golangci-lint`-Konfiguration:** Linter-Regeln aus M0 müssen für Template-Code passen (lange Zeilen in Templates ggf. ignorieren).
