# M3: vinyl-agent

**Status:** Bereit zur Implementierung
**Voraussetzungen:** M0
**Parallelisierbar mit:** M1 (CRD-Typen), M2 (VCL-Generator)
**Geschätzte Team-Größe:** 1 Person

---

## Kontext

Der vinyl-agent ist ein schlankes Go-Binary (~300–500 LOC) das als Sidecar in jedem Varnish-Pod läuft. Er ist bewusst dünn gehalten: **keine Business-Logik, kein Kubernetes-Watch, keine Entscheidungslogik**. Er ist ausschließlich eine sichere Bridge zwischen dem Operator (außerhalb des Pods) und dem Varnish Admin-Port (nur localhost:6082 sichtbar).

Der Agent ist das einzige Modul das **keinen Kubernetes-Kontext braucht** und unabhängig von M1 (CRD-Typen) implementiert werden kann. Er kennt kein `VinylCache`-Objekt.

**Relevante Architektur-Abschnitte:**
- §3.3 — Sidecar-Agent: Aufgabe, Startup-Sequenz, Authentifizierung, vollständige API-Referenz
- §3.4 — VCL-GC: Warum `vcl.discard` verzögert aufgerufen werden muss (Race-Condition mit Worker-Threads)
- §6.4 — xkey-Purge via localhost: warum xkey über HTTP statt Admin-Port geht (VCL-Funktion, nicht Admin-Befehl)
- §8.3 — Container-Images: Agent-Image muss klein sein (distroless oder scratch-basiert)
- §9.3 — Secret-Management: Wie das Bearer-Token in den Agent gelangt (Secret-Volume-Mount)

---

## Ziel

Nach M3 existiert:
- Ein produktionsreifes Go-Binary `cmd/agent/`
- Alle 7 HTTP-Endpunkte implementiert und getestet
- Integrationstests gegen echten `varnishd` via testcontainers-go
- Docker-Image buildbar

---

## Package-Struktur

```
cmd/agent/
└── main.go                  # HTTP-Server starten, Token einlesen, Startup-Sequenz

internal/agent/
├── server.go                # HTTP-Server-Setup, Routing
├── server_test.go
├── handler.go               # Alle HTTP-Handler
├── handler_test.go          # Unit-Tests mit MockAdminClient
├── middleware.go            # Bearer-Token-Auth-Middleware
├── middleware_test.go
├── admin.go                 # AdminClient-Interface + Implementierung
├── admin_test.go            # Integration (testcontainers-go: echter varnishd)
├── xkey.go                  # xkey-Purge via localhost:8080
└── xkey_test.go             # Integration (testcontainers-go)
```

---

## Schlüssel-Interfaces

```go
// internal/agent/admin.go

// AdminClient ist das Interface zur Varnish Admin-Protokoll-Kommunikation.
// Hinter einem Interface damit Handler-Tests keinen echten varnishd brauchen.
type AdminClient interface {
    // PushVCL lädt und aktiviert eine VCL. Name muss eindeutig sein.
    PushVCL(ctx context.Context, name, vcl string) error

    // ValidateVCL prüft VCL-Syntax ohne Aktivierung (vcl.load ohne vcl.use).
    // Gibt Zeilennummer + Fehlermeldung zurück wenn invalid.
    ValidateVCL(ctx context.Context, name, vcl string) (ValidationResult, error)

    // ActiveVCL gibt den Namen der aktuell aktiven VCL zurück.
    ActiveVCL(ctx context.Context) (string, error)

    // Ban pusht eine Ban-Expression über das Admin-Protokoll.
    // Expression muss bereits validiert sein (nur obj.http.*).
    Ban(ctx context.Context, expression string) error

    // DiscardVCL verwirft eine geladene aber nicht aktive VCL.
    // ACHTUNG: Darf erst nach Verzögerung nach vcl.use aufgerufen werden (§3.4).
    DiscardVCL(ctx context.Context, name string) error
}

type ValidationResult struct {
    Valid   bool
    Message string
    Line    int
}
```

---

## HTTP-Endpunkte

