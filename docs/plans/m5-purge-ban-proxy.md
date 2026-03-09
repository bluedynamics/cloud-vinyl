# M5: Purge/BAN-Proxy

**Status:** Bereit nach M1, parallel zu M4
**Voraussetzungen:** M1 (CRD-Typen für allowedSources/Spec)
**Parallelisierbar mit:** M4 (Controller), M6 (Monitoring)
**Geschätzte Team-Größe:** 1 Person

---

## Kontext

Der Purge/BAN-Proxy ist ein eigenständiger HTTP-Server im Operator-Pod (Port 8090). Er empfängt Invalidierungs-Requests von externen Clients (Anwendungen, CI/CD-Pipelines) und broadcastet sie parallel an alle Varnish-Pods des jeweiligen `VinylCache`-Clusters.

Der Proxy läuft auf **allen** Operator-Replicas (nicht nur dem Leader) — damit ist Purge/BAN auch während Leader-Election-Pausen verfügbar. Jede Replica hält eine eigene In-Memory-Map der Pod-IPs via Kubernetes-Watch.

**Relevante Architektur-Abschnitte:**
- §6 (komplett) — Purge/BAN-Strategie: alle Protokoll-Details, Namensschema, Sicherheit
- §6.1 — Warum zentraler Proxy statt Sidecar
- §6.2 — Service ohne Selector + EndpointSlice (wie externe Clients den Proxy erreichen)
- §6.3 — Vorteile des Ansatzes
- §6.4 — Protokoll-Details: PURGE, BAN (HTTP-Methode + REST), xkey, Response-Format (200/207/503)
- §6.5 — Sicherheit: Host-Header-Routing, Source-IP-Check, BAN-Allowlist
- §6.6 — VCL-Integration: ACL im Generator (Operator-Pod-IP automatisch eingetragen)
- §7.5 — Leader-Election: Proxy läuft auf allen Replicas, kein Leader-Check

---

## Ziel

Nach M5 existiert:
- HTTP-Server auf Port 8090 mit allen Invalidierungs-Endpunkten
- Host-Header-basiertes Routing zum richtigen VinylCache-Cluster
- Paralleler Broadcast mit korrektem 200/207/503-Response-Format
- Source-IP-Check und BAN-Expression-Allowlist
- In-Memory-Pod-IP-Map via Kubernetes-Watch

---

## Package-Struktur

```
internal/proxy/
├── server.go              # HTTP-Server-Setup, Routing
├── server_test.go
├── handler.go             # PURGE, BAN, xkey Handler
├── handler_test.go        # Unit: alle Handler mit MockBroadcaster
├── broadcast.go           # Paralleler Broadcast an Pods + Response-Aggregation
├── broadcast_test.go      # Unit: 200/207/503 Logik, Timeout, Mock-HTTP-Server
├── routing.go             # Host-Header → VinylCache-Lookup
├── routing_test.go
├── acl.go                 # Source-IP-Check gegen allowedSources
├── acl_test.go
├── ban_allowlist.go       # BAN-Expression-Validierung (nur obj.http.*)
├── ban_allowlist_test.go
├── ratelimit.go           # Token-Bucket Rate-Limiter
├── ratelimit_test.go
└── podmap.go              # In-Memory-Pod-IP-Map (Kubernetes-Watch-basiert)
```

---

## Schlüssel-Interfaces

```go
// internal/proxy/broadcast.go

// Broadcaster sendet einen Request an alle Pods eines Clusters
// und aggregiert die Ergebnisse zum definierten Response-Format.
type Broadcaster interface {
    Broadcast(ctx context.Context, pods []string, req BroadcastRequest) BroadcastResult
}

type BroadcastRequest struct {
    Method  string            // "PURGE", "BAN", "POST"
    Path    string
    Headers map[string]string
    Body    []byte
}

type BroadcastResult struct {
    Status    string       // "ok" | "partial" | "failed"
    Total     int
    Succeeded int
    Results   []PodResult
}

type PodResult struct {
    Pod    string `json:"pod"`
    Status int    `json:"status,omitempty"`
    Error  string `json:"error,omitempty"`
}

// HTTP-Response-Codes: 200 (ok), 207 (partial), 503 (failed)
// Vollständige Spezifikation: architektur.md §6.4 (Response-Format bei Broadcast-Anfragen)
```

```go
// internal/proxy/podmap.go

// PodIPProvider liefert die aktuellen Pod-IPs für einen VinylCache.
// Implementierung: Kubernetes-Watch auf Pods (unabhängig vom Controller-Leader).
type PodIPProvider interface {
    GetPodIPs(namespace, cacheName string) []string
}
```

---

## TDD-Workflow

### Schritt 1: Broadcast-Response-Format zuerst

Das Response-Format ist exakt spezifiziert (§6.4) — hier beginnen:

```go
// internal/proxy/broadcast_test.go

func TestBroadcast_AllSuccess_Returns200(t *testing.T) {
    // 3 Mock-Pods antworten alle 200 → BroadcastResult.Status == "ok", HTTP 200
}

func TestBroadcast_PartialFailure_Returns207(t *testing.T) {
    // 2 von 3 Pods erfolgreich → Status "partial", HTTP 207
    // Body enthält Results[2].Error
}

func TestBroadcast_AllFailed_Returns503(t *testing.T) {
    // 0 von 3 Pods erfolgreich → Status "failed", HTTP 503
}

func TestBroadcast_Timeout_CountsAsFailure(t *testing.T) {
    // Pod antwortet nicht innerhalb Timeout → Error: "connection timeout after 5s"
}

func TestBroadcast_Parallel_AllPodsCalledSimultaneously(t *testing.T) {
    // Messung: 3 Pods mit je 100ms Latenz → Gesamtdauer < 200ms (parallel, nicht seriell)
}
```

