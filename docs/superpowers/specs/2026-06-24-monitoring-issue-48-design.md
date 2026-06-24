# Design: Varnish/Cache-Monitoring (Issue #48)

**Datum:** 2026-06-24
**Issue:** [bluedynamics/cloud-vinyl#48](https://github.com/bluedynamics/cloud-vinyl/issues/48)
**Status:** Genehmigt, bereit für Implementierungsplan

## Problem

`spec.monitoring` (enabled / serviceMonitor / prometheusRules) ist im CRD vorhanden,
aber der Reconciler handelt nicht darauf:

- Die `vinyl_*`-Metriken in `internal/monitoring/metrics.go` sind definiert, aber
  `NewMetrics()` wird nirgends aufgerufen — die Registry bleibt leer.
- `GenerateServiceMonitor()` / `GeneratePrometheusRule()` existieren, werden aber nie
  aus `Reconcile()` aufgerufen. Sie liefern zudem eigene Minimal-Structs, kein echter
  CRD-Typ → von Go aus nicht anwendbar (die in `docs/plans/m6-monitoring.md` notierte
  „Offene Frage 1").
- Kein Varnish-Exporter-Sidecar → `varnishstat`-Daten (cache_hit/miss, backend health)
  sind nicht scrapebar.

**Folge:** `spec.monitoring` aktivieren produziert keine scrapebaren Daten; das
Grafana-Ops-Dashboard kann die Cache-Hit-Rate nicht zeigen.

Der ursprüngliche M6-Plan (`docs/plans/m6-monitoring.md`) und `architektur.md` §8.4/§8.5
spezifizieren das Soll-Verhalten bereits vollständig; die Umsetzung blieb auf halbem Weg
stehen.

## Entscheidungen

1. **Umfang:** Alle drei Issue-Teilaufgaben, Hit-Rate zuerst (das ist das blockierte Ziel).
2. **Hit-Ratio-/Backend-Health-Quelle:** Der `prometheus_varnish_exporter`-Sidecar ist die
   Quelle (native `varnish_*`-Metriken). Die redundanten Gauges `vinyl_cache_hit_ratio`
   und `vinyl_backend_health` werden **entfernt**; keine Operator-Polling-Maschinerie.

## Architektur

Drei unabhängige, opt-in Schichten. Alle generierten Ressourcen sind per OwnerReference
an die VinylCache gekoppelt (GC bei Löschung). Nichts bricht auf Clustern ohne
prometheus-operator.

### Schicht 1 — `vinyl_*`-Metriken verdrahten (DI, nil-safe)

- `monitoring.NewMetrics(metrics.Registry)` **einmal** in `cmd/operator/main.go`
  instanziieren — registriert in die controller-runtime-Registry
  (`sigs.k8s.io/controller-runtime/pkg/metrics`), also denselben `/metrics`-Endpunkt, den
  der Operator bereits serviert. Kein zweiter Metrics-Server.
- `*monitoring.Metrics` als Feld in `VinylCacheReconciler` und `proxy.Server` injizieren
  (nil-safe: alle Metric-Aufrufe `if m != nil { … }`, damit Tests ohne Metrics laufen).
- **Operator-Domänen-Metriken werden immer gezählt** (nicht per-Cache gegated): die Zähler
  sind billig, und `spec.monitoring.enabled` ist *pro Cache* — der Operator-`/metrics`-
  Endpunkt ist cluster-global. Ein per-Cache-`nil` wie im alten M6-Plan ergibt für den
  Singleton-Operator keinen Sinn. `monitoring.enabled` steuert nur die generierten CRs und
  den Sidecar (Schicht 2/3).
- Instrumentierungspunkte:
  - `Reconcile()`: `ReconcileDuration` (defer-Timer) + `ReconcileTotal{cache,namespace,result}`.
  - `pushVCL` (`internal/controller/vcl_push.go`): `VCLPushTotal{cache,namespace,result}`
    pro Peer, `VCLPushDuration` um die Gesamtoperation.
  - Proxy `handlePurge`/`handleBAN`/`handleXkey` (`internal/proxy/handler.go`):
    `InvalidationTotal{cache,namespace,type,result}`, `InvalidationDuration`,
    `BroadcastTotal{pod,result}` pro Pod, `PartialFailureTotal{cache,namespace}` wenn
    manche-aber-nicht-alle Pods scheitern.
  - `VCLVersionsLoaded{cache,namespace}`: im Status-Update aus `vcl.list` gesetzt.

### Schicht 2 — ServiceMonitor + PrometheusRule generieren & anwenden

- **Apply-Lücke** (M6 „Offene Frage 1"): die vorhandenen Minimal-Structs per
  JSON-Roundtrip in `unstructured.Unstructured` wandeln und anwenden — vermeidet die
  schwere `prometheus-operator`-Go-Dependency.
- Neuer Schritt `reconcileMonitoring(ctx, vc)` in `Reconcile()` (nach den Services):
  - Nur wenn `spec.monitoring.serviceMonitor.enabled` bzw.
    `spec.monitoring.prometheusRules.enabled`.
  - **Nur wenn die `monitoring.coreos.com`-CRDs installiert sind** (RESTMapper-Check) —
    sonst überspringen mit Log + Status-Condition, **kein** Reconcile-Fehler. So brechen
    Cluster ohne prometheus-operator nicht.
  - `CreateOrUpdate` + OwnerReference auf die VinylCache.
- **PrometheusRule**: die 10 Alerts aus §8.5 bleiben; die 2, die auf die entfallenden
  Gauges zeigen, werden auf Exporter-Metriken umgeschrieben:
  - `VinylCacheLowHitRatio` →
    `rate(varnish_main_cache_hit[5m]) / (rate(varnish_main_cache_hit[5m]) + rate(varnish_main_cache_miss[5m])) < 0.5`
  - `VinylCacheBackendUnhealthy` → `varnish_backend_up == 0`
  - (Exakte Exporter-Metriknamen vor Implementierung gegen die Exporter-Version verifizieren.)
- **ServiceMonitor** zielt auf den Exporter-Port des Cache-Service (siehe Schicht 3).
- Der operator-eigene ServiceMonitor (`vinyl_*`) wird **nicht** pro Cache generiert,
  sondern im Helm-Chart (M7) verortet.

### Schicht 3 — Varnish-Exporter-Sidecar

- **CRD-Erweiterung** in `api/v1alpha1/vinylcache_types.go`:
  `MonitoringSpec.Exporter *ExporterSpec { Enabled bool; Image ImageSpec{Repository,Tag};
  Resources corev1.ResourceRequirements; Port int32 }`. Defaults:
  `ghcr.io/bluedynamics/varnish-exporter:1.6.1`, Port `9131` (gemäß `architektur.md` §8).
  `make generate manifests` für `zz_generated.deepcopy.go` + CRD-YAML.
- Sidecar in `internal/controller/statefulset.go`, angehängt an `Containers` wenn
  `exporter.enabled`:
  - mountet das vorhandene `varnish-workdir`-Volume (`/var/lib/varnish`) **readOnly** →
    teilt sich die VSM-Dateien mit varnishd.
  - non-root SecurityContext analog der anderen Container.
- Exporter-Port zusätzlich in den Varnish-Service aufnehmen, damit der ServiceMonitor ihn
  scrapen kann.

## Komponenten & Schnittstellen

| Einheit | Zweck | Abhängigkeiten |
|---------|-------|----------------|
| `monitoring.NewMetrics` | Registriert alle `vinyl_*` in eine `prometheus.Registerer` | controller-runtime metrics.Registry |
| `VinylCacheReconciler.Metrics` | nil-safe Metric-Hooks im Reconcile/Push | `*monitoring.Metrics` |
| `proxy.Server.Metrics` | nil-safe Metric-Hooks bei Invalidation/Broadcast | `*monitoring.Metrics` |
| `reconcileMonitoring` | Generiert+applied SM/PromRule als unstructured, CRD-gegated | RESTMapper, client |
| `ExporterSpec` (CRD) | Konfiguriert Sidecar | — |
| Exporter-Sidecar (StatefulSet) | Exponiert native `varnish_*` aus VSM | varnish-workdir-Volume |

## Tests

- **Unit:** Instrumentierung über eigene `prometheus.NewRegistry()` (Label-/Increment-
  Deltas); `unstructured`-Konvertierung; umgeschriebene Alert-Exprs; Sidecar-Präsenz/
  Mounts/Port in `statefulset_test`; `reconcileMonitoring` mit und ohne CRDs (envtest /
  fake RESTMapper).
- **Nil-safe:** Reconcile/Proxy-Tests mit `Metrics: nil` müssen grün bleiben.
- **E2E (chainsaw):** minimal halten (bestehender Parallel-Flake bekannt) — ggf. ein Test,
  der ServiceMonitor-Erzeugung bei `enabled` prüft.

## Doku & CHANGES

- `docs/sources/reference/metrics.md` (Reference: alle Metriken, Typ, Labels).
- `docs/sources/how-to/setup-monitoring.md` (How-To: prometheus-operator, opt-in, Grafana).
- CHANGES-Eintrag (jede Änderung inkl. Tests/Tooling braucht einen Changelog-Eintrag).

## Vor Implementierung zu verifizieren

- Exakte `prometheus_varnish_exporter`-Metriknamen, Default-Port und `-n`-Instanz-Handling
  (VSM-Pfad muss zu varnishds `-n` passen — varnishd nutzt hier Default-`-n`/hostname,
  der Sidecar teilt den Pod-Hostnamen, also passt der VSM-Pfad; vor Implementierung am
  laufenden Pod gegenprüfen).

## Bewusst nicht im Umfang

- Operator-eigener ServiceMonitor (`vinyl_*`) → Helm-Chart (M7).
- Mitgeliefertes Grafana-Dashboard-JSON → Helm-Chart (M7).
- Operator-seitiges Berechnen/Re-Publizieren der Hit-Ratio (durch Exporter-Entscheidung obsolet).