Vollständige Spezifikation in **architektur.md §3.3**. Hier die Kurzfassung:

| Endpunkt | Methode | Auth | Request | Response |
|----------|---------|------|---------|----------|
| `/vcl/push` | POST | Bearer | `{"name": "...", "vcl": "..."}` | `{"status": "ok"}` oder `{"status": "error", "message": "..."}` |
| `/vcl/validate` | POST | Bearer | `{"vcl": "..."}` | `{"status": "ok"}` oder `{"status": "error", "message": "...", "line": 42}` |
| `/vcl/active` | GET | Bearer | — | `{"name": "...", "status": "active"}` |
| `/ban` | POST | Bearer | `{"expression": "obj.http.X-Url ~ ^/product/"}` | `{"status": "ok"}` |
| `/purge/xkey` | POST | Bearer | `{"keys": ["article-123"]}` | `{"status": "ok", "purged": 42}` |
| `/health` | GET | **kein Auth** | — | `{"status": "ok", "varnish": "running"}` |
| `/metrics` | GET | **kein Auth** | — | Prometheus text format |

**Wichtig:** `/health` und `/metrics` dürfen **nie** Auth-Middleware bekommen. Das Readiness-Probe-Token wäre sonst im Pod-Spec sichtbar (§3.3 Sicherheitshinweis).

---

## TDD-Workflow

### Schritt 1: Middleware-Tests zuerst

```go
// internal/agent/middleware_test.go

func TestBearerAuth_MissingToken_Returns401(t *testing.T) { ... }
func TestBearerAuth_WrongToken_Returns401(t *testing.T) { ... }
func TestBearerAuth_CorrectToken_PassesThrough(t *testing.T) { ... }
func TestBearerAuth_HealthEndpoint_NoAuthRequired(t *testing.T) { ... }
func TestBearerAuth_MetricsEndpoint_NoAuthRequired(t *testing.T) { ... }
```

### Schritt 2: Handler-Unit-Tests mit Mock

```go
// internal/agent/handler_test.go

type mockAdminClient struct {
    pushVCLFn     func(ctx context.Context, name, vcl string) error
    validateVCLFn func(ctx context.Context, name, vcl string) (ValidationResult, error)
    // ...
}

func TestPushVCL_Success(t *testing.T) {
    mock := &mockAdminClient{
        pushVCLFn: func(ctx context.Context, name, vcl string) error { return nil },
    }
    // HTTP-Request gegen Handler, erwarte 200 + {"status": "ok"}
}

func TestPushVCL_AdminError_Returns500(t *testing.T) { ... }
func TestPushVCL_CompilationError_Returns400(t *testing.T) { ... }
func TestValidateVCL_InvalidSyntax_Returns400WithLine(t *testing.T) { ... }
func TestBan_EmptyExpression_Returns400(t *testing.T) { ... }
```

### Schritt 3: Integrationstests gegen echten varnishd

```go
// internal/agent/admin_test.go
//go:build integration

func TestAdminClient_PushAndActivateVCL(t *testing.T) {
    ctx := context.Background()
    varnish, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
        ContainerRequest: testcontainers.ContainerRequest{
            Image:        "varnish:7.6",
            ExposedPorts: []string{"6082/tcp"},
        },
        Started: true,
    })
    // ...
    client := NewAdminClient(host, port, secret)
    err = client.PushVCL(ctx, "test_vcl", minimalVCL)
    require.NoError(t, err)
    active, err := client.ActiveVCL(ctx)
    require.NoError(t, err)
    assert.Equal(t, "test_vcl", active)
}
```

### VCL-GC: Verzögertes Discard (§3.4)

Der Agent muss nach `vcl.use` eine Wartezeit einhalten bevor `vcl.discard` der alten VCL aufgerufen wird. Das verhindert 503-Fehler durch Worker-Threads die noch die alte VCL ausführen.