### Schritt 2: ACL und Routing testen

```go
// internal/proxy/acl_test.go

func TestACL_AllowedSource_Passes(t *testing.T) { ... }
func TestACL_DeniedSource_Returns403(t *testing.T) { ... }
func TestACL_CIDRMatch_Works(t *testing.T) { ... }  // 10.0.0.0/24 matcht 10.0.0.5

// internal/proxy/routing_test.go

func TestRouting_KnownHost_ReturnsVinylCache(t *testing.T) {
    // Host: my-cache-invalidation.production → production/my-cache
}
func TestRouting_UnknownHost_Returns404(t *testing.T) { ... }
```

### Schritt 3: BAN-Expression-Allowlist

```go
// internal/proxy/ban_allowlist_test.go

func TestBanAllowlist_ObjHttpUrl_Allowed(t *testing.T) {
    // "obj.http.X-Url ~ ^/product/" → ok
}
func TestBanAllowlist_ObjHttpContentType_Allowed(t *testing.T) { ... }
func TestBanAllowlist_ReqUrl_Rejected(t *testing.T) {
    // "req.url ~ ^/product/" → 400 (req.* nicht erlaubt, Ban-Lurker kann das nicht)
}
func TestBanAllowlist_WildcardPattern_Rejected(t *testing.T) {
    // "obj.http.content-type ~ ." → 400 (gesamter Cache, DoS-Risiko)
}
```

### Schritt 4: Handler-Integration (Mock-Broadcaster)

```go
// internal/proxy/handler_test.go

func TestHandler_PURGE_BroadcastsToAllPods(t *testing.T) { ... }
func TestHandler_BAN_ValidatesExpression(t *testing.T) { ... }
func TestHandler_BAN_REST_JSONBody(t *testing.T) { ... }
func TestHandler_Xkey_SendsToAllPods(t *testing.T) { ... }
func TestHandler_RateLimit_Returns429(t *testing.T) { ... }
```

---

## Protokoll-Details

Vollständig in **architektur.md §6.4**. Kurzzusammenfassung:

**PURGE** (URL-basiert):
```
PURGE /product/123 HTTP/1.1
Host: my-cache-invalidation.production.svc.cluster.local
```
→ Proxy broadcastet PURGE direkt an alle Varnish-Pods auf Port 8080.

**BAN via REST** (empfohlen):
```
POST /ban HTTP/1.1
X-Ban-Expression: obj.http.X-Url ~ ^/product/
```
→ Proxy validiert Expression, sendet via Admin-Protokoll an Pods.

**xkey**:
```
POST /purge/xkey HTTP/1.1
{"keys": ["article-123"]}
```
→ Proxy sendet HTTP-PURGE mit `X-Xkey-Purge`-Header an Pods.

---

## Sicherheit (§6.5)

Zwei unabhängige Prüfungen bei jedem Request:
1. **Host-Header**: Identifiziert den Ziel-VinylCache (unbekannt → 404)
2. **Source-IP**: Gegen `spec.invalidation.*.allowedSources` (nicht erlaubt → 403)

Wichtig: Diese Prüfungen sind **unabhängig** — auch direkte Requests an den Operator-Pod (ohne den Invalidierungs-Service zu nutzen) werden geprüft.

---

## Dokumentations-Deliverables

| Datei | Diataxis | Inhalt | Wann |
|-------|----------|--------|------|
| `docs/sources/reference/invalidation-api.md` | Reference | Vollständige Protokoll-Referenz: PURGE, BAN, xkey, Response-Format | Nach Handler |
| `docs/sources/how-to/configure-purge.md` | How-To | allowedSources, BAN-Allowlist, Rate-Limiting konfigurieren | Nach M5 |
| `docs/sources/how-to/invalidation-clients.md` | How-To | Client-Implementierung, Retry bei 207, Partial-Failure handling | Nach M5 |
| `docs/sources/explanation/invalidation.md` | Explanation | PURGE vs. BAN vs. xkey, Grace-Interaktion, warum zentraler Proxy | Parallel |

---

## Akzeptanzkriterien

- [ ] Broadcast: 200/207/503 je nach Erfolgsquote korrekt
- [ ] Broadcast ist parallel (nicht seriell) — verifiziert durch Timing-Test
- [ ] Unbekannter Host → 404
- [ ] Source-IP außerhalb allowedSources → 403
- [ ] BAN-Expression mit `req.*` LHS → 400
- [ ] Rate-Limit überschritten → 429
- [ ] Coverage `internal/proxy` ≥ 90%

---

## Offene Fragen

1. **Rate-Limit-Konfiguration:** Wo wird das Rate-Limit konfiguriert? Im CRD (`spec.invalidation.rateLimit`) oder als Operator-Flag? CRD ist flexibler (pro VinylCache), Flag ist einfacher. Empfehlung: CRD-Feld `spec.invalidation.ban.rateLimitPerMinute`.
2. **BAN via Admin-Protokoll:** Der Proxy muss für BAN einen Admin-Client zum Varnish-Pod aufbauen (nicht den Agent-HTTP-Endpunkt). Entweder direkt das Admin-Protokoll implementieren (wie in M3) oder den Agent-`/ban`-Endpunkt nutzen. Einfacher: Agent-Endpunkt nutzen — vermeidet doppelte Admin-Client-Implementierung.
3. **Pod-IP-Map Synchronisation:** Die In-Memory-Pod-IP-Map muss threadsafe sein (mehrere gleichzeitige Broadcast-Requests). `sync.RWMutex` verwenden.
