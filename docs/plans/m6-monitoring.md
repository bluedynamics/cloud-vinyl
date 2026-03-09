# M6: Monitoring

**Status:** Bereit nach M1, parallel zu M4 und M5
**Voraussetzungen:** M1 (CRD-Typen für MonitoringSpec)
**Parallelisierbar mit:** M4, M5
**Geschätzte Team-Größe:** 0.5–1 Person

---

## Kontext

Monitoring ist kein nachträgliches Feature — es wird von Anfang an in den Operator integriert. Alle relevanten Metriken müssen bei jedem Reconcile, jedem VCL-Push und jedem Broadcast-Request aktualisiert werden. PrometheusRule und ServiceMonitor sind optionale generierte Kubernetes-Ressourcen.

**Relevante Architektur-Abschnitte:**
- §8.4 — Fehlende Metriken: vollständige Tabelle aller Metriken mit Namen, Typ, Labels, Beschreibung
- §8.5 — Alert-Definitionen: 10 PrometheusRule-Alerts mit Schwellwerten und Severity
- §3.2 — Operator-Komponenten: Metrics-Endpunkt auf Port 8080 (controller-runtime default)

---

## Ziel

Nach M6 existieren:
- Alle definierten Metriken registriert und von `/metrics` geliefert
- Optionale `PrometheusRule`-Generierung (alle 10 Alerts aus §8.5)
- Optionaler `ServiceMonitor`
- Metriken sind in M4 und M5 eingebaut (Injection via Interface)

---

## Package-Struktur

```
internal/monitoring/
├── metrics.go              # Prometheus-Metriken-Registry (alle Metriken definiert)
├── metrics_test.go         # Unit: alle Metriken registriert, Labels korrekt
├── prometheusrule.go       # PrometheusRule-Objekt generieren
├── prometheusrule_test.go  # Unit: alle 10 Alerts korrekt, Schwellwerte stimmen
└── servicemonitor.go       # ServiceMonitor-Objekt generieren
```

---

## Metriken-Registry

Vollständige Metriken-Liste in **architektur.md §8.4**. Hier die Struktur:

```go
// internal/monitoring/metrics.go

type Metrics struct {
    // VCL-Push
    VCLPushTotal    *prometheus.CounterVec   // labels: cache, namespace, result (success|error)
    VCLPushDuration prometheus.Histogram     // labels: cache, namespace

    // Invalidierung
    InvalidationTotal       *prometheus.CounterVec // labels: cache, namespace, type (purge|ban|xkey), result
    InvalidationDuration    prometheus.Histogram
    BroadcastTotal          *prometheus.CounterVec // labels: pod, result
    PartialFailureTotal     *prometheus.CounterVec // labels: cache, namespace

    // Cache-Zustand
    HitRatio        *prometheus.GaugeVec  // labels: cache, namespace
    BackendHealth   *prometheus.GaugeVec  // labels: cache, namespace, backend
    VCLVersions     *prometheus.GaugeVec  // labels: cache, namespace

    // Operator
    ReconcileTotal    *prometheus.CounterVec // labels: cache, namespace, result
    ReconcileDuration prometheus.Histogram
}

func NewMetrics(reg prometheus.Registerer) *Metrics {
    m := &Metrics{}
    m.VCLPushTotal = promauto.With(reg).NewCounterVec(prometheus.CounterOpts{
        Name: "vinyl_vcl_push_total",
        Help: "Total VCL push attempts",
    }, []string{"cache", "namespace", "result"})
    // ... alle weiteren Metriken
    return m
}
```

**Warum `prometheus.Registerer` als Parameter?** Tests können eine eigene Registry übergeben — keine globale Registry, keine Test-Isolation-Probleme.

---

## TDD-Workflow

### Schritt 1: Metriken-Registrierung testen

```go
// internal/monitoring/metrics_test.go

func TestMetrics_AllRegistered(t *testing.T) {
    reg := prometheus.NewRegistry()
    m := NewMetrics(reg)
    assert.NotNil(t, m.VCLPushTotal)
    // ... alle Felder

    // Über Registry prüfen ob alle Metriken gesammelt werden
    mfs, err := reg.Gather()
    require.NoError(t, err)
    names := make(map[string]bool)
    for _, mf := range mfs {
        names[mf.GetName()] = true
    }
    assert.True(t, names["vinyl_vcl_push_total"])
    // ... alle erwarteten Namen
}

func TestMetrics_Labels_Correct(t *testing.T) {
    reg := prometheus.NewRegistry()
    m := NewMetrics(reg)
    m.VCLPushTotal.WithLabelValues("my-cache", "production", "success").Inc()
    // Prüfen ob Label-Kombination vorhanden
}
```

