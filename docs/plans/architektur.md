# cloud-vinyl — Architektur-Designdokument

**Version:** 0.1-draft  
**Datum:** 2026-03-08  
**Status:** In Diskussion

---

## Inhaltsverzeichnis

1. [Übersicht & Ziele](#1-übersicht--ziele)
2. [CRD-Design](#2-crd-design)
3. [Komponentenarchitektur](#3-komponentenarchitektur)
4. [VCL-Generierung](#4-vcl-generierung)
5. [Clustering-Strategie](#5-clustering-strategie)
6. [Purge/BAN-Strategie](#6-purgeban-strategie)
7. [Operator Lifecycle & Reconcile-Loop](#7-operator-lifecycle--reconcile-loop)
8. [Technischer Stack](#8-technischer-stack)
9. [Architekturentscheidungen (ADR)](#9-architekturentscheidungen-adr)
10. [Security-Modell](#10-security-modell)
11. [API-Versionierungsstrategie](#11-api-versionierungsstrategie)

---

## 1. Übersicht & Ziele

### 1.1 Motivation

Bestehende Ansätze, Varnish in Kubernetes zu betreiben, verwenden typischerweise einen Sidecar-Prozess im Varnish-Pod selbst. Dieser Ansatz hat strukturelle Schwächen: Das Chicken-and-Egg-Problem beim Clustering (der Sidecar kennt seine eigenen Pod-IPs erst, wenn der Pod ready ist), fehlendes Retry bei VCL-Update-Fehlern, kein Debouncing bei schnellen Endpoint-Änderungen, kein Multi-Backend-Support und fehlender Non-Root-Betrieb sind Konsequenzen dieser Grundarchitektur — sie lassen sich nicht durch Patches beheben.

`cloud-vinyl` wählt einen anderen Ausgangspunkt: ein vollwertiger Kubernetes-Operator, der Varnish-Cluster als First-Class-Kubernetes-Ressource verwaltet.

### 1.2 Ziele

**Architektur**
- Full Operator: Zentrales Deployment, nicht Sidecar. Ein Operator-Pod verwaltet beliebig viele `VinylCache`-Instanzen im Cluster.
- CRD-basiert: Der gewünschte Zustand eines Varnish-Clusters wird vollständig in einem `VinylCache`-Objekt beschrieben.
- Echter Reconcile-Loop: Kubernetes-idiomatisch, kein Silent-Drop bei Fehlern, Status-Subresource für Observability.

**Features**
- Mehrere Backends als First-Class-Bürger (mehrere Services mit eigener Gewichtung, Health-Probes und Director-Konfiguration)
- Shard-Director als empfohlenes Default für Clustering (nicht hash-Director)
- Integrierter Purge/BAN-Proxy im Operator — kein separater Port pro Pod
- Debouncing konfigurierbar (Grace-Period vor VCL-Push)
- Non-Root by Default

**VCL-Ansatz**
- Strukturierte Konfiguration im CRD generiert typsichere VCL
- Snippet-Hooks an allen VCL-Subroutinen (vcl_init, vcl_recv, vcl_hash, vcl_hit, vcl_miss, vcl_pass, vcl_purge, vcl_pipe, vcl_backend_fetch, vcl_backend_response, vcl_deliver, vcl_synth, vcl_backend_error, vcl_fini) plus `header`-Hook für Imports
- Full-Override als Escape-Hatch für fortgeschrittene Anwendungsfälle

**Betrieb**
- varnish-modules im Standard-Image
- Prometheus-Metriken für alle Komponenten
- Sinnvolle Default-Probes out of the box
- Automatisierte Releases und Dependency-Updates (CI/CD, Dependabot)

### 1.3 Nicht-Ziele

- `cloud-vinyl` ist kein Varnish-as-a-Service für den Endnutzer. Es ist eine Infrastrukturkomponente.
- Kein Wrapper um einen spezifischen Anwendungsfall (z. B. kein "Drupal-Cache-Operator"). Die Abstraktion bleibt auf Varnish-Cluster-Management.
- Keine Unterstützung für Varnish Enterprise (Varnish Cache Plus). Ausschließlich Varnish Cache (OSS).
- Kein Management von Ingress- oder Gateway-Objekten — das ist Aufgabe der darüberliegenden Infrastruktur.

---

## 2. CRD-Design

### 2.1 Ressource-Übersicht

```
Gruppe:     vinyl.bluedynamics.eu
Version:    v1alpha1
Kind:       VinylCache
Scope:      Namespaced
Short-Name: vc
Kategorie:  vinyl
```

**Kubebuilder-Marker** (in `vinylcache_types.go`):

```go
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:subresource:scale:specpath=.spec.replicas,statuspath=.status.replicas,selectorpath=.status.selector
// +kubebuilder:resource:shortName=vc,categories=vinyl
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Replicas",type="string",JSONPath=".status.readyReplicas",priority=0
// +kubebuilder:printcolumn:name="VCL",type="string",JSONPath=".status.conditions[?(@.type=='VCLSynced')].status"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase",priority=1
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
```

Damit zeigt `kubectl get vc` (oder `kubectl get vinylcaches`):
```
NAME       READY   REPLICAS   VCL    AGE
my-cache   True    3          True   2d
```

`VinylCache` beschreibt einen vollständigen Varnish-Cluster: wie viele Replicas, welche Backends, wie gecacht wird, wie die VCL aussieht und wie invalidiert wird.

Der Operator erzeugt und verwaltet daraus:
- ein `StatefulSet` (Varnish-Pods)
- einen headless `Service` (Cluster-Peers)
- einen regulären `Service` (Traffic-Eingang)
- einen Invalidierungs-`Service` ohne Selector + manuell verwaltetes `EndpointSlice` (Purge/BAN-Endpunkt, im Namespace des `VinylCache`, zeigt auf Operator-Pod-IPs — Details siehe Abschnitt 6.2)
- `NetworkPolicy`-Objekte (Zugriffskontrolle auf Agent- und Varnish-Ports)
- eine `ServiceAccount` + `RBAC`-Objekte
- optional: `PodDisruptionBudget`

### 2.2 Vollständiges Beispiel-YAML

```yaml
apiVersion: vinyl.bluedynamics.eu/v1alpha1
kind: VinylCache
metadata:
  name: my-cache
  namespace: production

spec:
  # ---------------------------------------------------------------------------
  # Cluster-Größe
  # ---------------------------------------------------------------------------
  replicas: 3

  # ---------------------------------------------------------------------------
  # Varnish-Container-Image
  # ---------------------------------------------------------------------------
  image:
    repository: ghcr.io/bluedynamics/cloud-vinyl-varnish
    tag: "7.6.0"
    pullPolicy: IfNotPresent

  # ---------------------------------------------------------------------------
  # Ressourcen für den Varnish-Container
  # ---------------------------------------------------------------------------
  resources:
    requests:
      cpu: "500m"
      memory: "512Mi"
    limits:
      cpu: "2"
      memory: "2Gi"

  # ---------------------------------------------------------------------------
  # Storage-Konfiguration (Array-Struktur für Erweiterbarkeit)
  # Entspricht varnishd -s <name>=<backend>,<size>
  # HINWEIS: Größen als resource.Quantity (1Gi = binär, nicht "1G" dezimal).
  #          Der Operator konvertiert für varnishd: 1Gi → 1073741824.
  # ---------------------------------------------------------------------------
  # malloc-Sizing-Faustregel: max. 75% des verfügbaren Pod-Speichers allokieren
  # (5% Overhead + ~20% jemalloc-Fragmentierung). Bei 2Gi Limit: ca. 1.5Gi für Cache.
  storage:
    - name: cache            # Interner Name (entspricht varnishd -s <name>=...)
      type: malloc           # malloc | file
                             # HINWEIS: umem (Solaris/illumos) und persistent (deprecated,
                             # fundamentale Konsistenzprobleme) werden vom Webhook abgelehnt.
                             # 'default' entfällt — auf Linux identisch mit malloc, verwirrend.
                             # file-Storage ist memory-mapped (mmap): der Kernel entscheidet
                             # über Paging, kein klassischer "Festplatten-Cache".
      size: "1Gi"            # resource.Quantity (binär: 1 GiB)
    - name: transient        # Transient-Speicher für unkachierbare Objekte
      type: malloc
      size: "128Mi"
    # Optionaler file-basierter Persistent-Cache:
    # - name: persistent
    #   type: file
    #   path: /var/lib/varnish/cache
    #   size: "10Gi"

  # ---------------------------------------------------------------------------
  # Backends: Mehrere Backend-Services mit individueller Konfiguration
  # Mindestens ein Backend ist Pflicht.
  # ---------------------------------------------------------------------------
  backends:
    - name: app                          # Interner Name, wird in VCL verwendet
      serviceRef:
        name: app-service                # Kubernetes-Service im selben Namespace
        port: 8080
        # namespace: other-ns           # Cross-Namespace (erfordert erweiterte RBAC)
      weight: 100                        # Relative Gewichtung für Director
      # Varnish-Backend-Probe (inline, wird in VCL emittiert)
      probe:
        path: /healthz
        interval: 5s
        timeout: 2s
        threshold: 3                     # Mindestanzahl gesunder Checks
        window: 5                        # Fenster für threshold-Bewertung
      # Verbindungsparameter (Connection-Pool)
      connectionParameters:
        connectTimeout: "1s"
        firstByteTimeout: "60s"
        betweenBytesTimeout: "60s"
        maxConnections: 200
        # idleTimeout: Muss KLEINER als Backend-Keep-Alive-Timeout sein.
        # Apache/Node.js/Go default: 5s. Varnish-Default 60s → Race-Condition → 503.
        # Varnish versucht eine Verbindung wiederzuverwenden, die das Backend bereits
        # geschlossen hat. Empfohlen: 4s (Apache 5s - 1s Safety-Margin).
        idleTimeout: "4s"

    - name: legacy
      serviceRef:
        name: legacy-service
        port: 8080
      weight: 0                          # weight: 0 = Standby (erzwingt fallback-Director statt shard)
      probe:
        path: /ping
        interval: 10s
        timeout: 3s
        threshold: 2
        window: 3
      connectionParameters:
        connectTimeout: "2s"
        firstByteTimeout: "120s"
        betweenBytesTimeout: "60s"
        maxConnections: 50
        idleTimeout: "4s"

  # ---------------------------------------------------------------------------
  # Director: Lastverteilung zwischen Backend-Endpoints
  # ---------------------------------------------------------------------------
  director:
    type: shard                          # shard | round_robin | random | fallback
    # Director-Typ gilt pro Backend für dessen Endpoints. Ausnahme:
    # Backends mit weight: 0 erhalten immer einen fallback-Director
    # (Standby-Semantik), unabhängig vom hier gesetzten Typ.
    # shard-spezifisch: Konsistenz-Hash über Backends
    shard:
      warmup: 0.1                        # Anteil Warmup-Traffic (0.0–1.0)
                                         # Pre-populiert den Cache des Alternativ-Backends.
                                         # Ohne warmup: kalter Cache bei Failover.
      rampup: 30s                        # Ramp-up-Periode nach Hinzufügen eines Backends.
                                         # Verhindert Thundering-Herd auf einen neu-healthy Pod.
                                         # Ohne rampup: Pod bekommt sofort 100% seines Key-Ranges.
      # Selten zu ändern (Varnish-Defaults):
      replicas: 67                       # Ketama-Replicas pro Backend im Ring
      by: HASH                           # HASH | URL | KEY | BLOB (HASH ist korrekt)
      healthy: CHOSEN                    # CHOSEN | IGNORE | ALL (CHOSEN ist korrekt)

  # ---------------------------------------------------------------------------
  # Clustering: Wie Varnish-Pods untereinander kommunizieren
  # ---------------------------------------------------------------------------
  cluster:
    enabled: true
    # peerRouting: Routing-Strategie zwischen Varnish-Pods (≠ spec.director, der das
    # Backend-Routing steuert). shard ist der einzige sinnvolle Wert für Clustering.
    peerRouting:
      type: shard
    # Peer-Port: Port auf dem andere Varnish-Pods erreichbar sind
    peerPort: 8080

  # ---------------------------------------------------------------------------
  # Varnish-Runtime-Parameter (varnishd -p)
  # ---------------------------------------------------------------------------
  varnishParameters:
    thread_pool_min: "100"
    thread_pool_max: "1000"
    thread_pool_timeout: "300"
    timeout_idle: "5"
    http_max_hdr: "64"
    # HINWEIS: Folgende Parameter sind vom Operator blockiert und können nicht gesetzt werden:
    # vcc_allow_inline_c  — erlaubt C-Code in VCL (Remote Code Execution)
    # cc_command          — beliebiger Compiler-Befehl
    # feature +esi_disable_xml_check — XSS via ESI

  # ---------------------------------------------------------------------------
  # VCL-Konfiguration
  # Drei Ebenen: strukturiert (default) > snippets (additiv) > fullOverride (ersetzt alles)
  # ---------------------------------------------------------------------------
  vcl:
    # --- Ebene 1: Strukturierte Konfiguration ---
    # Steuert das Verhalten der generierten VCL ohne eigenen VCL-Code.

    # Standard-Caching-Verhalten
    cache:
      defaultTTL: "120s"               # Fallback-TTL wenn kein Cache-Control
      defaultGrace: "24h"              # Grace-Period für stale-while-revalidate
      defaultKeep: "0s"                # Wie lange nach Grace behalten

      # Cache-Bypass für bestimmte Cookies (Regex)
      bypassCookies:
        - "^SESS[0-9a-f]+"            # Drupal-Session-Cookie
        - "^wordpress_logged_in_"
        - "^wp-settings-"

      # Cache-Bypass für bestimmte URL-Pfade (Regex)
      bypassPaths:
        - "^/admin"
        - "^/wp-admin"
        - "^/user/login"

      # Respekt für X-Accel-Expires-Header (nginx-kompatibel)
      respectXAccelExpires: false

      # Accept-Encoding-Normalisierung: verhindert Cache-Fragmentierung.
      # Modi:
      #   gzip-only  (Default, sicher): Normalisiert auf gzip, entfernt alles andere.
      #              Varnish kann gzip nativ komprimieren/dekomprimieren.
      #   gzip-br:   Normalisiert auf bis zu 3 Varianten (gzip+br, gzip, br).
      #              Empfohlen wenn Backends Brotli liefern — ~20% kleinere Responses.
      #              ACHTUNG: In diesem Modus muss http_gzip_support deaktiviert werden;
      #              die Normalisierung passiert vollständig in der generierten VCL.
      #              Varnish kann Brotli-Antworten cachen und durchreichen, aber nicht
      #              selbst komprimieren/dekomprimieren.
      #   off:        Keine Normalisierung — Vorsicht: Cache-Fragmentierung durch
      #              beliebige Accept-Encoding-Varianten (gzip, br, deflate, Kombis).
      normalizeAcceptEncoding: gzip-only

      # Edge Side Includes (ESI): Varnish assembliert fragmentierte Seiten aus
      # mehreren Backend-Requests. Für CMS-Anwendungen (Plone, Drupal, WordPress)
      # häufig benötigt. Aktiviert beresp.do_esi = true in vcl_backend_response.
      #
      # VARNISH-VERHALTEN: ESI-Subrequests werden SEQUENTIELL verarbeitet (kein Parallel-ESI
      # in Open-Source-Varnish). 5 Includes × 50ms Latenz = 250ms zusätzliche Latenz.
      #
      # PRODUKTIONS-WARNUNG: Unkachierbare ESI-Fragmente sind der häufigste Produktionsausfall.
      # Jeder Seitenaufruf erzeugt einen Backend-Request für jedes unkachierbare Fragment.
      # Dokumentierter Incident: Varnish 11+ Minuten unerreichbar durch Request-Queue-Saturation.
      # Der Operator emittiert eine Warning-Condition wenn ESI aktiviert aber keine explizite
      # TTL-Strategie für Fragmente konfiguriert ist.
      esi:
        enabled: false
        # Maximale ESI-Verschachtelungstiefe (Varnish-Default: 5)
        maxDepth: 5
        # Thread-Stack-Größe für ESI-Subrequests.
        # KRITISCH bei enabled: true — Varnish-Default 48KB verursacht Stack-Overflow
        # bei Tiefe >4 mit -fstack-protector-strong (Issue #2129, alle modernen Distros).
        # Der Operator setzt thread_pool_stack AUTOMATISCH auf diesen Wert wenn ESI aktiviert.
        # Nie manuell über varnishParameters setzen müssen — wird hier abgeleitet.
        threadPoolStack: "80KB"
        # ESI-Includes über HTTPS: Varnish kann kein TLS zu sich selbst aufbauen.
        # Bei ignoreHttps: false werden https://-ESI-Includes als Fehler behandelt.
        ignoreHttps: true

      # WebSocket-Passthrough: leitet Upgrade-Requests direkt durch (pipe),
      # ohne Cache-Lookup. Default: false (Varnish's Standard-Verhalten).
      websocketPassthrough: false

    # --- Ebene 2: VCL-Snippets (Hook-System) ---
    # An jeder generierten Subroutine können Snippets eingefügt werden.
    # Snippets werden in der definierten Reihenfolge vor/nach dem generierten Code eingefügt.
    #
    # SICHERHEITSHINWEIS: Rechte zum Bearbeiten von VinylCache-Objekten (kubectl edit / patch)
    # sind gleichbedeutend mit Code-Execution-Rechten auf Varnish-Pods. VCL-Snippets werden
    # ohne weitere Sanitisierung in die generierte VCL übernommen. RBAC muss entsprechend
    # restriktiv gesetzt werden.
    #
    # GRÖßENLIMIT: Max. 64 KB pro Snippet-Feld (CEL-Validierung im CRD-Schema).
    # Kubernetes-Objekte haben ein etcd-Limit von 1,5 MB — große VCL-Dateien
    # sollten über fullOverrideRef (ConfigMap) statt Inline-Snippets verwaltet werden.
    # Für zukünftige v1beta1-Feature: snippetRefs (ConfigMapKeyRef) für einzelne Hooks.
    snippets:
      # Snippets, die am Anfang der VCL-Datei eingefügt werden (z.B. import-Statements)
      header: |
        import geoip2;

      # Snippets im vcl_init-Block (nach Director-Initialisierung)
      vclInit: |
        new geo = geoip2.open("/usr/share/geoip/GeoLite2-Country.mmdb");

      # Snippets im vcl_recv-Block
      # Position: NACH der generierten Routing-Logik, VOR return()
      vclRecv: |
        # Eigene Cookie-Normalisierung
        if (req.http.Cookie) {
          set req.http.Cookie = regsuball(req.http.Cookie,
            "(^|;\s*)(_ga|_gid|_gat)[^;]*", "");
        }
        if (req.http.Cookie == "") {
          unset req.http.Cookie;
        }

      # Snippets im vcl_hash-Block
      vclHash: |
        # Variante nach Land cachen
        hash_data(geoip2.lookup_str(client.ip));

      # Snippets im vcl_hit-Block
      vclHit: ""

      # Snippets im vcl_miss-Block
      vclMiss: ""

      # Snippets im vcl_pass-Block
      vclPass: ""

      # Snippets im vcl_backend_fetch-Block
      vclBackendFetch: |
        # Debug-Header
        set bereq.http.X-Varnish-Node = server.identity;

      # Snippets im vcl_backend_response-Block
      vclBackendResponse: |
        # Uncachierbare Antworten kurz im Transient-Speicher halten
        if (beresp.uncacheable) {
          set beresp.ttl = 30s;
          set beresp.grace = 0s;
        }

      # Snippets im vcl_deliver-Block
      vclDeliver: |
        # Interne Header entfernen
        unset resp.http.X-Powered-By;
        # HINWEIS: X-Cache-Header (HIT/MISS) werden bei debug.responseHeaders: true
        # automatisch VOR diesem Snippet generiert. Bei debug.responseHeaders: false
        # (wie hier) muss das Snippet sie selbst setzen, falls gewünscht.

      # Snippets im vcl_pipe-Block
      vclPipe: ""

      # Snippets im vcl_purge-Block
      vclPurge: ""

      # Snippets im vcl_synth-Block
      vclSynth: ""

      # Snippets im vcl_backend_error-Block
      vclBackendError: ""

      # Snippets im vcl_fini-Block (VMOD-Cleanup beim VCL-Discard)
      vclFini: ""

    # --- Debug-Header (opt-in) ---
    # Wenn aktiviert, generiert der Operator in vcl_deliver: X-Cache (HIT/MISS), X-Cache-Hits,
    # und in vcl_backend_fetch: X-Varnish-Node — VOR dem User-Snippet.
    # HINWEIS: Diese Header verraten interne Topologie-Informationen (Pod-Namen, Cache-Status).
    # Nur in nicht-produktiven Umgebungen oder hinter einem Header-strippenden Reverse-Proxy aktivieren.
    # Interaktion mit Snippets: debug.responseHeaders generiert Code VOR dem vclDeliver-Snippet.
    # User-Snippets können die gesetzten Header überschreiben oder entfernen.
    # Wenn das Snippet eigene X-Cache-Header setzt, hat es Vorrang.
    debug:
      responseHeaders: false            # Default: aus

    # --- Ebene 3: Full-Override ---
    # Wenn gesetzt, wird die gesamte VCL-Generierung übersprungen.
    # Der Operator injiziert NUR die Backend-Definitionen als Kommentar-Block
    # am Anfang, damit der Operator-Status noch korrekt ist.
    # ACHTUNG: Kein automatisches Clustering-Setup. Volle Verantwortung beim Nutzer.
    #
    # fullOverride: |
    #   vcl 4.1;
    #   import std;
    #   ...

    # Referenz auf eine ConfigMap mit dem Full-Override-VCL
    # (Alternative zu fullOverride als Inline-String — für große VCL-Dateien)
    # fullOverrideRef:
    #   configMapName: my-vcl-config
    #   key: vcl

  # ---------------------------------------------------------------------------
  # Purge/BAN-Konfiguration
  # ---------------------------------------------------------------------------
  invalidation:
    # Purge-Endpunkt im Operator aktivieren
    purge:
      enabled: true
      # Erlaubte Quell-IPs/CIDRs für PURGE-Requests (Varnish ACL wird generiert).
      # HINWEIS: Per Default leer — der Operator trägt automatisch seine eigene Pod-IP ein.
      # Kein RFC1918-Default: jeder Pod im Cluster könnte sonst den Cache invalidieren.
      # Nur explizit benötigte Quellen eintragen.
      allowedSources: []
      # Beispiele:
      # allowedSources:
      #   - "10.1.2.3/32"   # spezifische CI/CD-Pipeline-IP
      #   - "10.2.0.0/24"   # Anwendungs-Namespace-Subnet
      # Soft-Purge: Objekt wird als expired markiert (TTL=0), bleibt aber für Grace-Delivery
      # verfügbar. Erster Request nach Soft-Purge bekommt sofort die stale Response,
      # Backend-Fetch passiert asynchron im Hintergrund — kein Thundering-Herd.
      # Hard-Purge (soft: false) entfernt das Objekt sofort — kein Grace, kein Stale-Serving.
      # Bei 100 gleichzeitigen Requests nach Hard-Purge: Thundering-Herd oder Coalescing-Queue.
      # Erfordert vmod_purge (in varnish-modules enthalten).
      #
      # WARNUNG bei soft: true + defaultGrace: 24h: Nach Soft-Purge wird noch bis zu 24h
      # stale Content ausgeliefert (async Refresh nur beim ersten Request nach Purge).
      # Für Inhalte die "sofort weg müssen": hard: true verwenden.
      soft: true

    # BAN-Endpunkt im Operator aktivieren
    # BAN-Requests werden ausschließlich über den REST-Endpunkt des Invalidierungs-Proxy
    # entgegengenommen (POST /ban mit JSON-Body). VCL-seitiger BAN via HTTP-Methode
    # ist deaktiviert (verhindert BAN-Expression-Injection über req.url).
    ban:
      enabled: true
      allowedSources: []
      # BAN-Lurker aktivieren: intern gemappt auf ban_lurker_sleep > 0
      # (es gibt keinen varnishd-Parameter namens "lurker").
      # true  → ban_lurker_sleep Default (0.01s) — Lurker aktiv
      # false → ban_lurker_sleep 0 — Lurker deaktiviert
      # Fein-Tuning über spec.varnishParameters möglich:
      #   ban_lurker_sleep: "0.01"   # Pause zwischen Ban-Prüfungen
      #   ban_lurker_age: "60"       # Mindestalter eines Bans bevor Lurker ihn prüft
      lurker: true

    # Surrogate-Key-Invalidierung via vmod_xkey (in varnish-modules enthalten).
    # Ermöglicht tag-basierte Invalidierung: "lösche alle Objekte mit Tag article-123".
    # Der Operator generiert automatisch xkey-Header-Handling in vcl_backend_response
    # und exponiert POST /purge/xkey am Invalidierungs-Service.
    xkey:
      enabled: false
      # Name des Headers, den das Backend zum Setzen von Surrogate-Keys verwendet.
      headerName: "xkey"    # Kompatibel mit Fastly/Varnish-Standard
      # Immer Soft-Purge verwenden (empfohlen, Default: true).
      # Soft-Purge preserviert Grace — kein kalter Cache nach Invalidierung.
      # Hard-Purge (softPurge: false) nur als expliziter Opt-in für regulatorische Anforderungen.
      softPurge: true
      #
      # SKALIERUNGS-WARNUNG: xkey ist in Maintenance-Mode mit bekannten Mutex-Problemen.
      # xkey piggybacks auf dem internen Expiry-Mutex. Während Object-Insertion oder Purge
      # blockiert xkey ALLE anderen Expiry-Operationen (inkl. normales TTL-Expiry).
      # Auf High-Traffic-Sites mit Millionen Objekten oder Sekunden-Purges → globaler Bottleneck.
      # Für moderate Workloads (tausende Objekte, Purges im Minutentakt) akzeptabel.
      # Alternative für pattern-basierte Invalidierung: BAN-Expressions (/news/* etc.).

  # ---------------------------------------------------------------------------
  # Service-Konfiguration
  # ---------------------------------------------------------------------------
  # ---------------------------------------------------------------------------
  # PROXY-Protocol-Support
  # Wenn aktiviert: zweiter Listener auf Port 8081 mit PROXY-Protocol.
  # varnishd startet mit: -a "0.0.0.0:8080,HTTP" -a "0.0.0.0:8081,PROXY"
  # client.ip in VCL zeigt automatisch die echte Client-IP aus dem PROXY-Header.
  # Traefik: serversTransport.proxyProtocol auf den Varnish-Backend-Pool setzen.
  # Der Traffic-Service exponiert Port 8081 automatisch wenn enabled: true.
  # ---------------------------------------------------------------------------
  proxyProtocol:
    enabled: false
    port: 8081                           # Port für den PROXY-Protocol-Listener

  service:
    # Traffic-Service (regulärer ClusterIP/LoadBalancer)
    traffic:
      type: ClusterIP
      port: 80
      annotations: {}
    # Purge/BAN-Service (ClusterIP, nur interner Traffic)
    invalidation:
      type: ClusterIP
      port: 8090

  # ---------------------------------------------------------------------------
  # Debouncing: Wartezeit nach der letzten Änderung vor VCL-Push
  # Verhindert N VCL-Updates bei N gleichzeitigen Pod-Starts
  # ---------------------------------------------------------------------------
  # HINWEIS: Duration-Felder verwenden metav1.Duration (Go-Typ), repräsentiert
  # als String im Format "5s", "30s", "1m30s", "24h". Dieser Stil ist der
  # controller-runtime/kubebuilder-Standard für Operator-Projekte (im Gegensatz
  # zu Kubernetes-Kern-Feldern wie terminationGracePeriodSeconds die Integer verwenden).
  debounce:
    period: "5s"                        # Wartezeit nach letztem Trigger
    maxDelay: "30s"                     # Maximale Verzögerung (erzwingt Update)

  # ---------------------------------------------------------------------------
  # Retry-Strategie für VCL-Push
  # ---------------------------------------------------------------------------
  retry:
    attempts: 5                         # Maximale Versuche
    backoff:
      initialInterval: "1s"
      multiplier: 2.0
      maxInterval: "30s"

  # ---------------------------------------------------------------------------
  # Pod-Template-Konfiguration (Security, Scheduling)
  # Alle Pod-spezifischen Felder sind unter spec.pod gruppiert, um Top-Level-
  # Proliferation zu vermeiden und eine saubere v1beta1-Migration zu erlauben.
  # ---------------------------------------------------------------------------
  pod:
    # Security-Kontext (Non-Root by Default)
    securityContext:
      runAsNonRoot: true
      runAsUser: 1000
      runAsGroup: 1000
      fsGroup: 1000
      seccompProfile:
        type: RuntimeDefault

    containerSecurityContext:
      allowPrivilegeEscalation: false
      readOnlyRootFilesystem: true       # /tmp und /var/lib/varnish als emptyDir gemountet
      capabilities:
        drop:
          - ALL

    # Scheduling
    nodeSelector: {}
    tolerations: []
    affinity:
      podAntiAffinity:
        preferredDuringSchedulingIgnoredDuringExecution:
          - weight: 100
            podAffinityTerm:
              topologyKey: kubernetes.io/hostname
              labelSelector:
                matchLabels:
                  vinyl.bluedynamics.eu/cache: my-cache

    topologySpreadConstraints:
      - topologyKey: topology.kubernetes.io/zone
        maxSkew: 1
        whenUnsatisfiable: ScheduleAnyway
        labelSelector:
          matchLabels:
            vinyl.bluedynamics.eu/cache: my-cache

  # ---------------------------------------------------------------------------
  # PodDisruptionBudget
  # ---------------------------------------------------------------------------
  podDisruptionBudget:
    enabled: true
    minAvailable: 1                     # oder maxUnavailable: 1

  # ---------------------------------------------------------------------------
  # Monitoring
  # ---------------------------------------------------------------------------
  monitoring:
    # ServiceMonitor für Prometheus Operator
    serviceMonitor:
      enabled: true
      labels:
        prometheus: kube-prometheus
      interval: "30s"

    # varnishstat-Exporter als Sidecar
    # HINWEIS: Exporter-Image vor Produktionseinsatz prüfen — entweder in eigene Registry
    # spiegeln (ghcr.io/bluedynamics/varnish-exporter) oder Digest pinnen statt Tag.
    exporter:
      enabled: true
      image:
        repository: ghcr.io/bluedynamics/varnish-exporter   # empfohlen: eigene gespiegelte Registry
        tag: "1.6.1"
      resources:
        requests:
          cpu: "50m"
          memory: "32Mi"
        limits:
          cpu: "200m"
          memory: "64Mi"

  # ---------------------------------------------------------------------------
  # Logging-Sidecar (varnishncsa)
  # ---------------------------------------------------------------------------
  accessLog:
    enabled: false
    format: |
      %{X-Forwarded-For}i %l %u %t "%r" %s %b "%{Referer}i" "%{User-agent}i"

  # ---------------------------------------------------------------------------
  # Zusätzliche Volumes und Mounts (für GeoIP-DBs, etc.)
  # ---------------------------------------------------------------------------
  extraVolumes:
    - name: geoip
      configMap:
        name: geoip-database

  extraVolumeMounts:
    - name: geoip
      mountPath: /usr/share/geoip
      readOnly: true

# ---------------------------------------------------------------------------
# Status (wird vom Operator befüllt, nicht vom Nutzer)
# Go-Typen: metav1.Condition für alle Conditions (enthält observedGeneration).
# ---------------------------------------------------------------------------
status:
  # observedGeneration: spiegelt die metadata.generation wider, auf der dieser
  # Status basiert. Wenn observedGeneration < metadata.generation, hat der
  # Controller die letzte Spec-Änderung noch nicht verarbeitet.
  observedGeneration: 4

  # phase ist ein ABGELEITETES Feld — strikt aus den Conditions berechnet.
  # Es wird NICHT vom Nutzer gesetzt und enthält keine Zusatzinformation
  # gegenüber den Conditions. Es existiert für UX/kubectl-Lesbarkeit.
  #
  # Berechnungsregeln (Reihenfolge ist signifikant):
  #   "Pending"  wenn Progressing=True AND Ready=False AND VCLSynced=False
  #   "Error"    wenn Ready=False (und nicht Pending)
  #   "Degraded" wenn Ready=True AND (VCLSynced=False OR BackendsAvailable=False)
  #   "Ready"    wenn Ready=True AND VCLSynced=True AND BackendsAvailable=True
  phase: Degraded                       # Pending | Ready | Degraded | Error

  message: "3 replicas ready, VCL active, backend 'app' degraded (2/3 endpoints)"

  # Aktiv geladene VCL-Version
  activeVCL:
    # name ist der varnishd-interne Reload-Name (informational, kein API-Vertrag)
    name: "reload_20260308_142300_00001"
    hash: "sha256:abc123..."            # SHA-256 des generierten VCL-Strings
    loadedAt: "2026-03-08T14:23:01Z"

  # Pod-Status (konsistent mit StatefulSet/Deployment-Konventionen)
  replicas: 3                           # Soll-Replicas (aus spec.replicas)
  readyReplicas: 3                      # Pods mit bestandener Readiness-Probe
  updatedReplicas: 3                    # Pods mit aktueller VCL
  availableReplicas: 3
  # selector wird von der /scale-Subresource (HPA) benötigt
  selector: "vinyl.bluedynamics.eu/cache=my-cache"

  # Backend-Status (aggregiert — keine IP-Liste im Status, da dies etcd-
  # Churn bei jedem Endpoint-Churn erzeugt; Details via Events/Logs)
  backends:
    - name: app
      totalEndpoints: 3
      readyEndpoints: 2
    - name: legacy
      totalEndpoints: 1
      readyEndpoints: 1

  # Cluster-Peers — pro Pod mit VCL-Hash für Drift-Erkennung (B4)
  # VCL-Hashes sind stabile Werte (kein IP-Churn), daher pro-Pod-Granularität sinnvoll.
  # Abweichende Hashes sind sofort via `kubectl get vinylcache -o yaml` sichtbar.
  clusterPeers:
    - podName: "my-cache-0"
      ready: true
      activeVCLHash: "sha256:abc123..."
    - podName: "my-cache-1"
      ready: true
      activeVCLHash: "sha256:abc123..."
    - podName: "my-cache-2"
      ready: true
      activeVCLHash: "sha256:abc123..."
  readyPeers: 3
  totalPeers: 3

  # Conditions (Kubernetes-Standard, Go-Typ: metav1.Condition)
  # Alle Conditions MÜSSEN observedGeneration enthalten.
  conditions:
    - type: Ready
      status: "True"
      observedGeneration: 4
      lastTransitionTime: "2026-03-08T14:23:01Z"
      reason: AllReplicasReady
      message: "3/3 replicas ready"
    - type: VCLSynced
      status: "True"
      observedGeneration: 4
      lastTransitionTime: "2026-03-08T14:23:01Z"
      reason: VCLPushSucceeded
      message: "VCL reload_20260308_142300_00001 active on all pods"
    - type: BackendsAvailable
      status: "False"
      observedGeneration: 4
      lastTransitionTime: "2026-03-08T14:20:00Z"
      reason: EndpointNotReady
      message: "Backend 'app': 2/3 endpoints ready"
    - type: Progressing
      status: "False"
      observedGeneration: 4
      lastTransitionTime: "2026-03-08T14:23:01Z"
      reason: ReconcileComplete
      message: "All pods running current VCL"
    - type: VCLConsistent
      status: "True"
      observedGeneration: 4
      lastTransitionTime: "2026-03-08T14:23:01Z"
      reason: AllPodsConsistent
      message: "All 3 pods running VCL sha256:abc123..."
```

### 2.3 CRD-Felder-Übersicht

| Feld | Typ | Pflicht | Beschreibung |
|------|-----|---------|--------------|
| `spec.replicas` | integer | nein (default: 1) | Anzahl Varnish-Pods |
| `spec.image` | ImageSpec | nein | Varnish-Image (default: operator-gesteuert) |
| `spec.storage[]` | []StorageSpec | ja (min: 1) | Storage-Konfiguration als Array (min. `cache`-Eintrag) |
| `spec.storage[].name` | string | ja | Interner Storage-Name (varnishd `-s <name>=...`) |
| `spec.storage[].type` | string | ja | `malloc` \| `file` |
| `spec.storage[].size` | resource.Quantity | ja | Größe (z.B. `1Gi`, `128Mi`) |
| `spec.backends[]` | []BackendSpec | ja (min: 1) | Backend-Services |
| `spec.backends[].serviceRef` | ServiceRef | ja | Kubernetes-Service-Referenz |
| `spec.backends[].probe` | ProbeSpec | nein | Health-Probe-Konfiguration |
| `spec.director` | DirectorSpec | nein | Backend-Director-Typ |
| `spec.cluster` | ClusterSpec | nein | Cluster-Konfiguration |
| `spec.vcl.cache` | CacheSpec | nein | Strukturiertes Caching-Verhalten |
| `spec.vcl.snippets` | SnippetsSpec | nein | VCL-Snippet-Hooks (max. 64 KB pro Feld) |
| `spec.vcl.fullOverride` | string | nein | Vollständige VCL (Escape-Hatch) |
| `spec.invalidation` | InvalidationSpec | nein | Purge/BAN-Konfiguration |
| `spec.debounce` | DebounceSpec | nein | Debouncing-Konfiguration (metav1.Duration) |
| `spec.retry` | RetrySpec | nein | Retry-Strategie für VCL-Push |
| `spec.pod` | PodSpec | nein | Pod-Template-Konfiguration (Security, Scheduling) |

---

## 3. Komponentenarchitektur

### 3.1 Gesamtbild

```
┌──────────────────────────────────────────────────────────────────────────────┐
│  Kubernetes Cluster                                                           │
│                                                                              │
│  ┌──────────────────────────────────────────────────────────────────────┐    │
│  │  cloud-vinyl-operator (Deployment, cluster-wide)                      │    │
│  │  ┌──────────────┐  ┌──────────────────────────────────────────────┐  │    │
│  │  │  Controller  │  │  Purge/BAN-Proxy  HTTP :8090                 │  │    │
│  │  │  Manager     │  │  Host: my-cache-invalidation.production →     │  │    │
│  │  │              │  │    broadcast zu Varnish-Pods von "my-cache"  │  │    │
│  │  │              │  │  Host: other-cache-invalidation.staging →    │  │    │
│  │  │              │  │    broadcast zu Varnish-Pods von "other-…"   │  │    │
│  │  └──────┬───────┘  └──────────────────────────────────────────────┘  │    │
│  │         │ reconcile + VCL-push              ▲                          │    │
│  └─────────┼───────────────────────────────────┼──────────────────────┘    │
│            │ watches & manages                  │ EndpointSlice → op-pod-IP │
│            ▼                                   │                            │
│  ┌───────────────────────────────────┐  ┌─────┴────────────────────────┐   │
│  │  namespace: production             │  │  namespace: production        │   │
│  │  my-cache StatefulSet              │  │  Service: my-cache-invalidat. │   │
│  │                                   │  │  ClusterIP, Port 8090         │   │
│  │  ┌──────────────┐  ┌────────────┐ │  │  kein Selector (EndpointSlice)│   │
│  │  │  my-cache-0  │  │ my-cache-1 │ │  └──────────────────────────────┘   │
│  │  │ ┌──────────┐ │  │ ┌────────┐ │ │                                     │
│  │  │ │ varnishd │ │  │ │varnishd│ │ │  ┌──────────────────────────────┐   │
│  │  │ │  :8080   │ │  │ │ :8080  │ │ │  │  Service: my-cache-traffic   │   │
│  │  │ └──────────┘ │  │ └────────┘ │ │  │  ClusterIP, Port 80          │   │
│  │  │ ┌──────────┐ │  │ ┌────────┐ │ │  │  Selector: my-cache-*        │   │
│  │  │ │  vinyl-  │◄┼──┼─┤vinyl-  │ │ │  └──────────────────────────────┘   │
│  │  │ │  agent   │ │  │ │agent   │ │ │                                     │
│  │  │ │  :9090   │ │  │ │ :9090  │ │ │  ┌──────────────────────────────┐   │
│  │  │ └──────────┘ │  │ └────────┘ │ │  │  Service: my-cache-headless  │   │
│  │  └──────────────┘  └────────────┘ │  │  (headless, Cluster-Peers)   │   │
│  └───────────────────────────────────┘  └──────────────────────────────┘   │
│            │                         │                                        │
│            ▼                         ▼                                        │
│  ┌────────────────────┐  ┌───────────────────────┐                           │
│  │  Endpoints-API     │  │  Backend-Services      │                           │
│  │  (StatefulSet-Pods)│  │  app-service :8080     │                           │
│  │  my-cache-headless │  │  legacy-service :8080  │                           │
│  └────────────────────┘  └───────────────────────┘                           │
└──────────────────────────────────────────────────────────────────────────────┘
```

### 3.2 Operator (cloud-vinyl-operator)

Der Operator läuft als zentrales Deployment mit einem einzigen Pod (optional mit Leader-Election für HA). Er besteht aus:

**Controller-Manager** (`controller-runtime`-basiert)
- Registriert den `VinylCacheReconciler`
- Führt Reconcile-Loops für alle `VinylCache`-Objekte aus
- Beobachtet `VinylCache`, `StatefulSet`, `Service`, `Endpoints`-Ressourcen (owned oder referenced)
- Hält eine In-Memory-Map der bekannten Pod-IPs und Backend-Endpoints pro `VinylCache`

**VCL-Generator**
- Nimmt `VinylCacheSpec` und aktuelle Endpoint-Listen entgegen
- Erzeugt deterministische VCL
- Berechnet SHA-256-Hash der generierten VCL
- Bei Übereinstimmung mit dem letzten bekannten Hash: kein Push nötig

**Purge/BAN-Proxy**
- Separater HTTP-Server im Operator-Pod (Port 8090)
- Empfängt PURGE/BAN-Requests
- Broadcastet an alle bekannten Varnish-Pod-Endpoints
- Retry mit Backoff
- Prometheus-Metriken

### 3.3 Sidecar-Agent (vinyl-agent)

Der Agent läuft als Sidecar im Varnish-Pod. Er ist bewusst dünn gehalten: **keine Business-Logik, kein Kubernetes-Watch, keine Entscheidungslogik**.

Seine einzige Aufgabe: Den Varnish-Admin-Port (`127.0.0.1:6082`) nach außen als einfache HTTP/gRPC-API zu bridgen, damit der Operator VCL pushen kann, ohne direkten Netzwerkzugang zum Admin-Port zu benötigen.

**Warum kein direkter Admin-Port?**

Der Varnish Admin-Port ist ein Klartextprotokoll mit Secret-basierter Authentifizierung. Das Secret müsste entweder im Operator-Pod liegen (dann kann jeder mit Zugang zum Operator Pod die Admin-Ports aller Varnish-Instanzen kontrollieren) oder pro Pod verwaltet werden. Der Agent löst das Problem auf Netzwerkebene: Der Admin-Port ist nur auf `localhost` gebunden, der Agent exponiert eine REST-API, die innerhalb des Pods läuft. Netzwerksegmentierung erzwingt, dass nur der Agent (als legitimer Gesprächspartner) auf den Admin-Port zugreift.

**Authentifizierung der Agent-API**

Alle Requests an die Agent-API müssen einen Bearer-Token im `Authorization`-Header mitführen:

```
Authorization: Bearer <token>
```

Der Operator generiert das Token (kryptographisch zufällig, 32 Bytes hex) und speichert es als Kubernetes-Secret (`vinyl-agent-<cache-name>` — **ein shared Secret pro VinylCache**, alle Pods des Clusters teilen es). Das Secret wird als Volume direkt in den Agent-Container gemountet (`/run/vinyl/agent-token`). Details zum Token-Flow siehe Abschnitt 9.3.

Zusätzlich schützt eine vom Operator generierte `NetworkPolicy` den Port 9090: Ingress ist ausschließlich vom Operator-Pod erlaubt (Label-Selector).

**Agent-API (HTTP, Port 9090)**

Alle Endpunkte erfordern `Authorization: Bearer <token>`.

```
POST /vcl/push
  Body: { "name": "reload_20260308_...", "vcl": "<VCL-Inhalt>" }
  Response: { "status": "ok" } | { "status": "error", "message": "..." }

POST /ban
  Body: { "expression": "obj.http.X-Url ~ ^/product/" }
  Response: { "status": "ok" } | { "status": "error", "message": "..." }
  (Leitet validierten BAN-Ausdruck über Admin-Protokoll an varnishd weiter)

POST /purge/xkey
  Body: { "keys": ["article-123", "category-news"] }
  Response: { "status": "ok", "purged": 42 }
  (Nur aktiv wenn spec.invalidation.xkey.enabled: true)
  ACHTUNG: xkey.purge() ist eine VCL-Funktion und kann NICHT über den Admin-Port (6082)
  aufgerufen werden — VMOD-Funktionen sind nur im Kontext einer HTTP-Transaktion aufrufbar.
  Der Agent sendet stattdessen einen internen HTTP-PURGE an varnishd auf localhost:8080:
    PURGE / HTTP/1.1 | Host: localhost | X-Xkey-Purge: article-123
  Die generierte VCL enthält dafür einen dedizierten Handler in vcl_recv (siehe 6.4).

POST /vcl/validate
  Body: { "vcl": "<VCL-Inhalt>" }
  Response: { "status": "ok" } | { "status": "error", "message": "...", "line": 42 }
  (vcl.load ohne vcl.use — Dry-Run-Validierung ohne Aktivierung)

GET /vcl/active
  Response: { "name": "reload_20260308_...", "status": "active" }

GET /health
  Response: { "status": "ok", "varnish": "running" }

GET /metrics  (Prometheus, kein Auth — nur cluster-interne Erreichbarkeit)
```

**Agent-Implementierung**

Der Agent ist ein simples Go-Binary (~300 LOC), das:
1. Beim Start wartet, bis `varnishd` auf dem Admin-Port erreichbar ist (Polling mit Backoff)
2. Einen HTTP-Server startet
3. Auf `/vcl/push`-Requests die VCL via Admin-Protokoll an `varnishd` übergibt
4. Den Admin-Client (`go-varnish-client`) für die eigentliche Kommunikation nutzt

**Startup-Sequenz im Pod**

```
Pod startet
  │
  ├── varnishd startet (mit Initial-VCL, generiert von Init-Container oder Operator)
  │     └── Admin-Port auf 127.0.0.1:6082
  │
  └── vinyl-agent startet
        ├── Wartet auf 127.0.0.1:6082 (Polling)
        ├── Authentifiziert sich
        └── HTTP-Server auf :9090 bereit
              └── Operator kann VCL pushen
```

**Init-Container und Placeholder-VCL**

Ein Init-Container (Image: `vinyl-init`) schreibt eine minimale Placeholder-VCL in ein shared `emptyDir`-Volume, damit `varnishd` starten kann, bevor der Operator die erste echte VCL pusht.

Die Placeholder-VCL ist eine gültige minimale VCL, die alle Requests mit `503 Service Unavailable` beantwortet und eine klare Meldung zurückgibt:

```vcl
vcl 4.1;
// cloud-vinyl bootstrap VCL — wird durch Operator-Push ersetzt
sub vcl_recv { return(synth(503, "Cache initializing")); }
sub vcl_synth {
    if (resp.status == 503) {
        set resp.http.Retry-After = "5";
        return(deliver);
    }
}
```

Dies ist der definierte Zustand während der Startphase. Der Operator pusht die echte VCL sobald der Agent `/health` meldet. Die Readiness-Probe des Pods meldet erst "ready", wenn diese erste echte VCL aktiv ist (siehe unten).

```
initContainers:
  - name: vinyl-init
    # Tag wird vom Operator auf den eigenen Release-Tag gesetzt — kein mutable :latest
    image: ghcr.io/bluedynamics/cloud-vinyl-init:0.1.0
    command: ["/vinyl-init"]
    volumeMounts:
      - name: vcl-bootstrap
        mountPath: /etc/varnish/bootstrap
      - name: tmp
        mountPath: /tmp
      - name: varnish-lib
        mountPath: /var/lib/varnish
```

**Agent-Auth-Token — vereinfachter Flow:**

Der Operator generiert das Bearer-Token, speichert es als Kubernetes-Secret (`vinyl-agent-<cache-name>` — **ein shared Secret pro VinylCache**, alle Pods teilen es) und mountet es als Secret-Volume direkt in den Agent-Container. Kein Init-Container-Hop nötig:

```yaml
containers:
  - name: vinyl-agent
    volumeMounts:
      - name: agent-token
        mountPath: /run/vinyl
        readOnly: true
volumes:
  - name: agent-token
    secret:
      secretName: vinyl-agent-my-cache    # Ein shared Secret pro VinylCache (alle Pods)
      items:
        - key: token
          path: agent-token
```

Die vom Operator generierten `emptyDir`-Volumes im StatefulSet:
```yaml
volumes:
  - name: vcl-bootstrap
    emptyDir: {}
  - name: tmp
    emptyDir: {}
  - name: varnish-lib
    emptyDir:
      medium: Memory    # exec-fähig (VSM-Anforderung), Kernel managed Paging
      sizeLimit: 256Mi  # Verhindert Disk-Vollläufe durch VCL-Compile-Cache-Akkumulation
```

Der varnishstat-Exporter-Sidecar muss ebenfalls `/var/lib/varnish` mounten (VSM-Dateien):
```yaml
  - name: vinyl-exporter
    volumeMounts:
      - name: varnish-lib
        mountPath: /var/lib/varnish
        readOnly: true
```

**Readiness-Probe und Liveness-Probe:**

Die Probes prüfen den vinyl-agent `/health`-Endpunkt. **`/health` erfordert keine Authentifizierung** — Health-Checks enthalten keine sensitiven Daten, und ein Auth-Token in der Pod-Spec wäre via `kubectl get pod -o yaml` für jeden mit Pod-Read-Rechten sichtbar. Nur die mutativen Endpunkte (`/vcl/push`, `/ban`, `/purge/xkey`, `/vcl/validate`) erfordern Bearer-Token-Auth.

Der Agent gibt `ready: true` erst zurück, wenn varnishd erreichbar ist UND die aktive VCL nicht mehr die Placeholder-VCL ist:

```yaml
readinessProbe:
  httpGet:
    path: /health
    port: 9090
  initialDelaySeconds: 5
  periodSeconds: 5
  failureThreshold: 6

livenessProbe:
  httpGet:
    path: /health
    port: 9090
  initialDelaySeconds: 10
  periodSeconds: 10
  failureThreshold: 3
```

**Graceful Shutdown — preStop-Hook (K3-Fix):**

Varnish hat keinen eingebauten Graceful-Drain-Mechanismus — varnishd stirbt bei SIGTERM sofort. Ohne preStop-Hook entsteht eine Race Condition: Kubernetes sendet SIGTERM und beginnt gleichzeitig, den Pod aus den Endpoints zu entfernen. Clients, die den Pod noch in ihrem Routing haben, bekommen Connection-Resets.

```yaml
lifecycle:
  preStop:
    exec:
      command: ["sleep", "5"]

# terminationGracePeriodSeconds muss >= preStop-Sleep + max. Request-Dauer sein
terminationGracePeriodSeconds: 30
```

Die 5 Sekunden geben dem Endpoints-Controller und kube-proxy/iptables Zeit, den Pod aus dem Routing zu entfernen, bevor varnishd terminiert. Ohne diesen Hook produziert jedes Rolling Update, jeder Scale-Down und jede Node-Drain 5xx-Fehler für in-flight Requests.

Ein Pod ist erst "ready" (und damit im Cluster-Peer-Pool des Operators) wenn der Operator die erste echte VCL erfolgreich gepusht hat. Das stellt sicher, dass kein Pod in den Shard-Director aufgenommen wird, bevor er die korrekte VCL ausführt.

### 3.4 Kommunikationsfluss bei VCL-Update

```
Operator                    vinyl-agent (Pod N)         varnishd (Pod N)
   │                              │                          │
   │  POST /vcl/push              │                          │
   ├─────────────────────────────►│                          │
   │                              │                          │
   │                              │  vcl.load <name> -       │
   │                              ├─────────────────────────►│
   │                              │  vcl.use <name>          │
   │                              ├─────────────────────────►│
   │                              │  vcl.state <old> cold    │
   │                              ├─────────────────────────►│
   │                              │  vcl.discard <oldest>    │ (wenn > max_vcl_versions)
   │                              ├─────────────────────────►│
   │                              │  200 OK                  │
   │                              │◄─────────────────────────┤
   │  200 OK                      │                          │
   │◄─────────────────────────────┤                          │
   │                              │                          │
   │  (für jeden weiteren Pod)    │                          │
   │  POST /vcl/push              │                          │
   ├──────────────────────────────────────────────────────── ►
```

Alle Pod-Updates laufen parallel (Goroutinen pro Pod). Der Operator aggregiert Ergebnisse und schreibt den Gesamtstatus in `VinylCache.status`.

**VCL Garbage Collection (H2-Fix):** Nach `vcl.use` setzt der Agent die vorherige VCL auf `cold`. Er hält maximal `max_vcl_versions` (Default: 3) VCL-Versionen im `cold`-Zustand als Rollback-Reserve.

`vcl.discard` darf **nicht unmittelbar nach `vcl.use`** aufgerufen werden. Worker-Threads halten noch Referenzen auf die vorherige VCL (sie geben sie erst am Beginn der nächsten Transaktion frei). Schnelle load/use/discard-Zyklen (z.B. bei Endpoint-Churn mit 5s Debounce) können den Shard-Director in einen inkonsistenten Zustand bringen und 503-Fehler ("Director returned no backends") verursachen.

**Discard-Logik des Agents:**
1. Nach `vcl.use`: Warte mindestens 5–10 Sekunden (konfigurierbar als Agent-Flag)
2. Frage `vcl.list` ab — überspringe alle VCLs im `busy`- oder `cooling`-State
3. Lösche nur VCLs im `cold`-State, die die `max_vcl_versions`-Grenze überschreiten
4. Metrik: `vinyl_vcl_versions_loaded` (Gauge pro Pod) für Alert bei VCL-Akkumulation

`max_vcl_versions` ist kein CRD-Feld — der Agent verwendet einen festen Default (konfigurierbar als Agent-Flag `--max-vcl-versions`).

---

## 4. VCL-Generierung

### 4.1 Generierungs-Pipeline

```
VinylCacheSpec
      │
      ▼
┌─────────────────────────────────────────┐
│  VCL-Generator                          │
│                                         │
│  1. Backend-Definitionen rendern        │
│     (aus spec.backends + Endpoints)     │
│                                         │
│  2. Director-Setup rendern              │
│     (vcl_init)                          │
│                                         │
│  3. Cluster-Peer-Definitionen rendern   │
│     (wenn spec.cluster.enabled)         │
│                                         │
│  4. vcl_recv zusammenbauen:             │
│     a. Cluster-Routing (wenn enabled)   │
│     b. Cache-Bypass-Regeln              │
│     c. Director-Zuweisung              │
│     d. Snippet: vclRecv                 │
│                                         │
│  5. Weitere Subroutinen zusammenbauen:  │
│     generated code + snippet           │
│                                         │
│  6. SHA-256 berechnen                   │
└─────────────────────────────────────────┘
      │
      ▼
  VCL-String
```

### 4.2 Generiertes VCL-Beispiel

Für das obige Beispiel-YAML mit 2 Backends, shard-Director, Clustering und Snippets würde der Operator folgende VCL generieren:

```vcl
vcl 4.1;
// GENERATED BY cloud-vinyl-operator v0.1.0
// VinylCache: production/my-cache
// Hash: sha256:abc123...
// DO NOT EDIT MANUALLY

import std;
import directors;
import purge;       // Für Soft-Purge (spec.invalidation.purge.soft: true)
// snippet:header - BEGIN
import geoip2;
// snippet:header - END

// ---------------------------------------------------------------------------
// Cluster-Peers (Varnish-Pods)
// ---------------------------------------------------------------------------
backend peer_my_cache_0 {
    .host = "10.0.2.1";
    .port = "8080";
}
backend peer_my_cache_1 {
    .host = "10.0.2.2";
    .port = "8080";
}
backend peer_my_cache_2 {
    .host = "10.0.2.3";
    .port = "8080";
}

// ---------------------------------------------------------------------------
// Backends
// ---------------------------------------------------------------------------
backend app_10_0_1_1 {
    .host = "10.0.1.1";
    .port = "8080";
    .connect_timeout = 1s;
    .first_byte_timeout = 60s;
    .between_bytes_timeout = 60s;
    .max_connections = 200;
    .probe = {
        .url = "/healthz";
        .interval = 5s;
        .timeout = 2s;
        .threshold = 3;
        .window = 5;
    }
}
backend app_10_0_1_2 {
    .host = "10.0.1.2";
    .port = "8080";
    .connect_timeout = 1s;
    .first_byte_timeout = 60s;
    .between_bytes_timeout = 60s;
    .max_connections = 200;
    .probe = {
        .url = "/healthz";
        .interval = 5s;
        .timeout = 2s;
        .threshold = 3;
        .window = 5;
    }
}
backend legacy_10_0_1_10 {
    .host = "10.0.1.10";
    .port = "8080";
    .connect_timeout = 2s;
    .first_byte_timeout = 120s;
    .between_bytes_timeout = 60s;
    .max_connections = 50;
    .probe = {
        .url = "/ping";
        .interval = 10s;
        .timeout = 3s;
        .threshold = 2;
        .window = 3;
    }
}

// ---------------------------------------------------------------------------
// ACL: Erlaubte Purge/BAN-Quellen
// Nur die Operator-Pod-IP — wird vom Operator automatisch eingetragen.
// Zusätzliche Quellen aus spec.invalidation.purge.allowedSources folgen.
// ---------------------------------------------------------------------------
acl vinyl_purge_allowed {
    "10.0.3.5";   // Operator-Pod-IP (automatisch eingetragen)
}

// ---------------------------------------------------------------------------
// ACL: Bekannte Cluster-Peers (schützt X-Vinyl-Shard gegen Spoofing)
// Wird vom Operator mit aktuellen Pod-IPs befüllt.
// ---------------------------------------------------------------------------
acl vinyl_cluster_peers {
    "10.0.2.1";   // my-cache-0
    "10.0.2.2";   // my-cache-1
    "10.0.2.3";   // my-cache-2
}

sub vcl_init {
    // Cluster-Director (Shard über Peers)
    new vinyl_cluster = directors.shard();
    vinyl_cluster.add_backend(peer_my_cache_0);
    vinyl_cluster.add_backend(peer_my_cache_1);
    vinyl_cluster.add_backend(peer_my_cache_2);
    vinyl_cluster.reconfigure();

    // Backend-Director (Shard über App-Backends)
    new vinyl_app = directors.shard();
    vinyl_app.add_backend(app_10_0_1_1);
    vinyl_app.add_backend(app_10_0_1_2);
    vinyl_app.reconfigure();

    // Backend-Director (Fallback für Legacy)
    new vinyl_legacy = directors.fallback();
    vinyl_legacy.add_backend(legacy_10_0_1_10);

    // snippet:vclInit - BEGIN
    new geo = geoip2.open("/usr/share/geoip/GeoLite2-Country.mmdb");
    // snippet:vclInit - END
}

sub vcl_recv {
    // Security-Hardening: interne und sensitive Header vom Client entfernen
    unset req.http.proxy;               // Httpoxy-Mitigation (CVE-2016-5385)
    unset req.http.X-Varnish-Internal;  // Interne Header dürfen nicht vom Client gesetzt werden

    // Normalisierung: Host-Header und Query-String
    // Host lowercasen, Port strippen: "Example.com:443" → "example.com"
    // (verschiedene Schreibweisen erzeugen sonst verschiedene Cache-Keys)
    set req.http.Host = std.tolower(regsub(req.http.Host, ":[0-9]+$", ""));
    // Query-String sortieren: "?b=2&a=1" und "?a=1&b=2" sind identisch
    set req.url = std.querysort(req.url);

    // Accept-Encoding-Normalisierung: Cache-Fragmentierung verhindern
    if (req.http.Accept-Encoding) {
        if (req.http.Accept-Encoding ~ "gzip") {
            set req.http.Accept-Encoding = "gzip";
        } else {
            unset req.http.Accept-Encoding;
        }
    }

    // WebSocket-Passthrough (nur wenn spec.vcl.cache.websocketPassthrough: true)
    // if (req.http.Upgrade ~ "(?i)websocket") { return(pipe); }

    // Cluster-Routing: Unrouted Requests werden an den zuständigen Pod delegiert.
    // X-Vinyl-Shard verhindert Routing-Schleifen (request bereits delegiert).
    // Hinweis: req.backend_hint ist ein BACKEND-Typ (kein String), kann nicht direkt
    // mit server.identity (String) verglichen werden — daher Header-Pattern statt
    // Namensvergleich. Das ermöglicht identische VCL auf allen Pods.
    //
    // Spoofing-Schutz: Extern gesetzte X-Vinyl-Shard-Header strippen.
    // Nur bekannte Cluster-Peer-IPs dürfen diesen Header legitimerweise setzen.
    if (req.http.X-Vinyl-Shard && !(client.ip ~ vinyl_cluster_peers)) {
        unset req.http.X-Vinyl-Shard;
    }
    if (!req.http.X-Vinyl-Shard) {
        set req.http.X-Vinyl-Shard = "1";
        set req.backend_hint = vinyl_cluster.backend(by=URL);
        return(pass);  // An zuständigen Peer weiterleiten (ggf. auch dieser Pod)
    }
    // Bereits geroutet — Cache-Lookup auf diesem Pod
    unset req.http.X-Vinyl-Shard;

    // Purge-Behandlung
    if (req.method == "PURGE") {
        if (!client.ip ~ vinyl_purge_allowed) {
            return(synth(405, "PURGE not allowed"));
        }
        return(purge);
    }

    // BAN via HTTP-Methode ist deaktiviert (Injection-Risiko durch req.url-Konkatenation).
    // BAN-Requests müssen über den Invalidierungs-REST-Endpunkt gesendet werden:
    //   POST /ban an my-cache-invalidation.production.svc.cluster.local:8090
    if (req.method == "BAN") {
        return(synth(405, "BAN method not supported. Use the invalidation REST endpoint."));
    }

    // Cache-Bypass: Cookies
    if (req.http.Cookie) {
        if (req.http.Cookie ~ "SESS[0-9a-f]+" ||
            req.http.Cookie ~ "wordpress_logged_in_" ||
            req.http.Cookie ~ "wp-settings-") {
            return(pass);
        }
    }

    // Cache-Bypass: Pfade
    if (req.url ~ "^/admin" ||
        req.url ~ "^/wp-admin" ||
        req.url ~ "^/user/login") {
        return(pass);
    }

    // Backend-Hint setzen
    set req.backend_hint = vinyl_app.backend(by=URL);

    // snippet:vclRecv - BEGIN
    if (req.http.Cookie) {
        set req.http.Cookie = regsuball(req.http.Cookie,
            "(^|;\s*)(_ga|_gid|_gat)[^;]*", "");
    }
    if (req.http.Cookie == "") {
        unset req.http.Cookie;
    }
    // snippet:vclRecv - END
}

sub vcl_hash {
    hash_data(req.url);
    if (req.http.host) {
        hash_data(req.http.host);
    } else {
        hash_data(server.ip);
    }
    // snippet:vclHash - BEGIN
    hash_data(geoip2.lookup_str(client.ip));
    // snippet:vclHash - END
}

sub vcl_hit {
    // Soft-Purge (nur wenn spec.invalidation.purge.soft: true)
    // Muss in vcl_hit UND vcl_miss aufgerufen werden, um alle Vary-Varianten zu treffen.
    // purge.soft(ttl, grace, keep): TTL=0, Grace/Keep bleiben erhalten.
    if (req.method == "PURGE") {
        purge.soft(0s, -1s, -1s);
        return(synth(200, "Soft-purged"));
    }
    // snippet:vclHit - (empty)
}

sub vcl_miss {
    // Soft-Purge auch in vcl_miss: PURGE auf nicht-gecachtes Objekt (Variant fehlt im Cache)
    if (req.method == "PURGE") {
        purge.soft(0s, -1s, -1s);
        return(synth(200, "Soft-purged (miss)"));
    }
    // snippet:vclMiss - (empty)
}

sub vcl_pass {
    // snippet:vclPass - (empty)
}

sub vcl_backend_fetch {
    // snippet:vclBackendFetch - BEGIN
    set bereq.http.X-Varnish-Node = server.identity;
    // snippet:vclBackendFetch - END
    // HINWEIS: X-Varnish-Node nur gesetzt wenn spec.vcl.debug.responseHeaders: true
}

sub vcl_backend_response {
    // TTL-Logik
    if (beresp.ttl <= 0s || beresp.http.Set-Cookie || beresp.http.Surrogate-control ~ "no-store" ||
        (!beresp.http.Surrogate-Control && beresp.http.Cache-Control ~ "no-cache|no-store|private")) {
        set beresp.uncacheable = true;
        set beresp.ttl = 120s;
    } else if (beresp.ttl < 1s) {
        set beresp.ttl = 120s;
    }
    set beresp.grace = 24h;

    // Lurker-friendly BAN-Support: URL und Host in Objekt-Header speichern.
    // Ban-Lurker kann req.* nicht verarbeiten (kein aktiver Request-Kontext).
    // Ban-Expressions müssen obj.http.* verwenden — daher URL hier kopieren.
    // Der Generator emittiert entsprechende Ban-Expressions wie:
    //   std.ban("obj.http.x-url ~ ^/product/ && obj.http.x-host == example.com")
    // In vcl_deliver werden diese Header vor dem Client gestripped.
    set beresp.http.x-url = bereq.url;
    set beresp.http.x-host = bereq.http.host;

    // snippet:vclBackendResponse - BEGIN
    if (beresp.uncacheable) {
        set beresp.ttl = 30s;
        set beresp.grace = 0s;
    }
    // snippet:vclBackendResponse - END
}

sub vcl_deliver {
    // Interne BAN-Support-Header entfernen (nie zum Client leaken)
    unset resp.http.x-url;
    unset resp.http.x-host;

    // X-Cache-Header nur wenn spec.vcl.debug.responseHeaders: true gesetzt
    // (verrät interne Topologie; per Default deaktiviert)
    // snippet:vclDeliver - BEGIN
    unset resp.http.X-Powered-By;
    // snippet:vclDeliver - END
}

sub vcl_pipe {
    // snippet:vclPipe - (empty)
}

sub vcl_purge {
    // snippet:vclPurge - (empty)
    // Standard-Verhalten: synth(200) nach Purge
}

sub vcl_synth {
    // Minimale generierte vcl_synth: strukturierte Fehlerresponse statt Varnish-Default-HTML.
    if (resp.status == 301 || resp.status == 302) {
        set resp.http.Location = resp.reason;
        return(deliver);
    }
    set resp.http.Content-Type = "application/json; charset=utf-8";
    synthetic({"{"status": "} + resp.status + {", "message": ""} + resp.reason + {""}}"});
    // snippet:vclSynth - BEGIN
    // snippet:vclSynth - END
    return(deliver);
}

sub vcl_backend_error {
    // snippet:vclBackendError - (empty)
}

sub vcl_fini {
    // snippet:vclFini - (empty)
    // Wird beim VCL-Discard aufgerufen — für VMOD-Ressourcen-Cleanup
    return(ok);
}
```

### 4.3 Snippet-Injection-Punkte

| Hook | Position im generierten Code | Typischer Anwendungsfall |
|------|------------------------------|--------------------------|
| `header` | Datei-Anfang (nach Kommentar-Block) | `import`-Statements, eigene VCL-Sub-Deklarationen (z.B. `sub detect_auth { ... }`) |
| `vclInit` | Ende von `vcl_init`, nach Director-Setup | VMOD-Initialisierung |
| `vclRecv` | Ende von `vcl_recv`, vor implizitem `return(hash)` | Cookie-Normalisierung, URL-basiertes Backend-Routing, A/B-Testing |
| `vclHash` | Ende von `vcl_hash` | Zusätzliche Hash-Dimensionen (Sprache, Geo) |
| `vclHit` | Ende von `vcl_hit` | Custom-Hit-Logik |
| `vclMiss` | Ende von `vcl_miss` | Custom-Miss-Logik |
| `vclPass` | Ende von `vcl_pass` | Pass-spezifische Header |
| `vclBackendFetch` | Ende von `vcl_backend_fetch` | Backend-Request-Modifikation |
| `vclBackendResponse` | Ende von `vcl_backend_response`, nach TTL-Logik | Custom-TTL-Overrides, applikationsspezifische Cache-Logik |
| `vclDeliver` | Ende von `vcl_deliver` | Response-Header-Manipulation |
| `vclPipe` | Ende von `vcl_pipe` | Pipe-spezifische Logik |
| `vclPurge` | Ende von `vcl_purge` | Custom-Synth-Response nach Purge |
| `vclSynth` | Ende von `vcl_synth` | Custom-Fehlerseiten, Redirect-Handling |
| `vclBackendError` | Ende von `vcl_backend_error` | Backend-Fehlerbehandlung |
| `vclFini` | Ende von `vcl_fini` | Cleanup von VMOD-Ressourcen beim VCL-Discard |

### 4.4 Director-Naming und URL-basiertes Backend-Routing

Der VCL-Generator erzeugt für jedes Backend in `spec.backends[]` einen Director mit einem deterministischen Namen. Das Schema:

```
vinyl_<backend-name>
```

Beispiel: Ein Backend mit `name: frontend` erzeugt den Director `vinyl_frontend`. Ein Backend mit `name: api_backend` erzeugt `vinyl_api_backend`.

Diese Namen sind relevant für fortgeschrittene `vclRecv`-Snippets, die das URL-basierte Backend-Routing überschreiben — ein häufiger Anwendungsfall bei Applikationen mit mehreren Backends für unterschiedliche URL-Präfixe.

**Beispiel: URL-basiertes Routing auf zwei Backends**

```yaml
spec:
  backends:
    - name: frontend
      serviceRef: { name: plone-frontend, port: 3000 }
    - name: api
      serviceRef: { name: plone-backend, port: 8080 }
  vcl:
    snippets:
      # Eigene Sub-Deklarationen für Request-Klassifizierung
      header: |
        sub detect_api_request {
          unset req.http.x-route;
          if (req.url ~ "^/\+\+api\+\+" || req.url ~ "^/@@(images|download)/") {
            set req.http.x-route = "api";
          } else {
            set req.http.x-route = "frontend";
          }
        }

      # URL-basiertes Routing überschreibt den vom Generator gesetzten backend_hint
      vclRecv: |
        call detect_api_request;
        if (req.http.x-route == "api") {
          set req.backend_hint = vinyl_api.backend(by=URL);
          # Plone-spezifisch: VirtualHostBase-URL-Rewrite
          set req.http.x-vcl-proto = "http";
          if (req.http.X-Forwarded-Proto) {
            set req.http.x-vcl-proto = req.http.X-Forwarded-Proto;
          }
          set req.url = "/VirtualHostBase/" + req.http.x-vcl-proto + "/"
            + req.http.host + "/Plone/VirtualHostRoot" + req.url;
        } else {
          set req.backend_hint = vinyl_frontend.backend(by=URL);
        }
```

**Hinweis:** Wenn `spec.director.type` gesetzt ist und der `vclRecv`-Snippet `req.backend_hint` überschreibt, wird die generierte Backend-Hint-Zeile effektiv deaktiviert. Der Generator setzt den Hint vor dem Snippet — der Snippet gewinnt immer.

### 4.5 PURGE-Semantik: native PURGE vs. BAN-basiertes PURGE

Der Operator generiert für PURGE-Requests natives `return(purge)` — das invalidiert nur den spezifischen Cache-Eintrag (Hash-Key = URL + Host).

Applikationen, die URL-Pattern-basierte Invalidierung benötigen (z.B. "alle gecachten Objekte für `/product/*`"), müssen den **BAN-REST-Endpunkt** des Invalidierungs-Proxy verwenden:

```http
POST /ban HTTP/1.1
Host: my-cache-invalidation.production.svc.cluster.local
Content-Type: application/json

{"expression": "obj.http.url ~ ^/product/"}
```

Dies ist sicherer als VCL-seitiges `ban("req.url == " + req.url)` (Injection-Risiko) und ermöglicht ausdrucksstärkere Expressions.

**Grace-Interaktion mit Soft-Purge**

| Szenario | Verhalten |
|----------|-----------|
| `soft: true` + `defaultGrace: 24h` | Soft-Purge setzt TTL=0, Grace bleibt. Das erste Request nach Purge bekommt sofort die stale Response (kein Warten), async Backend-Fetch im Hintergrund. Stale Content kann noch bis zu 24h ausgeliefert werden. |
| `soft: false` (Hard-Purge) | Objekt wird sofort entfernt. Nächster Request wartet synchron auf Backend-Fetch. Bei hoher Last: Thundering-Herd oder Request-Coalescing-Queue. Notwendig für regulatorische Anforderungen ("Inhalt MUSS sofort weg"). |
| Backend-Down + `defaultGrace: 24h` | Site überlebt einen 24h-Backend-Ausfall mit stale Content. Dies ist das Hauptargument für hohe Grace-Werte. |
| `xkey.softpurge()` + Grace | Identisches Verhalten wie Soft-Purge, aber für alle Objekte mit einem bestimmten Tag. |
| Soft-Purge auf abgelaufene Grace | Soft-Purge kann TTL nur *reduzieren*, nie verlängern. Wenn Grace bereits abgelaufen ist, wirkt Soft-Purge wie Hard-Purge — aber lautlos. Deshalb `defaultGrace` großzügig setzen. |

Für Inhalte die "sofort weg müssen" (DSGVO-Anforderungen, Datenlöschanfragen): `spec.invalidation.purge.soft: false` oder pro-Request Hard-Purge via separatem Invalidierungs-Endpunkt.

### 4.6 Full-Override-Modus

Wenn `spec.vcl.fullOverride` oder `spec.vcl.fullOverrideRef` gesetzt ist, überspringt der Generator die gesamte VCL-Generierung. Der Operator injiziert ausschließlich einen Kommentar-Block am Anfang der VCL, der die bekannten Backend-Endpoints dokumentiert:

```vcl
// CLOUD-VINYL MANAGED ENDPOINTS (informational only)
// Backend: app -> 10.0.1.1:8080, 10.0.1.2:8080
// Cluster peers: 10.0.2.1:8080, 10.0.2.2:8080, 10.0.2.3:8080
// Generated: 2026-03-08T14:23:01Z
```

Das Operator-Status-Tracking (welche VCL aktiv ist, wann gepusht) funktioniert auch im Full-Override-Modus vollständig.

### 4.7 VCL-Generator Qualitätsanforderungen

Der VCL-Generator muss folgende Invarianten garantieren. Diese Bugs treten in Produktion am häufigsten auf und sind zur Generierungszeit verhinderbar.

**Kritische Bugs (Generator MUSS verhindern)**

| # | Bug | Konsequenz | Maßnahme |
|---|-----|-----------|----------|
| 1 | `beresp.ttl = 0s` | Request-Serialisierung durch Coalescing | Niemals generieren; für unkachierbare Inhalte: `beresp.uncacheable = true; beresp.ttl = 120s;` |
| 2 | `beresp.http.Cache-Control` als TTL-Mechanismus | Header-Manipulation hat keinen Effekt — TTL wird vor `vcl_backend_response` berechnet | Immer `beresp.ttl` direkt setzen |
| 3 | Fehlende `return()` am Ende einer Subroutine | Fall-Through in Built-in-VCL: Built-in `vcl_recv` bypassed Cache für alle Requests mit Cookies | Jede generierte Subroutine mit explizitem `return()` abschließen |
| 4 | `req.*` in Ban-Expressions | Ban-Lurker kann diese nicht verarbeiten; Bans akkumulieren → O(n×m) CPU | Nur `obj.http.*` in Bans; URL und Host in `vcl_backend_response` nach `beresp.http.x-url`/`x-host` kopieren |
| 5 | Unguarded `return(restart)` | Restart-Loop bis `max_restarts` (default 4), dann 503 | Immer `if (req.restarts < N)` Guard generieren |
| 6 | `Vary: User-Agent` ohne Normalisierung | ~8.000 Cache-Varianten pro URL (reale Messung) — Cache effektiv deaktiviert | User-Agent auf Kategorien normalisieren (`"mobile"`, `"desktop"`, `"bot"`) |
| 7 | `ban()` statt `std.ban()` | Deprecated seit Varnish 6.6, wird in 7.x entfernt | Immer `std.ban()` + `std.ban_error()` generieren |
| 8 | `regsub()` ohne Match-Guard | Bei keinem Match gibt `regsub()` den gesamten Input zurück — Data-Leak (z.B. ganzer Cookie-Header) | `if (var ~ "pattern") { regsub(...) }` statt nacktem `regsub()` |

**Sicherheits-Bugs (Generator MUSS abfangen)**

| # | Bug | Konsequenz | Maßnahme |
|---|-----|-----------|----------|
| 1 | Fehlendes `unset req.http.Proxy` | Httpoxy-Vulnerability (CVE-2016-5385): Client kann Backend-Verbindungen hijacken | Immer am Anfang von `vcl_recv` generieren ✓ |
| 2 | PURGE ohne ACL | Jeder kann den Cache invalidieren | ACL-Enforcement generieren; Operator-Pod-IP automatisch eintragen ✓ |
| 3 | `Set-Cookie` in gecachter Response | Session-Transfer: ein Client bekommt das `Set-Cookie` eines anderen | `Set-Cookie` in `vcl_backend_response` strippen oder `return(pass)` ✓ |
| 4 | Client-Header als Trust-Basis | Cache-Poisoning: `req.http.X-Internal == "true"` ist fälschbar | Alle internen Header am Anfang von `vcl_recv` strippen ✓ |
| 5 | XFF Append statt Overwrite | XFF-Spoofing: Client kann beliebige IPs in die Kette injizieren | XFF mit `client.ip` überschreiben (erster Trusted Proxy) |
| 6 | ACL mit Hostnamen | Nicht auflösbarer Hostname matcht *alles*; mit Negation wird alles abgelehnt | IP-Adressen in ACLs bevorzugen; bei Hostnamen warnen |

**Strukturelle Bugs (Generator SOLLTE verhindern)**

| # | Bug | Konsequenz | Maßnahme |
|---|-----|-----------|----------|
| 1 | Kein Grace konfiguriert | Kein stale-while-revalidate; jeder Cache-Miss wartet synchron | Immer explizite `beresp.grace` generieren ✓ |
| 2 | Host-Header nicht normalisiert | `example.com` vs `Example.com:443` → verschiedene Cache-Keys | Host lowercasen, Port strippen ✓ |
| 3 | Query-String nicht sortiert | `?a=1&b=2` vs `?b=2&a=1` → verschiedene Cache-Keys | `std.querysort(req.url)` generieren ✓ |
| 4 | `return(pipe)` für HTTP-Traffic | Kein Logging, kein Header-Manipulation, Backend-Selection frozen | Nur für WebSocket-Upgrades; sonst `return(pass)` |
| 5 | Backend Idle Timeout Race | Varnish Default 60s, viele Backends Default 5s → Connection-Reset, 503 | `idleTimeout` kleiner als Backend-Keep-Alive → CRD-Feld `connectionParameters.idleTimeout` ✓ |
| 6 | Interne Header nicht gestripped | `x-url`, `x-host`, BAN-Support-Header leaken zum Client | In `vcl_deliver` alle internen Header entfernen ✓ |

**Varnish 7.x Migrations-Fallen**

| Änderung | Generator-Maßnahme |
|----------|---------------------|
| PCRE → PCRE2 | `pcre2_match_limit` statt `pcre_match_limit` verwenden |
| Zahlenformat RFC8941 | Max 15 Digits, max 3 Dezimalstellen, keine Scientific Notation |
| ACL Pedantic Mode (ON by Default) | ACLs mit korrekter Netzmaske generieren |
| `std.rollback()` in `vcl_pipe` | Niemals `rollback()` in Pipe generieren (VCL-Failure) |

**VCL-Validierung vor Push**

Der Operator validiert die generierte VCL mit `varnishd -Cf /dev/stdin` bevor sie an die Pods gepusht wird. Das fängt Syntax-Fehler in User-Snippets ab, bevor sie einen Outage verursachen. Der Agent-API-Endpunkt `POST /vcl/validate` führt denselben Check als Dry-Run durch (`vcl.load` ohne `vcl.use`). Damit wird sichergestellt, dass selbst fehlerhafter User-Snippet-Code die aktive VCL nicht korrumpiert.

---

## 5. Clustering-Strategie

### 5.1 Problem & Lösung

Der Sidecar-Ansatz erzeugt ein Chicken-and-Egg-Problem beim Clustering: Varnish-Pod A muss seinen eigenen Endpoint sehen, damit der Controller im Sidecar die korrekte Peer-Liste generieren kann — aber der Pod ist erst ready, wenn Varnish läuft.

cloud-vinyl löst das durch den zentralen Operator: Der Operator kennt alle Pod-IPs (aus dem StatefulSet und dem headless Service), unabhängig davon, ob die Pods selbst ready sind. Die VCL wird mit der vollständigen Peer-Liste generiert und erst dann gepusht, wenn alle Pods bereit sind, die neue VCL zu empfangen.

**Single-Replica-Optimierung (M5):** Bei `spec.replicas: 1` oder `spec.cluster.enabled: false` lässt der VCL-Generator den gesamten Cluster-Routing-Block (X-Vinyl-Shard, vinyl_cluster-Director, Peer-ACL) weg. Der `return(pass)` an localhost wäre reiner Overhead — ein HTTP-Roundtrip ohne Mehrwert. Ohne diesen Block funktioniert caching direkt.

### 5.2 Shard-Director

Der Shard-Director (`directors.shard()` aus `vmod_directors`) ist der empfohlene Director für Varnish-Clustering. Im Gegensatz zum Hash-Director:

- Konsistentes Hashing (Ketama-Style, SHA256, 67 Replicas pro Backend): Bei Hinzufügen/Entfernen eines Pods werden nur ~1/N der Keys neu geroutet — nicht der gesamte Cache
- Effektive Cache-Größe = Summe aller Nodes (jedes Objekt wird nur einmal gespeichert) — statt N-facher Duplikation bei unabhängigem Caching
- Konfigurierbare Warmup/Rampup-Phase verhindert Cold-Cache-Failover und Thundering-Herd
- Keine Implementierung auf Nutzerseite nötig — `cloud-vinyl` generiert die Director-Konfiguration automatisch

**Warmup und Rampup** (Default: `warmup: 0.1`, `rampup: 30s`)

`warmup: 0.1` lässt 10% des Traffics auf das Alternativ-Backend des jeweiligen Keys fließen. Das pre-populiert dessen Cache. Fällt ein Pod aus, hat sein Alternativ-Backend bereits ~10% der Keys warm — kein kalter Cache beim Failover. Ohne Warmup ist jeder Failover ein vollständiger Cache-Miss.

`rampup: 30s` gibt einem neu gestarteten Pod 30 Sekunden, um seinen Cache aufzubauen, bevor er 100% seines Key-Ranges erhält. Ohne Rampup bekommt ein Pod, der gerade den Placeholder-VCL-Status verlassen hat, sofort seine volle Last — mit leerem Cache. Bei häufigem HPA-Scaling kann das die Cache-Hit-Rate systematisch senken.

**Skalierungslimit:** Varnish Software empfiehlt Multi-Tier-Sharding ab 7+ Nodes, da Intra-Cluster-Traffic mit der Cluster-Größe wächst. Für cloud-vinyl v1alpha1 ist ein einzelner Tier ausreichend. Multi-Tier-Sharding ist ein explizites Nicht-Ziel für v1alpha1.

**libvmod-cluster (Evaluierung für v1alpha2):** Das Open-Source-Modul `libvmod-cluster` (UPLEX, Shard-Director-Autoren) erweitert um `.self_is_next()` für primäre-Node-Erkennung (sauberer als String-Vergleich mit `server.identity`) und `.set_real()` für dynamisches Backend-Switching. Für v1alpha2 evaluieren ob es ins cloud-vinyl-Varnish-Image gehört.

```
Client-Request (URL: /product/123) → trifft Pod my-cache-0
        │
        ▼
    vcl_recv
        │
        ├── X-Vinyl-Shard gesetzt? NEIN
        │       └── set X-Vinyl-Shard = "1"
        │           shard(URL) → my-cache-1
        │           set backend_hint = peer_my_cache_1
        │           return(pass) → Proxy zu my-cache-1
        │
        ▼
    my-cache-1 empfängt Request
        │
        ├── X-Vinyl-Shard gesetzt? JA
        │       └── unset X-Vinyl-Shard
        │           set backend_hint = vinyl_app.backend()
        │           → vcl_hash → Cache-Lookup
        │
        └── my-cache-1 verarbeitet Request → Cache-Hit oder Backend-Fetch
```

### 5.3 Pod-Liste-Verwaltung im Operator

Der Operator beobachtet zwei Quellen für Cluster-Peer-IPs:

**Quelle 1: StatefulSet-Pods**
Der Operator listet alle Pods des verwalteten StatefulSets und filtert auf `Running`-Status und bereit-sein (Readiness-Probe ok). Pod-IP aus `Pod.Status.PodIP`.

**Quelle 2: Headless-Service-Endpoints**
Als zweite Quelle dienen die Endpoints des headless Service, den der Operator für das StatefulSet anlegt. Diese werden vom kube-dns automatisch aktuell gehalten.

**Bevorzugte Quelle:** Pod-Watch (Quelle 1), da der Operator dort Kontrolle über den genauen Zeitpunkt des Aufnehmens in die Peer-Liste hat (z.B. erst nach erfolgreicher VCL-Push auf dem neuen Pod).

### 5.4 Rolling Update

Bei Scale-Up (neuer Pod startet):

```
1. Pod my-cache-3 startet
2. Init-Container schreibt Placeholder-VCL
3. varnishd startet mit Placeholder-VCL
4. vinyl-agent meldet /health = ok
5. Operator erkennt neuen Pod (Pod-Watch)
6. Operator wartet auf Debounce-Period (z.B. 5s)
7. Operator generiert neue VCL (jetzt mit 4 Peers)
8. Operator pusht VCL an alle 4 Pods (parallel)
9. Operator aktualisiert VinylCache.status
```

Bei Scale-Down (Pod terminiert):

```
1. Kubernetes sendet SIGTERM an Pod my-cache-3
2. Pod geht in Terminating-Zustand
3. Operator erkennt Zustandsänderung (Pod-Watch)
4. Operator generiert neue VCL (mit 3 Peers)
5. Operator pusht VCL an die verbleibenden 3 Pods
6. Pod beendet sich (terminationGracePeriodSeconds abwarten)
```

### 5.5 Cluster-Routing: X-Vinyl-Shard-Header-Pattern

In Varnish VCL ist `req.backend_hint` ein Backend-Typ — kein String. Er kann nicht direkt mit `server.identity` (String) verglichen werden. Daher verwendet cloud-vinyl das **Already-Routed-Pattern**:

```vcl
if (!req.http.X-Vinyl-Shard) {
    set req.http.X-Vinyl-Shard = "1";
    set req.backend_hint = vinyl_cluster.backend(by=URL);
    return(pass);   // Weiterleiten — ggf. an diesen Pod selbst
}
unset req.http.X-Vinyl-Shard;   // Bereits geroutet → Cache-Lookup
```

**Warum dieses Pattern:**
- Identische VCL auf allen Pods — kein pod-spezifischer Namensvergleich nötig
- `X-Vinyl-Shard`-Header verhindert Routing-Schleifen
- Falls der Shard-Director diesen Pod wählt, proxied Varnish localhost → minimaler Overhead im Cluster-Netzwerk
- Funktioniert ohne `-i`-Flag-Magie oder pod-spezifische VCL-Varianten

**Schutz gegen X-Vinyl-Shard-Spoofing (K2-Fix):** Ein externer Client, der `X-Vinyl-Shard: 1` setzt, würde das Cluster-Routing komplett umgehen — der Request wird nicht an den zuständigen Shard geroutet, was Cache-Fragmentierung und gezielte Cache-Misses (DoS-Vektor) ermöglicht. Der Operator generiert daher eine **Peer-ACL** aus den bekannten Pod-IPs und strippt extern gesetzte Shard-Header:

```vcl
// Peer-ACL (wird vom Operator mit aktuellen Pod-IPs befüllt)
acl vinyl_cluster_peers {
    "10.0.2.1";  // my-cache-0
    "10.0.2.2";  // my-cache-1
    "10.0.2.3";  // my-cache-2
}

// In vcl_recv, vor der Routing-Logik:
if (req.http.X-Vinyl-Shard && !(client.ip ~ vinyl_cluster_peers)) {
    unset req.http.X-Vinyl-Shard;  // Extern gesetzten Header strippen
}
```

Damit kann nur ein legitimer Cluster-Peer den `X-Vinyl-Shard`-Header setzen. Die Peer-ACL wird bei jedem VCL-Push mit den aktuellen Pod-IPs neu generiert (analog zur `vinyl_purge_allowed`-ACL).

**X-Vinyl-Shard-Header wird am Cluster-Ingress gestrippt** (der Operator generiert in `vcl_deliver` ein `unset resp.http.X-Vinyl-Shard`).

---

## 6. Purge/BAN-Strategie

### 6.1 Problem mit dem Sidecar-Ansatz

Ein sidecar-basierter Purge/BAN-Signaller im Varnish-Pod selbst führt zu strukturellen Problemen:
- Clients müssen zwei unterschiedliche Endpunkte kennen (Cache-Traffic vs. Invalidierung)
- Der Signaller-Port ist bei Pod-Neustart temporär nicht erreichbar
- Kein zentraler Kontrollpunkt für Invalidierungslogik

### 6.2 cloud-vinyl-Ansatz: Zentraler Proxy mit Host-Header-Routing

Der Operator läuft als **cluster-weites Deployment** (ein Pod, optional mit Leader-Election). Der Purge/BAN-Proxy läuft als HTTP-Server (:8090) im Operator-Pod.

Für jeden `VinylCache` erzeugt der Operator **einen dedizierten Kubernetes-Service im Namespace des `VinylCache`**. Dieser Service hat **keinen Selector** — ein ClusterIP-Service mit Selector kann nur Pods im eigenen Namespace selektieren, der Operator-Pod läuft aber in einem anderen Namespace. Stattdessen pflegt der Operator manuell ein `EndpointSlice`-Objekt, das auf die aktuellen IP-Adressen der Operator-Pods zeigt.

**Cross-Namespace-Routing via EndpointSlice:**

```yaml
# Service im VinylCache-Namespace (kein Selector — Kubernetes pflegt keine Endpoints)
apiVersion: v1
kind: Service
metadata:
  name: my-cache-invalidation
  namespace: production
spec:
  type: ClusterIP
  ports:
    - port: 8090
      protocol: TCP
---
# EndpointSlice: manuell vom Operator verwaltet
apiVersion: discovery.k8s.io/v1
kind: EndpointSlice
metadata:
  name: my-cache-invalidation-operator
  namespace: production
  labels:
    kubernetes.io/service-name: my-cache-invalidation
addressType: IPv4
ports:
  - port: 8090
    protocol: TCP
endpoints:
  - addresses: ["10.0.3.5"]   # Operator-Pod-IP (wird vom Operator aktuell gehalten)
    conditions:
      ready: true
```

Der Operator aktualisiert den `EndpointSlice` bei jedem Reconcile mit der aktuellen IP-Adresse seiner eigenen Pods. Bei mehreren Operator-Replicas (HA) werden alle Replica-IPs eingetragen.

**Cleanup via Finalizer:** Da der Invalidierungs-Service und sein EndpointSlice im Namespace des `VinylCache` liegen, der Operator aber in einem anderen Namespace — sind Cross-Namespace-OwnerReferences in Kubernetes verboten. Der Operator setzt stattdessen einen **Finalizer** auf das `VinylCache`-Objekt:

```
VinylCache gelöscht
  → DeletionTimestamp gesetzt
  → Finalizer: cloud-vinyl-operator verarbeitet:
    → Lösche Invalidierungs-Service in VinylCache-Namespace
    → Lösche EndpointSlice in VinylCache-Namespace
    → Lösche Agent-Secret (vinyl-agent-my-cache)
    → Entferne Finalizer → VinylCache wird endgültig gelöscht
```

Ressourcen im selben Namespace wie der `VinylCache` (StatefulSet, headless Service, Traffic-Service) können per OwnerReference auf das `VinylCache`-Objekt verweisen — **nur für cross-namespace Ressourcen (EndpointSlice, Service im anderen Namespace) ist der Finalizer-Weg nötig**.

Der Proxy identifiziert den Ziel-`VinylCache` über den `Host`-Header des eingehenden Requests: Der Hostname entspricht dem Namen des Invalidierungs-Service (`<cache-name>-invalidation.<namespace>.svc.cluster.local`). Anhand dieses Namens schlägt der Proxy die aktuelle Liste der Varnish-Pod-IPs in seiner In-Memory-Map nach und broadcastet an alle Pods dieses Clusters.

```
Purge-Client (z.B. Anwendung, CI/CD-Pipeline)
        │
        │  PURGE /product/123 HTTP/1.1
        │  Host: my-cache-invalidation.production.svc.cluster.local
        ▼
┌───────────────────────────────────────────────────────────┐
│  Service: my-cache-invalidation  (namespace: production)  │
│  ClusterIP, Port 8090                                      │
│  kein Selector — EndpointSlice zeigt auf Operator-Pod-IP  │
└──────────────────────────┬────────────────────────────────┘
                           │ leitet weiter an Operator-Pod
                           ▼
┌───────────────────────────────────────────────────────────┐
│  cloud-vinyl-operator (cluster-wide Deployment)           │
│                                                           │
│  Purge/BAN-Proxy (:8090)                                  │
│  ├── Host-Header lesen:                                   │
│  │     "my-cache-invalidation.production…"                │
│  │     → Lookup: VinylCache production/my-cache           │
│  ├── ACL-Check (Quell-IP gegen allowedSources)            │
│  ├── Broadcast-Queue für production/my-cache              │
│  └── Worker-Pool                                          │
│       ├── → varnishd my-cache-0 (:8080)  PURGE /product/… │
│       ├── → varnishd my-cache-1 (:8080)  PURGE /product/… │
│       └── → varnishd my-cache-2 (:8080)  PURGE /product/… │
└───────────────────────────────────────────────────────────┘
```

**Namensschema der Invalidierungs-Services**

| VinylCache | Namespace | Invalidierungs-Service DNS |
|------------|-----------|---------------------------|
| `my-cache` | `production` | `my-cache-invalidation.production.svc.cluster.local:8090` |
| `other-cache` | `staging` | `other-cache-invalidation.staging.svc.cluster.local:8090` |

Der Name des Service ist `<VinylCache.name>-invalidation` im Namespace des `VinylCache`. Konfigurierbar über `spec.service.invalidation`.

### 6.3 Vorteile dieses Ansatzes

- **Namespace-lokaler Endpunkt**: Jeder `VinylCache` hat einen eigenen stabilen DNS-Namen in seinem Namespace — kein Wissen über andere Namespaces oder den Operator nötig
- **Cluster-weite Kontrolle**: Die Proxy-Logik (Retry, ACL, Logging, Metriken) ist zentral im Operator — kein Code in den Varnish-Pods
- **Hochverfügbar**: Der Operator-Pod ist stabiler als einzelne Varnish-Pods; Leader-Election für HA
- **Audit-Logging**: Alle Invalidierungsanfragen passieren einen einzigen Punkt — vollständig loggbar
- **Retry**: Temporär nicht erreichbare Varnish-Pods werden mit konfigurierbarem Backoff wiederholt
- **Kein Sidecar-Port**: Varnish-Pods exponieren keinen eigenen Invalidierungsport
- **Multi-Tenant-sicher**: Host-Header-Routing verhindert, dass Requests eines Namespaces Pods eines anderen Namespaces treffen
- **Hochverfügbar (Multi-Replica)**: Der Invalidierungs-Proxy läuft auf **allen** Operator-Replicas, nicht nur auf dem Leader. Der manuell gepflegte EndpointSlice enthält alle Operator-Replica-IPs — Kubernetes load-balanciert eingehende Requests. Jede Replica hält ihre eigene aktuelle Varnish-Pod-IP-Map via Kubernetes-Watch, ohne geteilten State. Leader-Election gilt nur für den Controller-Manager (Reconcile-Loop), nicht für den Proxy.

### 6.4 Protokoll-Details

**PURGE (URL-basiert)**
```http
PURGE /product/123 HTTP/1.1
Host: my-cache-invalidation.production.svc.cluster.local
```

Der Proxy leitet das PURGE-Request direkt an alle Varnish-Pod-IPs des identifizierten `VinylCache` weiter. Varnish verarbeitet PURGE nativ (ACL aus `vcl_recv`).

**BAN (Regexp-basiert, via HTTP-Methode — an den Proxy, Port 8090)**

Der Invalidierungs-Proxy (Port 8090) akzeptiert BAN als HTTP-Methode mit dem Ausdruck im `X-Ban-Expression`-Header. Der Proxy übersetzt dies in einen validierten Admin-Protokoll-Aufruf und broadcastet an alle Varnish-Pods. Varnish selbst empfängt die BAN-Anfrage **nicht** per HTTP-Methode — das BAN-via-HTTP-Methode gilt ausschließlich für den Proxy-Endpunkt.
```http
BAN / HTTP/1.1
Host: my-cache-invalidation.production.svc.cluster.local
X-Ban-Expression: obj.http.X-Url ~ ^/product/
```

**BAN (Regexp-basiert, via REST-Endpunkt — an den Proxy, Port 8090)**
```http
POST /ban HTTP/1.1
Host: my-cache-invalidation.production.svc.cluster.local
Content-Type: application/json

{"expression": "obj.http.X-Url ~ ^/product/"}
```

**Surrogate-Key-Invalidierung / xkey (nur wenn `spec.invalidation.xkey.enabled: true`)**
```http
POST /purge/xkey HTTP/1.1
Host: my-cache-invalidation.production.svc.cluster.local
Content-Type: application/json

{"keys": ["article-123", "category-news"]}
```

**Architektur-Detail (K1-Fix):** `xkey.purge()` ist eine VCL-Funktion, die ausschließlich im Kontext einer laufenden HTTP-Transaktion aufrufbar ist — **nicht** über den Varnish Admin-Port (6082). Der Agent sendet xkey-Purges daher als internen HTTP-Request an `varnishd` auf `localhost:8080`:

```
PURGE / HTTP/1.1
Host: localhost
X-Xkey-Purge: article-123
```

Die generierte VCL enthält bei aktiviertem xkey einen Handler in `vcl_recv`:

```vcl
import xkey;

// In vcl_recv (nach Purge-ACL-Check):
if (req.method == "PURGE" && req.http.X-Xkey-Purge) {
    if (!client.ip ~ vinyl_purge_allowed) {
        return(synth(403, "Forbidden"));
    }
    set req.http.n-gone = xkey.purge(req.http.X-Xkey-Purge);
    return(synth(200, "Purged " + req.http.n-gone + " objects"));
}
```

Der Agent benötigt daher sowohl Zugang zum Admin-Port (127.0.0.1:6082) für VCL-Push als auch HTTP-Zugang zum Varnish-HTTP-Port (127.0.0.1:8080) für xkey-Purges. Soft-xkey-Purge analog via `xkey.softpurge()`.

Varnish invalidiert alle Objekte, deren `xkey`-Header einen der angegebenen Keys enthält. Das Backend muss den `xkey`-Header in Antworten setzen; der Operator generiert den entsprechenden Speicher-Code in `vcl_backend_response`.

**Response-Format bei Broadcast-Anfragen (B4)**

Der Proxy broadcastet PURGE/BAN/xkey-Purge an alle Varnish-Pods parallel. Das Response-Format muss bei partiellen Fehlern eindeutig sein:

- **HTTP 200**: Alle Pods erfolgreich invalidiert
- **HTTP 207 Multi-Status**: Mindestens ein Pod erfolgreich, mindestens einer fehlgeschlagen — der Client kann fortfahren, muss aber mit inkonsistentem Cache rechnen
- **HTTP 503**: Alle Pods fehlgeschlagen (kein Pod war erreichbar oder alle haben mit Fehler geantwortet)

Body (immer JSON, auch bei 200/503):
```json
{
  "status": "partial",
  "total": 3,
  "succeeded": 2,
  "results": [
    {"pod": "my-cache-0", "status": 200},
    {"pod": "my-cache-1", "status": 200},
    {"pod": "my-cache-2", "error": "connection timeout after 5s"}
  ]
}
```

Feld `status`: `"ok"` (alle erfolgreich), `"partial"` (207), `"failed"` (503).

Der Purge-Client sollte bei `"partial"` die betroffenen Pods loggen und ggf. manuellen Eingriff auslösen. Automatischer Retry einzelner fehlgeschlagener Pods ist möglich, da PURGE idempotent ist; BAN-Expressions hingegen akkumulieren — Retry-Logik muss Duplikate tolerieren (idempotent auf Varnish-Seite durch Deduplizierung im Lurker).

### 6.5 Sicherheit: Host-Header und Source-IP

**Unbekannter Host-Header:** Erreicht den Operator-Pod ein Request, dessen `Host`-Header keinem bekannten `VinylCache`-Invalidierungs-Service entspricht, antwortet der Proxy mit `404 Not Found`.

**Host-Header-Spoofing (Multi-Tenant):** In Multi-Tenant-Clustern könnte ein Angreifer, der den Operator-Pod direkt (nicht über den Invalidierungs-Service) erreichen kann, einen gefälschten `Host`-Header senden und so Caches anderer Namespaces invalidieren. Der Proxy wendet deshalb zwei unabhängige Prüfungen an:

1. **Host-Header**: Identifiziert den Ziel-`VinylCache`
2. **Source-IP-Check**: Die Quell-IP des Requests wird gegen die `allowedSources` des identifizierten `VinylCache` geprüft — unabhängig davon, ob der Request über den dedizierten Service oder direkt eintrifft

Zusätzlich sollte eine `NetworkPolicy` auf dem Operator-Pod Ingress auf Port 8090 auf die Namespaces beschränken, in denen `VinylCache`-Objekte existieren.

**BAN-Expression-Validierung (M4):** Der BAN-REST-Endpunkt nimmt rohe Ban-Expressions entgegen. Der Proxy validiert Expressions gegen eine Allowlist erlaubter LHS-Felder (z.B. `obj.http.X-Url`, `obj.http.X-Host`, `obj.http.X-Tag`) um willkürliche Cache-Invalidierung (`obj.http.content-type ~ .` = gesamter Cache) zu verhindern. Ban-Flooding ist ein realer DoS-Vektor (Bans akkumulieren im Speicher bis der Lurker sie abarbeitet) — Rate-Limiting auf dem Ban-Endpunkt ist Pflicht.

### 6.6 VCL-Integration

Der Operator generiert automatisch eine Varnish-ACL und den entsprechenden `vcl_recv`-Block für PURGE/BAN, wenn `spec.invalidation.purge.enabled` oder `spec.invalidation.ban.enabled` gesetzt ist. Die erlaubten Quellen aus `spec.invalidation.purge.allowedSources` werden als Varnish-ACL emittiert.

Da der Proxy im Operator-Pod läuft und die Requests an Varnish weiterleitet, muss die ACL die Pod-IP des Operator-Pods enthalten. Der Operator kennt seine eigene Pod-IP und fügt sie automatisch zur generierten ACL hinzu (zusätzlich zu `spec.invalidation.purge.allowedSources`).

---

## 7. Operator Lifecycle & Reconcile-Loop

### 7.1 Watches

Der `VinylCacheReconciler` registriert folgende Watches:

| Ressource | Event | Aktion |
|-----------|-------|--------|
| `VinylCache` | Erstellt, Geändert, Gelöscht | Reconcile auslösen |
| `StatefulSet` (owned) | Geändert | Reconcile auslösen (via OwnerReference) |
| `Pod` (StatefulSet-Pods) | Ready-Status geändert | Reconcile auslösen (Pod-Filter) |
| `Endpoints` (Backend-Services) | Geändert | Reconcile auslösen |
| `ConfigMap` (fullOverrideRef) | Geändert | Reconcile auslösen (via Field-Indexer, nicht OwnerReference — ConfigMaps werden nicht vom Operator erstellt) |

### 7.2 Reconcile-Loop

```
Reconcile(ctx, Request{NamespacedName: "production/my-cache"})
│
├── 1. VinylCache-Objekt laden
│       └── Falls nicht gefunden: return (deleted)
│
├── 2. Finalizer verwalten
│       └── Falls DeletionTimestamp: cleanup & remove finalizer
│               ├── Lösche InvalidationService + EndpointSlice (cross-namespace, kein OwnerRef möglich)
│               ├── Lösche Agent-Secret (vinyl-agent-<name>)
│               └── Entferne Finalizer cloud-vinyl-operator
│
├── 3. Status-Update vorbereiten (defer)
│
├── 4. Abhängige Ressourcen reconcilen
│       ├── 4a. ServiceAccount + RBAC sicherstellen
│       ├── 4b. HeadlessService sicherstellen
│       ├── 4c. TrafficService sicherstellen
│       ├── 4d. InvalidationService sicherstellen (wenn enabled):
│       │       ├── Service ohne Selector im VinylCache-Namespace
│       │       └── EndpointSlice manuell pflegen (eigene Operator-Pod-IPs)
│       ├── 4e. StatefulSet sicherstellen (Spec aus VinylCache generieren)
│       └── 4f. PodDisruptionBudget sicherstellen (wenn enabled)
│
├── 5. Aktuelle Pod-Liste ermitteln
│       ├── StatefulSet-Pods auflisten (Running, PodIP bekannt)
│       └── Readiness-Status pro Pod prüfen
│
├── 6. Backend-Endpoints auflisten
│       └── Für jeden Backend-ServiceRef: Endpoints-Objekt lesen
│
├── 7. Debouncing prüfen
│       ├── Letzte Endpoint-Änderung < debounce.period ?
│       │       └── JA: RequeueAfter(verbleibende Zeit)
│       └── NEIN: weiter
│
├── 8. VCL generieren
│       ├── VCL-Generator aufrufen (Spec + Endpoints + Peers)
│       └── SHA-256-Hash berechnen
│
├── 9. VCL-Push nötig?
│       ├── Hash == status.activeVCL.hash ?
│       │       └── JA: skip (kein Push)
│       └── NEIN: weiter
│
├── 10. VCL pushen (alle ready Pods parallel)
│        ├── Für jeden Pod: POST /vcl/push an vinyl-agent
│        ├── Fehler mit Retry (spec.retry)
│        └── Alle erfolgreich?
│                ├── JA: status.activeVCL aktualisieren
│                └── NEIN: Condition VCLSynced=False, RequeueAfter(backoff)
│
└── 11. Status schreiben
         ├── readyReplicas, updatedReplicas
         ├── backendEndpoints
         ├── clusterPeers
         ├── activeVCL
         └── conditions: Ready, VCLSynced, BackendsAvailable
```

### 7.3 Fehlerbehandlung

**VCL-Push-Fehler auf einzelnem Pod**
- Retry mit exponential Backoff (konfiguriert via `spec.retry`)
- Status-Condition `VCLSynced=False` mit Reason und Message
- Prometheus-Metrik: `vinyl_vcl_push_errors_total`
- Requeue nach letztem Retry-Intervall

**Kompilierungsfehler in VCL (Agent meldet 400)**
- Kein Retry (idempotent fehlerhaft)
- Status-Condition `VCLSynced=False` mit `Reason: VCLCompilationError`
- Event auf VinylCache-Objekt: `Warning VCLError "VCL compilation failed: ..."`
- Operator alertt auf vorherige aktive VCL (kein Rollback nötig — alte VCL bleibt aktiv in Varnish)

**Backend-Endpoint nicht erreichbar**
- Condition `BackendsAvailable=False`
- VCL-Push findet trotzdem statt (Varnish's eigene Health-Probe übernimmt das Management)
- Nur wenn *alle* Endpoints eines Backends gone: Warning-Event

**Pod nicht erreichbar (Agent-Timeout)**
- Retry gemäß `spec.retry`
- Nach max. Retries: Status `Degraded`, Condition `VCLSynced=False`
- Prometheus-Metrik: `vinyl_vcl_push_errors_total`

**Operator-Ausfall (Graceful Degradation)**
- Varnish-Pods laufen weiter mit der zuletzt aktiv gepushten VCL — kein Serviceausfall
- VCL-Updates und Purge/BAN-Invalidierungen sind während des Ausfalls nicht möglich
- Invalidierungsanfragen an den Proxy schlagen fehl (503) — Retry liegt beim Client
- Nach Operator-Wiederstart: vollständiger Reconcile-Loop läuft durch, Status wird neu bewertet
- Empfehlung: Clients der Invalidierungs-API sollten Retry mit Backoff implementieren

### 7.4 Debouncing-Implementierung

```
Trigger-Ereignis (Endpoint-Änderung)
        │
        ▼
  lastChangeTime = now()
        │
        ▼
  Requeue(after=debounce.period)
        │
        ▼
  Reconcile() aufgerufen
        │
        ├── now() - lastChangeTime < debounce.period ?
        │       └── JA: Requeue(after=verbleibende Zeit)
        │
        └── NEIN: VCL generieren & pushen

  (maxDelay verhindert unbegrenztes Hinauszögern:)
  lastChangeTime - firstChangeTime > maxDelay → sofort pushen
```

Die `lastChangeTime` wird in der `VinylCache.status`-Subresource oder in-memory im Operator gespeichert (letzteres ist ausreichend, da ein Operator-Neustart ohnehin eine vollständige Reconciliation auslöst).

**Interaktion mit dem Shard-Director:** Debouncing ist beim Shard-Director besonders kritisch. Jede VCL-Änderung (neue Peer-Liste) löst eine Ring-Rekonfiguration aus und verteilt ~1/N der Keys um. Bei aggressivem HPA-Scaling (z.B. Scale-Up von 3 auf 5 Pods in Schritten) verhindert ein ausreichendes `debounce.maxDelay` mehrere unnötige Ring-Rekonfigurationen in kurzer Zeit. Empfehlung: Bei aktivem Cluster-Routing `debounce.period` auf mindestens 10s und `debounce.maxDelay` auf mindestens 60s setzen.

### 7.5 Leader-Election

Für HA-Betrieb (mehrere Operator-Replicas) unterstützt der Operator Leader-Election via `controller-runtime`'s eingebautem Mechanismus (`--leader-elect` Flag). Nur der Leader-Pod führt Reconcile-Loops und VCL-Pushes durch. Der Purge/BAN-Proxy läuft auf **allen** Replicas (kein Leader-Check nötig — jede Replica hält ihre eigene Pod-IP-Map via Kubernetes-Watch).

### 7.6 Upgrade-Pfade (B1)

**Operator-Pod-Upgrade (Rolling Update des operator-Deployments):**

Varnish-Pods laufen autonom weiter mit der zuletzt gepushten VCL — kein Cache-Verlust, kein Datenverlust. Während der Transition (typisch 5–60 Sekunden):
- Kein VCL-Push möglich (neuer Endpoint-State wird nicht propagiert)
- Kein Purge/BAN möglich (503 auf dem Invalidierungs-Endpunkt wenn nur 1 Replica)
- Neue Pod-Starts im StatefulSet warten auf den neuen Operator

**Empfehlung:** 2+ Operator-Replicas für unterbrechungsfreien Purge/BAN-Betrieb. Der Reconcile-Loop hat trotzdem eine kurze Leader-Election-Pause (~5s).

**Varnish-Image-Upgrade (`spec.image.tag` ändern):**
1. Zuerst VCL-Kompatibilität auf einem Canary-Pod validieren: `spec.updateStrategy: OnDelete` setzen, einen Pod manuell löschen, mit Agent `/vcl/validate` prüfen
2. Dann `updateStrategy: RollingUpdate` → Operator aktualisiert StatefulSet → Rolling Restart
3. `OnDelete` als Default für Varnish-Image-Upgrades empfohlen — verhindert automatisches Durchstarten aller Pods ohne Validierung

**Agent-API-Kompatibilität:**
Beim Rolling Update können Pods unterschiedliche Agent-Versionen haben. Der Operator muss mit Agent N und N-1 kompatibel sein. Die Agent-API wird daher versioniert: `/v1/vcl/push`, `/v1/ban`, etc.

**CRD-Version-Upgrade (v1alpha1 → v1beta1):**
Siehe Abschnitt 11 (API-Versionierungsstrategie).

### 7.7 Blast-Radius und Produktionssicherheit (B2)

Ein Bug im VCL-Generator betrifft **alle** verwalteten VinylCache-Instanzen gleichzeitig. Das ist der größte Blast-Radius im System.

**Schutzmaßnahme 1 — Canary-VCL-Push:**
Vor dem vollständigen Rollout wird die neue VCL auf einem Pod aktiviert und 30s lang Metriken überwacht (Hit-Rate, Error-Rate). Bei positivem Ergebnis Push auf alle restlichen Pods. Das erfordert eine Canary-Phase im Reconcile-Loop. `POST /vcl/validate` (Abschnitt 9.6) ist der erste Schritt — er prüft Syntax, aber keine Semantik.

**Schutzmaßnahme 2 — Pause-Annotation:**
```
kubectl annotate vinylcache my-cache vinyl.bluedynamics.eu/reconcile-paused=true
```
Unterbricht den Reconcile-Loop für diese Instanz. Gibt Ops die Möglichkeit, bei einem bekannten Problem einzelne Caches zu pausieren, bevor der Operator weiterpusht.

**Schutzmaßnahme 3 — Rollback-Annotation:**
```
kubectl annotate vinylcache my-cache vinyl.bluedynamics.eu/rollback-to-hash=sha256:abc...
```
Der Operator sucht die VCL mit diesem Hash in seiner History und pusht sie erneut. Alternativ direkt via Agent: `vcl.use <ältere-cold-VCL>` — sofortiger Rollback ohne neuen Push.

**Schutzmaßnahme 4 — Reconcile-Rate-Limiting:**
Bei vielen VinylCache-Instanzen werden Reconcile-Loops über einen workqueue-RateLimiter (controller-runtime-Standard) auf z.B. 5 parallele Reconciles begrenzt. Das begrenzt den zeitlichen Blast-Radius bei einem Generator-Bug.

**Schutzmaßnahme 5 — VCL-Events:**
Bei jedem VCL-Push emittiert der Operator ein Kubernetes-Event mit altem und neuem Hash:
```
Normal VCLPushed  VinylCache my-cache: pushed sha256:abc→sha256:def to 3/3 pods
```
Ermöglicht schnelle Incident-Diagnose: was hat sich geändert und wann.

### 7.8 Periodischer Reconcile und Drift-Detection (B4)

**Periodischer Reconcile (Safety-Net):**
Der Operator reconciled jeden `VinylCache` mindestens alle 5 Minuten — unabhängig davon, ob ein Event eingetroffen ist:
```go
return reconcile.Result{RequeueAfter: 5 * time.Minute}, nil
```
Das stellt sicher, dass ein Pod, der einen VCL-Push verpasst hat (Netzwerk-Partition, temporärer Timeout), beim nächsten Reconcile-Zyklus korrigiert wird.

**VCL-Drift-Condition:**
Der Operator prüft bei jedem Reconcile, ob alle Pods denselben aktiven VCL-Hash melden (via `GET /vcl/active` auf jedem Agent). Bei Abweichung wird eine `VCLConsistent=False`-Condition gesetzt:
```yaml
- type: VCLConsistent
  status: "False"
  reason: VCLHashMismatch
  message: "Pod my-cache-2 has VCL sha256:old, expected sha256:new (2/3 pods consistent)"
```

---

## 8. Technischer Stack

### 8.1 Go-Module und Frameworks

| Komponente | Bibliothek | Begründung |
|------------|------------|------------|
| Operator-Framework | `sigs.k8s.io/controller-runtime` | Kubebuilder-kompatibel, Industriestandard, Leader-Election, Metrics out of the box |
| CRD-Generierung | `sigs.k8s.io/controller-tools` (`controller-gen`) | Generiert CRD YAML und DeepCopy aus Go-Structs |
| Kubernetes-Client | `k8s.io/client-go` | Via controller-runtime |
| Admin-Protokoll | `github.com/martin-helmich/go-varnish-client` | Bridgt Varnish-Admin-Protokoll (Klartextprotokoll mit Secret-Auth) |
| HTTP-Server | `net/http` (stdlib) | Für vinyl-agent und Purge/BAN-Proxy |
| Metriken | `github.com/prometheus/client_golang` | Prometheus-Metriken für Operator und Agent |
| Logging | `sigs.k8s.io/controller-runtime/pkg/log` (zap-basiert) | Strukturiertes Logging, kompatibel mit controller-runtime |
| VCL-Generierung | `text/template` (stdlib) | Typsichere Template-Ausführung |
| Test-Framework | `github.com/onsi/ginkgo/v2` + `github.com/onsi/gomega` | Kubebuilder-Standard für Controller-Tests |
| E2E-Tests | `sigs.k8s.io/controller-runtime/pkg/envtest` | Envtest für Integration-Tests ohne echten Cluster |
| Admission Webhook | `sigs.k8s.io/controller-runtime/pkg/webhook` | Validating Webhook: prüft `varnishParameters`-Blocklist, `allowedSources`-CIDRs, Backend-Identifier-Zeichensatz |

### 8.2 Repository-Struktur

```
cloud-vinyl/
├── cmd/
│   ├── operator/           # Operator-Binary (main.go)
│   └── agent/              # vinyl-agent Binary (main.go)
├── pkg/
│   ├── api/
│   │   └── v1alpha1/
│   │       ├── vinylcache_types.go     # CRD Go-Structs
│   │       ├── vinylcache_defaults.go  # Default-Werte
│   │       └── groupversion_info.go
│   ├── controller/
│   │   ├── vinylcache_controller.go    # Haupt-Reconciler
│   │   ├── resources/
│   │   │   ├── statefulset.go          # StatefulSet-Generierung
│   │   │   ├── service.go              # Service-Generierung
│   │   │   ├── endpointslice.go        # EndpointSlice (Invalidation-Service, cross-namespace)
│   │   │   ├── networkpolicy.go        # NetworkPolicy-Generierung
│   │   │   ├── secret.go               # Agent-Auth-Token-Secret (shared pro VinylCache)
│   │   │   └── pdb.go                  # PodDisruptionBudget
│   │   ├── webhook/
│   │   │   └── vinylcache_webhook.go   # Validating Admission Webhook
│   │   └── conditions/
│   │       └── conditions.go           # Condition-Helpers
│   ├── vcl/
│   │   ├── generator.go                # VCL-Generator (Haupt-Logik)
│   │   ├── generator_test.go
│   │   ├── snippets.go                 # Snippet-Injection
│   │   └── templates/                  # Go-Templates für VCL-Subroutinen
│   │       ├── vcl_init.vcl.tmpl
│   │       ├── vcl_recv.vcl.tmpl
│   │       └── ...
│   ├── agent/
│   │   ├── server.go                   # HTTP-Server des Agents
│   │   ├── varnish.go                  # Admin-Protokoll-Bridge
│   │   └── server_test.go
│   ├── invalidation/
│   │   ├── proxy.go                    # Purge/BAN-Proxy
│   │   └── proxy_test.go
│   └── endpoints/
│       └── watcher.go                  # Backend-Endpoint-Beobachtung
├── config/
│   ├── crd/                            # Generierte CRD-YAMLs
│   ├── rbac/                           # RBAC-YAMLs für Operator
│   └── samples/                        # Beispiel-VinylCache-YAMLs
├── chart/                              # Helm-Chart für cloud-vinyl-operator
│   ├── Chart.yaml
│   ├── values.yaml
│   └── templates/
├── images/
│   ├── varnish/                        # Varnish-Container-Image
│   │   └── Dockerfile
│   └── agent/                          # vinyl-agent-Image
│       └── Dockerfile
├── test/
│   ├── e2e/                            # E2E-Tests
│   └── integration/                    # Integration-Tests (envtest)
├── Makefile
└── go.mod
```

### 8.3 Container-Images

**`ghcr.io/bluedynamics/cloud-vinyl-varnish`**
- Basis: `debian:bookworm-slim`
- Enthält: `varnish` (OSS), `varnish-modules` (vmod_directors, vmod_vsthrottle, etc.)
- Enthält Debug-Tools: `varnishlog`, `varnishadm`, `varnishstat` (kein Distroless — Prod-Debugging erfordert diese Tools)
- User: `varnish` (UID 1000, nicht root)
- `/var/lib/varnish` und `/etc/varnish` gehören UID 1000
- **`varnishd` wird mit `-j none` gestartet** (H4-Fix): Varnish' Standard-Jail (`-j unix`) erfordert Root-Rechte zum Starten. Bei Non-Root-Betrieb (UID 1000) **muss** `-j none` explizit gesetzt werden — sonst schlägt der Pod-Start fehl.
- Enthält `vinyl-agent` Binary (oder als separates Image mit Init-Container-Pattern)

**`ghcr.io/bluedynamics/cloud-vinyl-operator`**
- Basis: `gcr.io/distroless/static:nonroot`
- Enthält ausschließlich das `operator`-Binary (statisch gelinkt)
- User: `65534` (nobody)

### 8.4 Prometheus-Metriken

Alle Metriken verwenden einheitlich den Prefix `vinyl_` (kein `vinylcache_`-Mischpräfix).

**Operator**

| Metrik | Typ | Beschreibung |
|--------|-----|--------------|
| `vinyl_reconcile_total` | Counter | Reconcile-Läufe (label: `result=success\|error`) |
| `vinyl_vcl_push_total` | Counter | VCL-Push-Versuche (label: `cache`, `pod`, `result`) |
| `vinyl_vcl_push_duration_seconds` | Histogram | VCL-Push-Latenz pro Pod |
| `vinyl_vcl_push_errors_total` | Counter | Fehlgeschlagene VCL-Pushes (label: `cache`, `pod`) |
| `vinyl_backend_endpoints` | Gauge | Bekannte Backend-Endpoints (label: `cache`, `backend`) |
| `vinyl_cluster_peers` | Gauge | Aktive Cluster-Peers (label: `cache`) |

**Purge/BAN-Proxy**

| Metrik | Typ | Beschreibung |
|--------|-----|--------------|
| `vinyl_invalidation_requests_total` | Counter | Eingehende Requests (label: `method=PURGE|BAN`) |
| `vinyl_invalidation_broadcast_total` | Counter | Ausgehende Requests an Pods (label: `pod`, `result`) |
| `vinyl_invalidation_queue_length` | Gauge | Aktuelle Queue-Länge |
| `vinyl_invalidation_duration_seconds` | Histogram | End-to-End-Latenz |

**vinyl-agent**

| Metrik | Typ | Beschreibung |
|--------|-----|--------------|
| `vinyl_agent_vcl_push_total` | Counter | VCL-Push-Anfragen (label: `result`) |
| `vinyl_agent_admin_errors_total` | Counter | Admin-Protokoll-Fehler (label: `error_type=timeout\|connection_refused\|auth_failed\|admin_unreachable\|compilation_error`) |
| `vinyl_agent_varnish_ready` | Gauge | 1 wenn varnishd erreichbar, sonst 0 |
| `vinyl_agent_varnish_uptime_seconds` | Gauge | varnishd-Uptime (Reset = unerwarteter varnishd-Child-Neustart) |
| `vinyl_agent_vcl_versions_loaded` | Gauge | Anzahl geladener VCL-Versionen (> 5 = Akkumulation/busy-State-Leak) |

**Fehlende Metriken (B3 — vor v1alpha1-Release ergänzen)**

| Metrik | Typ | Beschreibung |
|--------|-----|--------------|
| `vinyl_cache_hit_ratio` | Gauge (label: `cache`) | **Die** wichtigste SRE-Metrik. Aus varnishstat `cache_hit/(cache_hit+cache_miss)`. |
| `vinyl_backend_health` | Gauge (label: `cache`, `backend`, `pod`) | 1=healthy, 0=sick — aus Varnish-eigenen Probes (nicht Kubernetes-Probes) |
| `vinyl_operator_reconcile_duration_seconds` | Histogram | Erkennt hängende Reconcile-Loops |
| `vinyl_invalidation_partial_failure_total` | Counter | Broadcast an N Pods, M < N erfolgreich — Cache-Inkonsistenz-Indikator |
| `vinyl_vcl_push_skipped_total` | Counter (label: `cache`) | VCL-Push übersprungen (Hash identisch) — Debugging-Hilfe |

### 8.5 Alert-Definitionen

Mindest-Alertset für Produktionsbetrieb. Empfehlung: Als `PrometheusRule` im Helm-Chart (opt-in via `monitoring.prometheusRules.enabled: true`).

| Alert | Bedingung | Severity |
|-------|-----------|----------|
| `VinylCacheVCLSyncFailed` | Condition `VCLSynced=False` für > 5 Minuten | **critical** |
| `VinylCacheAllBackendsDown` | Alle Endpoints eines Backends `readyEndpoints=0` für > 2 Minuten | **critical** |
| `VinylOperatorDown` | Operator-Pod nicht ready für > 2 Minuten | **critical** |
| `VinylCacheDegraded` | Phase `Degraded` oder `Ready=False` für > 10 Minuten | **warning** |
| `VinylCacheHitRateDrop` | `vinyl_cache_hit_ratio` fällt um > 20 Prozentpunkte in 15 Minuten | **warning** |
| `VinylVCLAccumulation` | `vinyl_agent_vcl_versions_loaded` > 5 für > 10 Minuten auf einem Pod | **warning** |
| `VinylInvalidationPartialFailure` | `vinyl_invalidation_partial_failure_total` Rate > 1/min für 5 Minuten | **warning** |
| `VinylPurgeBanQueueBacklog` | `vinyl_invalidation_queue_length` > 100 für > 1 Minute | **warning** |
| `VinylVarnishChildRestarted` | `vinyl_agent_varnish_uptime_seconds` resettet (varnishd-Child-Neustart) | **warning** |
| `VinylCacheVCLDrift` | Condition `VCLConsistent=False` für > 10 Minuten | **warning** |

---

## 9. Architekturentscheidungen (ADR)

Dokumentiert die wesentlichen Designentscheidungen mit den bewerteten Alternativen. Das *Warum* hinter jeder Entscheidung ist für spätere Weiterentwicklung wichtiger als die Entscheidung selbst.

### 9.1 gRPC vs. REST für vinyl-agent-API

**Option A: REST (HTTP/JSON)**
- Einfacher zu implementieren und zu debuggen
- `curl`-kompatibel für manuelle Operationen
- Kein Protobuf-Schema-Management
- Ausreichend für die Anforderungen (VCL-Push ist kein hochfrequenter Vorgang)

**Option B: gRPC**
- Typsichere API durch Protobuf
- Besseres Streaming für zukünftige Features (z.B. Admin-Befehle)
- Mehr Komplexität im Build-System

**Entschieden: REST (HTTP/JSON).** 6 selten aufgerufene Endpunkte, keine Streaming-Anforderung, maximale Debuggbarkeit mit `curl`. gRPC würde Build-Komplexität ohne messbaren Vorteil einführen.

### 9.2 VCL-Template-Engine: text/template vs. eigenständiger AST

**Option A: `text/template`**
- Stdlib, keine Dependency
- Go-native, keine externe Lernkurve
- Schwieriger zu testen (String-Vergleiche)
- Snippet-Injection erfordert sorgfältige Template-Struktur

**Option B: Eigenständiger VCL-AST**
- Typsichere VCL-Generierung
- Theoretisch einfacher zu testen (strukturierter Vergleich)
- Erheblicher Implementierungsaufwand (VCL-Grammatik ist nicht trivial)
- Kein Nutzen für Full-Override-Modus

**Entschieden: `text/template`.** Stdlib, kein Build-Overhead, ausreichend für die Komplexität der generierten VCL. Syntaxfehler in User-Snippets werden durch den vorgelagerten `POST /vcl/validate`-Schritt abgefangen.

### 9.3 Secret-Management: Varnish-Admin-Secret und Agent-Auth-Token

**Entscheidung: Ein shared Secret pro VinylCache (statt pro Pod)**

Pro-Pod-Secrets (`vinyl-agent-<cache-name>-<pod-name>`) wurden als Overengineering ohne Sicherheitsgewinn eingestuft: Der Operator kontrolliert ohnehin alle Pods eines Clusters; ein einheitliches Token pro Cache-Cluster reicht aus. Bei 3 Caches mit je 5 Replicas würden 15 Secrets entstehen — 3 sind ausreichend.

Zwei Secrets müssen pro `VinylCache` verwaltet werden:

1. **Varnish-Admin-Secret** (`/etc/varnish/secret`): Authentifizierung zwischen `varnishd` und `vinyl-agent` über das Admin-Protokoll. Wird per Init-Container in ein `emptyDir`-Volume geschrieben (pod-intern, kein Kubernetes-Secret nötig).

2. **Agent-Auth-Token** (`/run/vinyl/agent-token`): Bearer-Token für die HTTP-API des Agents (Operator → Agent). **Ein Token pro VinylCache**, geteilt von allen Pods des Clusters.

```
Operator
  └── generiert Token → K8s Secret vinyl-agent-my-cache  (ein Secret, alle Pods)
        └── Secret-Volume-Mount → /run/vinyl/agent-token im Agent-Container jedes Pods
              └── Agent liest Token direkt beim Start
```

**Warum Kubernetes-Secret für den Agent-Token?**
- Der Operator muss das Token kennen (er schickt Requests an alle Agents)
- `emptyDir` ist pod-intern — der Operator kann es nicht von außen lesen
- Secret-Volume-Mount ist der Standard-Kubernetes-Weg, Secrets in Container zu geben

**Token-Lifecycle:**
- Token wird beim ersten Reconcile des `VinylCache` generiert (falls Secret nicht vorhanden)
- Token wird beim Löschen des `VinylCache` im Finalizer-Handler gelöscht
- Token-Rotation: `kubectl delete secret vinyl-agent-<cache>` → Operator regeneriert beim nächsten Reconcile, alle Pods des Clusters müssen neu starten (Rolling Update via StatefulSet-Rollout)

### 9.4 Cross-Namespace-Backends

Soll `spec.backends[].serviceRef.namespace` für Cross-Namespace-Referenzen unterstützt werden?

**Option A: Nur Same-Namespace**
- Einfacher RBAC
- Konsistent mit Kubernetes-Networking-Defaults

**Option B: Cross-Namespace mit explizitem ReferenceGrant**
- Gateway-API-konformes Pattern
- Komplexere RBAC-Anforderungen
- Wichtig für Multi-Tenant-Szenarien

**Entschieden: Nur Same-Namespace.** Cross-Namespace-Referenzen erlauben impliziten Zugriff auf Backend-Services anderer Teams ohne deren Zustimmung. Der Operator würde cluster-weite Service-Leserechte benötigen. `spec.backends[].serviceRef.namespace` ist ein verbotenes Feld — der Webhook lehnt es ab. Für v1beta1 ggf. mit ReferenceGrant-Pattern nachziehen.

### 9.5 Operator-Scope: Cluster-Wide vs. Namespace-Scoped

**Option A: Cluster-Wide**
- Ein Operator verwaltet alle `VinylCache`-Objekte im gesamten Cluster
- Einfachere Deployment
- Breite RBAC-Berechtigungen notwendig

**Option B: Namespace-Scoped**
- Operator verwaltet nur `VinylCache`-Objekte in konfigurierten Namespaces
- Geringere RBAC-Berechtigungen
- Mehrere Operator-Instanzen möglich (für Multi-Tenant)

**Entschieden: Cluster-Wide.** Ein Operator verwaltet alle `VinylCache`-Objekte im Cluster — analog zu CNPG und Strimzi (Strimzi: `STRIMZI_NAMESPACE=*`). Namespace-Scoping via `--namespace`-Flag kann in v1beta1 nachgerüstet werden ohne Breaking Change.

### 9.6 VCL-Validierung vor Push

Soll die generierte VCL vor dem Push syntaktisch validiert werden?

**Option A: Keine Vorab-Validierung**
- Einfacher
- Fehler werden erst beim Push (via Agent) gemeldet

**Option B: Vorab-Validierung via `varnishd -C` im Operator**
- Erfordert varnishd-Binary im Operator-Image (Distroless wird unmöglich) oder separaten Validierungs-Pod
- Falsch-Positiv-Risiko (Version-Mismatch Operator vs. Varnish-Pod)

**Option C: Vorab-Validierung via `POST /vcl/validate` im Agent (entschieden)**
- Der vinyl-agent führt `vcl.load <name> -` ohne nachfolgendes `vcl.use` aus
- Validation passiert auf dem echten `varnishd` im Pod — kein Version-Mismatch, kein extra Binary
- Der Operator kann vor dem Push gegen einen beliebigen (z.B. den ersten gesunden) Pod validieren
- Fehler mit Zeilen-Nummer und Meldung werden direkt zurückgegeben
- Kein Distroless-Problem, kein Overhead

**Empfehlung:** Option C. Der `POST /vcl/validate`-Endpunkt ist fester Bestandteil der vinyl-agent-API. Der Operator nutzt ihn optional (konfigurierbar per `--vcl-validate-before-push` Flag).

### 9.7 StatefulSet vs. Deployment

Varnish-Pods werden aktuell als StatefulSet geplant (für stabile Hostnamen als `server.identity`).

**Warum StatefulSet:**
- Stabile, vorhersehbare Pod-Namen (`my-cache-0`, `my-cache-1`)
- `server.identity` in VCL entspricht dem Pod-Namen
- Shard-Director-Routing basiert auf `server.identity`

**Nachteil:**
- Langsamere Updates (sequentiell by default)

**Entscheidung:** StatefulSet mit **`podManagementPolicy: Parallel` als fester Default** (N1). Sequentielles Pod-Management ist für Cache-Nodes sinnlos — Varnish-Pods teilen keinen State und haben keine Abhängigkeiten untereinander beim Start. Sequentiell würde Scale-Up von 1→5 fünfmal so lange dauern.

`updateStrategy` wird im CRD als `spec.updateStrategy` exponiert mit den Optionen:
- `RollingUpdate` (Default für Image-Updates): Pods werden sequentiell (einer nach dem anderen) aktualisiert
- `OnDelete`: Manueller Rollout — empfohlen für Varnish-Image-Upgrades, da VCL-Kompatibilität mit der neuen Version zuerst validiert werden sollte

### 9.8 PROXY-Protocol-Support

Varnish unterstützt nativ mehrere `-a`-Flags. Die Operator-generierte varnishd-Startkommando kann mehrere Listener konfigurieren. Dies könnte in `spec.listeners[]` abgebildet werden:

```yaml
# (zukünftiges Feature)
listeners:
  - name: http
    port: 8080
    protocol: HTTP
  - name: proxy
    port: 8443
    protocol: PROXY
```

**Entschieden: `spec.proxyProtocol.enabled: true` für v1alpha1.** Fügt einen zweiten Listener auf Port 8081 mit PROXY-Protocol hinzu (`varnishd -a "0.0.0.0:8080,HTTP" -a "0.0.0.0:8081,PROXY"`). Varnish setzt `client.ip` automatisch auf die echte Client-IP — kein VCL-Code nötig. Der Operator exponiert Port 8081 im Service wenn aktiviert. Das volle `spec.listeners[]`-Array ist für v1beta1 vorgesehen wenn mehrere Listener-Varianten gebraucht werden.

### 9.9 v1beta1-Feature-Backlog

Folgende Features sind für v1alpha1 bewusst ausgeklammert und für v1beta1 vorgesehen. Sie sind architektonisch berücksichtigt (keine Breaking Changes zu erwarten), aber noch nicht spezifiziert:

| Feature | Beschreibung | CRD-Feld |
|---------|-------------|----------|
| URL-Normalisierung | Tracking-Parameter entfernen (`utm_*`, `fbclid`, `gclid`), Query-String sortieren | `spec.vcl.cache.urlNormalization` |
| Static-Asset-Optimierung | Pfade/Extensions immer cachen, Cookies ignorieren, lange TTL | `spec.vcl.cache.staticAssets` |
| Kompression | `beresp.do_gzip = true` für konfigurierte Content-Types | `spec.vcl.cache.compression` |
| Synthetische Antworten | Health-Endpunkt direkt aus Varnish, HTTP→HTTPS-Redirects | `spec.vcl.synthetic` |
| Saint-Mode | Fehlerhafte Backends temporär blacklisten via `vmod_saintmode` | `spec.backends[].saintMode` |
| Multi-File-VCL | `fullOverrideRef` mit mehreren ConfigMap-Keys (include-Semantik) | `spec.vcl.fullOverrideRef.includeKeys` |
| Snippet-Priorität | Array mit `priority`-Feld pro Hook für Multi-Team-Szenarien | `spec.vcl.snippets.<hook>[].priority` |
| Cross-Namespace-Backends | Backend-Services in anderen Namespaces via ReferenceGrant | `spec.backends[].serviceRef.namespace` |
| Namespace-Scoped Operator | Operator verwaltet nur konfigurierte Namespaces (`--namespace`) | Operator-Flag |

---

## 10. Security-Modell

### 10.1 Trust-Boundaries

```
┌─────────────────────────────────────────────────────────────────────┐
│  VERTRAUENSWÜRDIG (Operator-kontrolliert)                            │
│  - cloud-vinyl-operator Pod                                          │
│  - vinyl-agent (nur via Bearer-Token erreichbar)                     │
│  - varnishd Admin-Port (nur localhost:6082)                          │
└────────────────────────────────────────┬────────────────────────────┘
                                         │ Grenze
┌────────────────────────────────────────▼────────────────────────────┐
│  SEMI-VERTRAUENSWÜRDIG (Cluster-intern, RBAC-geschützt)             │
│  - VinylCache-Objekte (kubectl-Rechte = Code-Exec-Rechte auf Pods)  │
│  - Invalidierungs-Requests (ACL + Source-IP-Check)                  │
│  - Backend-Pods (HTTP, kein TLS auf Backend-Verbindung)              │
└────────────────────────────────────────┬────────────────────────────┘
                                         │ Grenze
┌────────────────────────────────────────▼────────────────────────────┐
│  NICHT VERTRAUENSWÜRDIG (Extern)                                     │
│  - Eingehender HTTP-Traffic auf :8080 (Varnish-Frontend)             │
│  - PURGE/BAN-Clients (müssen in allowedSources stehen)               │
└─────────────────────────────────────────────────────────────────────┘
```

### 10.2 RBAC-Implikationen

**VinylCache-Edit-Rechte = Code-Execution auf Varnish-Pods**

Wer `VinylCache`-Objekte anlegen oder bearbeiten kann, kann beliebigen VCL-Code einschleusen (via `spec.vcl.snippets` oder `spec.vcl.fullOverride`). Dies ist konzeptionell ähnlich zu `kubectl exec` — und muss in der RBAC-Konfiguration entsprechend behandelt werden.

Empfehlung: Separate RBAC-Rollen für:
- **VinylCache-Viewer**: `get`, `list`, `watch`
- **VinylCache-Operator**: `create`, `update` auf nicht-VCL-Felder (via Admission Webhook eingeschränkt — zukünftiges Feature)
- **VinylCache-Admin**: volle Rechte inkl. VCL-Snippets

### 10.3 Transport-Sicherheit

Für v1alpha1 gilt:
- **Operator → vinyl-agent**: Plain HTTP, authentifiziert via Bearer-Token (Vertraulichkeit durch Cluster-Netzwerk-Encryption vorausgesetzt)
- **Operator → varnishd-Pods (Invalidierung)**: Plain HTTP
- **varnishd → Backend-Services**: Plain HTTP (Backend-TLS ist Aufgabe des Backend-Services, nicht von Varnish)

**Voraussetzung:** Cluster-Netzwerk-Encryption (WireGuard, Cilium Transparent Encryption o.ä.) wenn Vertraulichkeit des VCL-Inhalts oder der Invalidierungs-Requests erforderlich ist.

**Roadmap:** mTLS zwischen Operator und vinyl-agent als Option in v1beta1.

### 10.4 NetworkPolicies (Operator-generiert)

Der Operator erzeugt für jeden `VinylCache` folgende NetworkPolicies:

| Policy | Ziel | Erlaubter Ingress |
|--------|------|-------------------|
| `vinyl-agent-access` | Pods des StatefulSets, Port 9090 | Nur Operator-Pod (Label-Selector) |
| `varnish-traffic` | Pods des StatefulSets, Port 8080 | Traffic-Service + Cluster-Peers (Labels) |
| `invalidation-ingress` | Operator-Pod, Port 8090 | `allowedSources` CIDRs |

**Voraussetzung:** Cluster hat einen NetworkPolicy-fähigen CNI-Plugin (Calico, Cilium, etc.). Ohne NetworkPolicy-Support werden die Objekte erzeugt, sind aber wirkungslos — der Operator loggt in diesem Fall eine Warnung.

### 10.5 Labels für verwaltete Ressourcen

Alle vom Operator erzeugten Ressourcen tragen sowohl die [Kubernetes Recommended Labels](https://kubernetes.io/docs/concepts/overview/working-with-objects/common-labels/) als auch operator-spezifische Labels:

```yaml
metadata:
  labels:
    # Kubernetes Recommended Labels
    app.kubernetes.io/name: vinyl-cache
    app.kubernetes.io/instance: my-cache          # VinylCache-Name
    app.kubernetes.io/component: cache            # oder "operator", "agent"
    app.kubernetes.io/managed-by: cloud-vinyl-operator
    app.kubernetes.io/part-of: cloud-vinyl
    # Operator-spezifisches Label (konsistenter Präfix mit API-Gruppe)
    vinyl.bluedynamics.eu/cache: my-cache
```

Der Selector in Affinitäten und TopologySpreadConstraints verwendet das operator-spezifische Label (`vinyl.bluedynamics.eu/cache`), da `app.kubernetes.io/instance` potentiell von anderen Tools gesetzt werden kann und nicht für Selektoren gedacht ist.

### 10.6 Admission Webhook und CEL-Validierung

**Hybridansatz:** Deterministische Regeln werden als CRD-eingebettete CEL-Ausdrücke (Kubernetes 1.25+) implementiert, komplexere Prüfungen als Webhook.

**CEL-Validierung (im CRD-Schema):**
```
# Mindestens ein Backend
rule: "size(self.backends) >= 1"

# Backend-Namen: nur VCL-konforme Bezeichner
rule: "self.backends.all(b, b.name.matches('^[a-zA-Z][a-zA-Z0-9_]*$'))"

# varnishParameters-Blocklist
rule: "!has(self.varnishParameters) || !('vcc_allow_inline_c' in self.varnishParameters)"
rule: "!has(self.varnishParameters) || !('cc_command' in self.varnishParameters)"

# Director-Union: nur die zum type passende Substruktur
rule: "self.director.type != 'shard' || !has(self.director.roundRobin)"

# Snippet-Größenlimit (64 KB pro Feld)
rule: "!has(self.vcl) || !has(self.vcl.snippets) || size(self.vcl.snippets.vclRecv) <= 65536"
```

**Validating Admission Webhook** (Bestandteil des Operator-Deployments) für Prüfungen, die externe Daten oder komplexere Logik erfordern:

- **`spec.varnishParameters`-Blocklist** (vollständig, mit `feature`-Flags): Abgelehnte Parameter:
  - `vcc_allow_inline_c` (C-Code in VCL → Remote Code Execution)
  - `cc_command` (beliebiger Compiler-Aufruf)
  - `feature +esi_disable_xml_check` (XSS via ESI)
- **`spec.storage[].type`-Blocklist** (H3): Abgelehnte Storage-Typen:
  - `persistent` — effektiv deprecated (`deprecated_persistent`), fundamentale Konsistenzprobleme über Neustarts, Bans werden nicht persistent gespeichert. Wurde von Wikimedia explizit abgeschaltet.
  - `umem` — nur auf Solaris/illumos verfügbar
  - `default` — auf Linux identisch mit `malloc`, verwirrend und überflüssig
- **`spec.invalidation.*.allowedSources`**: CIDR-Syntax-Validierung
- **`spec.backends[].serviceRef.name`**: Kubernetes-DNS-Name-Validierung
- **Defaulting** (Mutating Webhook): Default-Werte für optionale Felder setzen

### 10.7 Sicherheitsempfehlungen (nicht Architektur-Bestandteil)

Folgende Maßnahmen liegen außerhalb des Operators, werden aber empfohlen:

- **Image-Signing**: Alle eigenen Images mit Sigstore/Cosign signieren. Image-Verification-Policy (Kyverno o.ä.) im Cluster einsetzen.
- **Rate-Limiting am Invalidierungs-Endpunkt**: Der Purge/BAN-Proxy hat in v1alpha1 kein Rate-Limiting. Ein vorgelagerter ingress-level Rate-Limiter (z.B. via Nginx oder Envoy) ist empfohlen, um Mass-Purge-DoS zu verhindern.
- **Regelmäßige Dependency-Updates**: Dependabot/Renovate für Go-Module und Container-Base-Images.
- **Vulnerability-Scanning**: Container-Images bei jedem Release auf CVEs prüfen (Trivy o.ä.).

---

## 11. API-Versionierungsstrategie

### 11.1 Versionsplan

| Version | Status | Ziel |
|---------|--------|------|
| `v1alpha1` | **Aktiv** | Initiale API. Breaking Changes vorbehalten. Kein Conversion Webhook. Erwartete Lebensdauer: bis Feature-Vollständigkeit erreicht ist. |
| `v1beta1` | Geplant | Stabile Feldstruktur. Keine Breaking Changes mehr. Served parallel zu v1alpha1 mit Deprecation-Warnung. |
| `v1` | Langfristig | GA. Langzeit-Support. Vollständige Dokumentation und Conformance-Tests. |

### 11.2 Voraussetzungen für v1beta1-Promotion

- Cross-Namespace-Backends mit ReferenceGrant
- Erweiterter PROXY-Protocol-Support via `spec.listeners[]` (v1alpha1 hat `spec.proxyProtocol.enabled` als einfaches Bootstrapping)
- CEL-Validierung vollständig (Webhook als Ergänzung, nicht Ersatz)
- Gateway-API-konformes `backendRef`-Pattern
- Per-Content-Type Grace (`spec.vcl.cache.contentTypes[].grace`)
- Mindestens 2 Releases in Produktionseinsatz

### 11.3 Voraussetzungen für v1

- 6+ Monate v1beta1-Stabilität ohne Breaking Changes
- Vollständige Conformance-Tests
- Conversion Webhook v1beta1→v1 (falls strukturelle Unterschiede entstanden)

### 11.4 Deprecation-Strategie (v1alpha1 → v1beta1)

Wenn v1alpha1 strukturell korrekt gebaut ist (Storage als Array, Scheduling unter `spec.pod`, korrekte Conditions), sollte der Übergang zu v1beta1 rein additiv sein — kein Conversion Webhook nötig:

```yaml
# CRD versions-Feld nach Promotion:
versions:
  - name: v1beta1
    served: true
    storage: true
  - name: v1alpha1
    served: true       # → false nach Migrationszeitraum (min. 3 Releases)
    storage: false
    deprecated: true
    deprecationWarning: "vinyl.bluedynamics.eu/v1alpha1 VinylCache is deprecated; please migrate to v1beta1"
```

### 11.5 Conversion Webhook (wenn nötig)

Ein Conversion Webhook wird implementiert, sobald zwei Versionen mit strukturellen Unterschieden gleichzeitig served werden müssen. Pattern:

- **Hub-and-Spoke**: Eine interne Go-Struct als Hub (die "kanonische" Version), Conversion-Funktionen für jede API-Version als Spoke
- Scaffolding: `kubebuilder create webhook --group vinyl --version v1beta1 --kind VinylCache --conversion`
- Testpflicht: Roundtrip-Tests (v1alpha1 → Hub → v1beta1 → Hub → v1alpha1 ohne Datenverlust)

### 11.6 Migrationsrisiken der aktuellen v1alpha1-Felder

| Feld | Stabil? | Kommentar |
|------|---------|-----------|
| `spec.replicas` | Ja | Kubernetes-Standard |
| `spec.image` | Ja | Standard-Pattern |
| `spec.resources` | Ja | Standard `corev1.ResourceRequirements` |
| `spec.storage[]` | Ja | Array-Struktur erweiterbar ohne Breaking Change |
| `spec.backends[]` | Teilweise | `serviceRef` → `backendRef` für v1beta1 (additiv) |
| `spec.director` | Ja | Union-Pattern bereits vorbereitet, CEL-Validierung folgt |
| `spec.cluster` | Ja | Erweiterbar; `peerRouting.type` als Union-Field vorbereitet |
| `spec.pod` | Ja | Gruppierung unter `spec.pod` ist zukunftssicher |
| `spec.varnishParameters` | Ja | Freie Map, Blocklist via Webhook — beliebig erweiterbar |
| `spec.vcl` | Teilweise | Dreistufige Hierarchie ist gut; `spec.vcl.cache` kann wachsen |
| `spec.invalidation` | Ja | Sauber strukturiert, erweiterbar |
| `spec.proxyProtocol` | Teilweise | Einfaches Boolean-Feld; in v1beta1 durch `spec.listeners[]` ersetzt (Migration: additiv, altes Feld deprecated) |
| `spec.debounce` | Ja | Stabile Struktur mit `metav1.Duration` |
| `spec.retry` | Ja | Standard-Pattern |
| `spec.monitoring` | Ja | Gut isoliert |

---

*Dieses Dokument wird mit dem Fortschritt der Implementierung aktualisiert. Alle Architekturentscheidungen (Abschnitt 9) sind getroffen — Änderungen erfordern explizite Begründung und Aktualisierung des ADR.*