```go
// internal/agent/admin.go

func (c *varnishAdminClient) PushVCL(ctx context.Context, name, vcl string) error {
    // 1. vcl.load (kompilieren)
    // 2. vcl.use (aktivieren)
    // 3. Alte VCL asynchron nach Delay discarden
    go func() {
        time.Sleep(5 * time.Second) // Worker-Thread-Grace-Period (§3.4)
        c.DiscardVCL(context.Background(), oldName)
    }()
    return nil
}
```

### xkey-Purge (§6.4)

xkey ist eine VCL-Funktion — nicht aufrufbar über Admin-Port. Der Agent sendet stattdessen einen internen HTTP-PURGE:

```go
// internal/agent/xkey.go

func (x *XkeyPurger) Purge(ctx context.Context, keys []string, soft bool) (int, error) {
    for _, key := range keys {
        req, _ := http.NewRequestWithContext(ctx, "PURGE", "http://127.0.0.1:8080/", nil)
        req.Header.Set("X-Xkey-Purge", key)
        // soft-xkey via X-Xkey-Softpurge (wenn spec.xkey.softPurge: true)
        resp, err := http.DefaultClient.Do(req)
        // ...
    }
}
```

---

## Startup-Sequenz

Details in **architektur.md §3.3** (Startup-Sequenz-Diagramm). Implementierung:

```go
// cmd/agent/main.go

func main() {
    // 1. Token aus /run/vinyl/agent-token lesen (mit Retry, Datei kommt vom Secret-Mount)
    // 2. Auf varnishd Admin-Port warten (Polling mit exponential Backoff, max 60s)
    // 3. AdminClient initialisieren
    // 4. HTTP-Server starten
    // 5. /health gibt "ok" zurück (Readiness-Signal für Kubernetes)
}
```

---

## Container-Image

```dockerfile
# Dockerfile.agent
FROM golang:1.23 AS builder
WORKDIR /build
COPY . .
RUN CGO_ENABLED=0 go build -o agent ./cmd/agent

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /build/agent /agent
USER nonroot
ENTRYPOINT ["/agent"]
```

Distroless: kein Shell, minimale Angriffsfläche. Der Agent braucht keine externen Binaries (kein `varnishd`).

---

## Dokumentations-Deliverables

| Datei | Diataxis | Inhalt | Wann |
|-------|----------|--------|------|
| `docs/sources/reference/agent-api.md` | Reference | Vollständige API-Referenz aller Endpunkte | Nach Handler-Implementierung |
| `docs/sources/explanation/agent-design.md` | Explanation | Warum Sidecar, Sicherheitsmodell, Admin-Port-Bridging | Parallel |
| `docs/sources/how-to/agent-auth.md` | How-To | Token-Rotation, Secret-Management | Nach M1+M3 |

---

## Akzeptanzkriterien

- [ ] Alle Handler-Unit-Tests grün (vollständige Fehlerfall-Matrix)
- [ ] Bearer-Token-Middleware: 401 bei fehlendem/falschem Token
- [ ] `/health` ohne Auth erreichbar, gibt varnishd-Status zurück
- [ ] Integrationstests gegen echten varnishd: VCL-Push, Validate, Active, Ban
- [ ] VCL-GC: DiscardVCL wird nach Delay aufgerufen (nicht sofort)
- [ ] Binary kompiliert für `linux/amd64` und `linux/arm64` (Cross-Compilation)
- [ ] Coverage `internal/agent` ≥ 90%
- [ ] Distroless-Image baut und startet

---

## Offene Fragen

1. **Admin-Protokoll-Library:** Gibt es eine geeignete Go-Library für das Varnish Admin-Protokoll? Alternativen: `github.com/phenomenal-person/govarnish`, eigene Minimal-Implementierung. Das Protokoll ist einfach (TCP, Telnet-ähnlich) — Eigenimplementierung ist <100 LOC.
2. **Secret-Mount-Pfad:** `/run/vinyl/agent-token` — sicherstellen dass das im StatefulSet-Template (M4) so gemountet wird.
3. **xkey-Softpurge-Header:** Der genaue Header-Name für Soft-xkey-Purge muss mit der VCL-Implementierung (M2) abgestimmt werden.