### Schritt 2: PrometheusRule-Inhalt testen

```go
// internal/monitoring/prometheusrule_test.go

func TestPrometheusRule_Contains10Alerts(t *testing.T) {
    rule := GeneratePrometheusRule("cloud-vinyl")
    assert.Len(t, rule.Spec.Groups[0].Rules, 10)
}

func TestPrometheusRule_VCLSyncFailed_CorrectThreshold(t *testing.T) {
    rule := GeneratePrometheusRule("cloud-vinyl")
    alert := findAlert(rule, "VinylCacheVCLSyncFailed")
    assert.Contains(t, alert.Expr.String(), "vinyl_vcl_push_errors_total")
}

// Alle 10 Alerts aus architektur.md §8.5 testen
```

---

## PrometheusRule-Generierung

Alle 10 Alert-Definitionen stehen in **architektur.md §8.5**. Beispiel-Struktur:

```go
// internal/monitoring/prometheusrule.go

func GeneratePrometheusRule(namespace string) *monitoringv1.PrometheusRule {
    return &monitoringv1.PrometheusRule{
        ObjectMeta: metav1.ObjectMeta{
            Name:      "cloud-vinyl-alerts",
            Namespace: namespace,
        },
        Spec: monitoringv1.PrometheusRuleSpec{
            Groups: []monitoringv1.RuleGroup{{
                Name: "cloud-vinyl",
                Rules: []monitoringv1.Rule{
                    {
                        Alert: "VinylCacheVCLSyncFailed",
                        Expr:  intstr.FromString(`rate(vinyl_vcl_push_errors_total[5m]) > 0`),
                        For:   ptr("5m"),
                        Labels: map[string]string{"severity": "warning"},
                        Annotations: map[string]string{
                            "summary": "VCL sync failed on {{ $labels.cache }}",
                        },
                    },
                    // ... 9 weitere Alerts aus architektur.md §8.5
                },
            }},
        },
    }
}
```

---

## Integration in M4 und M5

Metriken werden per Dependency-Injection in Controller und Proxy eingebaut:

```go
// In M4: internal/controller/reconciler.go
type VinylCacheReconciler struct {
    // ...
    Metrics *monitoring.Metrics  // nil-safe: alle Metric-Calls prüfen auf nil
}

// In M5: internal/proxy/server.go
type ProxyServer struct {
    // ...
    Metrics *monitoring.Metrics
}
```

`nil`-Safe-Pattern: Wenn Monitoring deaktiviert ist (`spec.monitoring.enabled: false`), wird `nil` übergeben. Alle Metric-Aufrufe müssen nil-sicher sein:

```go
if r.Metrics != nil {
    r.Metrics.VCLPushTotal.WithLabelValues(vc.Name, vc.Namespace, "success").Inc()
}
```

---

## Dokumentations-Deliverables

| Datei | Diataxis | Inhalt | Wann |
|-------|----------|--------|------|
| `docs/sources/reference/metrics.md` | Reference | Alle Metriken: Name, Typ, Labels, Beschreibung, Beispielwert | Nach Implementierung |
| `docs/sources/how-to/setup-monitoring.md` | How-To | Prometheus-Operator, PrometheusRule opt-in, Grafana-Dashboard-Vorlage | Nach M6 |

---

## Akzeptanzkriterien

- [ ] Alle Metriken aus §8.4 registriert und in `/metrics` sichtbar
- [ ] PrometheusRule enthält alle 10 Alerts aus §8.5 mit korrekten Schwellwerten
- [ ] Metriken-Labels korrekt (cache, namespace, result etc.)
- [ ] Nil-safe: Tests mit `Metrics: nil` schlagen nicht fehl
- [ ] Coverage `internal/monitoring` ≥ 90%

---

## Offene Fragen

1. **`monitoring.coreos.com/v1` CRD:** Die PrometheusRule-Typen kommen aus `github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1`. Diese Dependency einbinden. Alternativ: eigene minimale Structs definieren um die schwere Dependency zu vermeiden — aber dann kein `kubectl apply` von Go aus möglich.
2. **Grafana-Dashboard:** Soll ein vordefiniertes Grafana-Dashboard als JSON-ConfigMap mitgeliefert werden? Wenn ja, als Helm-Chart-Bestandteil (M7), nicht hier.
