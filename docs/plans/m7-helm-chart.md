# M7: Helm Chart

**Status:** Bereit nach M4, M5, M6
**Voraussetzungen:** M4 (Operator-Deployment-Spec bekannt), M5 (Proxy-Port bekannt), M6 (Monitoring opt-in)
**Parallelisierbar mit:** M8 (E2E-Tests starten sobald Chart installierbar ist)
**Geschätzte Team-Größe:** 1 Person

---

## Kontext

Das Helm Chart ist die primäre Installationsmethode für cloud-vinyl in Produktionsclustern. Es kapselt alle Kubernetes-Ressourcen des Operators (Deployment, RBAC, Webhook, CRDs, ServiceMonitor, PrometheusRule) hinter konfigurierbaren Values mit JSON-Schema-Validierung.

**Relevante Architektur-Abschnitte:**
- §3.2 — Operator-Komponenten: Deployment, Ports (8080 Metriken, 8090 Proxy, 9443 Webhook)
- §7.5 — Leader-Election: optionales Feature, per Flag steuerbar
- §8.3 — Container-Images: `ghcr.io/bluedynamics/cloud-vinyl-operator` und `cloud-vinyl-varnish`
- §8.5 — Alert-Definitionen: PrometheusRule opt-in
- §10.2 — RBAC-Implikationen: ClusterRole-Scope nötig (cluster-weiter Operator)
- §10.4 — NetworkPolicies: Operator erzeugt sie pro VinylCache, Webhook-Endpoint braucht selbst keine NP
- §10.6 — Admission Webhook: TLS-Zertifikat für Webhook-Server nötig (cert-manager oder manuell)

---

## Ziel

Nach M7 existiert:
- Vollständiges Helm Chart das `helm install` in einem leeren kind-Cluster übersteht
- JSON-Schema-Validierung für alle konfigurierbaren Values
- helm unittest für alle Templates
- cert-manager-Integration (opt-in) und manuelle TLS-Option
- CRD-Installation als Chart-Subchart oder via `--set installCRDs=true`

---

## Chart-Struktur

```
charts/cloud-vinyl/
├── Chart.yaml                # Name, Version, appVersion, dependencies
├── values.yaml               # Alle Defaults
├── values.schema.json        # JSON Schema (strict: additionalProperties: false)
├── README.md                 # Kurzreferenz für artifacthub.io
├── templates/
│   ├── _helpers.tpl          # Gemeinsame Labels, Namen, Selector-Helpers
│   ├── deployment.yaml       # Operator-Deployment
│   ├── serviceaccount.yaml   # ServiceAccount
│   ├── clusterrole.yaml      # ClusterRole (alle Ressourcen die der Operator verwaltet)
│   ├── clusterrolebinding.yaml
│   ├── service.yaml          # Service für Metriken (Port 8080) und Webhook (Port 9443)
│   ├── prometheusrule.yaml   # PrometheusRule (opt-in: monitoring.prometheusRules.enabled)
│   ├── servicemonitor.yaml   # ServiceMonitor (opt-in: monitoring.serviceMonitor.enabled)
│   ├── certificate.yaml      # cert-manager Certificate (opt-in: webhook.certManager.enabled)
│   ├── issuer.yaml           # SelfSigned Issuer (opt-in)
│   ├── validatingwebhook.yaml # ValidatingWebhookConfiguration
│   ├── mutatingwebhook.yaml   # MutatingWebhookConfiguration
│   └── NOTES.txt             # Post-install Hinweise
├── crds/
│   └── vinylcache.yaml       # CRD-Manifest (via make generate → config/crd/)
└── tests/
    └── helm-unittest/
        ├── deployment_test.yaml
        ├── rbac_test.yaml
        ├── webhook_test.yaml
        ├── monitoring_test.yaml
        └── certmanager_test.yaml
```

---

## Values-Struktur

```yaml
# charts/cloud-vinyl/values.yaml

# Operator-Deployment
replicaCount: 1

image:
  operator:
    repository: ghcr.io/bluedynamics/cloud-vinyl-operator
    tag: ""          # Defaults auf Chart appVersion
    pullPolicy: IfNotPresent
  varnish:
    repository: ghcr.io/bluedynamics/cloud-vinyl-varnish
    tag: "7.6"       # Pinned: explizit setzen für Produktionsbetrieb

imagePullSecrets: []
nameOverride: ""
fullnameOverride: ""

serviceAccount:
  create: true
  annotations: {}
  name: ""

# Ressourcen-Requests/Limits für den Operator-Pod
resources:
  requests:
    cpu: "100m"
    memory: "128Mi"
  limits:
    cpu: "500m"
    memory: "256Mi"

# Leader-Election (empfohlen wenn replicaCount > 1)
leaderElection:
  enabled: true

# Operator-Flags
operatorFlags:
  metricsAddr: ":8080"
  probeAddr: ":8081"
  agentClientTimeout: "30s"

# Webhook-TLS (einer der beiden Ansätze muss gewählt werden)
webhook:
  certManager:
    enabled: true       # Empfohlen: cert-manager muss im Cluster installiert sein
  # Falls certManager.enabled: false → manuelle TLS-Konfiguration:
  tls:
    caCert: ""          # Base64-kodiertes CA-Zertifikat
    cert: ""            # Base64-kodiertes TLS-Zertifikat
    key: ""             # Base64-kodierter Private Key

# Monitoring
monitoring:
  prometheusRules:
    enabled: false      # PrometheusRule für 10 Alerts (architektur.md §8.5)
  serviceMonitor:
    enabled: false      # ServiceMonitor für Prometheus-Operator
    interval: "30s"
    scrapeTimeout: "10s"
    additionalLabels: {}

# CRD-Installation
installCRDs: true

# Pod-Scheduling
nodeSelector: {}
tolerations: []
affinity: {}

# Pod-Security-Context (Non-Root by Default, §1.2)
podSecurityContext:
  runAsNonRoot: true
  runAsUser: 65532
  fsGroup: 65532

securityContext:
  allowPrivilegeEscalation: false
  readOnlyRootFilesystem: true
  capabilities:
    drop:
      - ALL
```

---

## ClusterRole

Der Operator braucht Lese-/Schreibrechte auf alle Ressourcen, die er verwaltet. Vollständige RBAC-Anforderungen aus §10.2:

```yaml
# templates/clusterrole.yaml

rules:
  # Eigene CRD
  - apiGroups: ["vinyl.bluedynamics.eu"]
    resources: ["vinylcaches", "vinylcaches/status", "vinylcaches/finalizers", "vinylcaches/scale"]
    verbs: ["*"]

  # Kubernetes-Ressourcen die der Operator erzeugt
  - apiGroups: ["apps"]
    resources: ["statefulsets"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: [""]
    resources: ["pods", "pods/status", "services", "endpoints", "secrets", "serviceaccounts", "events"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["discovery.k8s.io"]
    resources: ["endpointslices"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["networking.k8s.io"]
    resources: ["networkpolicies"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]

  # Leader-Election
  - apiGroups: ["coordination.k8s.io"]
    resources: ["leases"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]

  # Events schreiben
  - apiGroups: [""]
    resources: ["events"]
    verbs: ["create", "patch"]
```

---

## Webhook-TLS: cert-manager vs. manuell

### Option A: cert-manager (empfohlen)

```yaml
# templates/certificate.yaml (nur wenn webhook.certManager.enabled: true)
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: {{ include "cloud-vinyl.fullname" . }}-webhook-cert
spec:
  secretName: {{ include "cloud-vinyl.fullname" . }}-webhook-tls
  issuerRef:
    name: {{ include "cloud-vinyl.fullname" . }}-selfsigned
    kind: Issuer
  dnsNames:
    - {{ include "cloud-vinyl.fullname" . }}-webhook-service.{{ .Release.Namespace }}.svc
    - {{ include "cloud-vinyl.fullname" . }}-webhook-service.{{ .Release.Namespace }}.svc.cluster.local
```

```yaml
# templates/validatingwebhook.yaml
annotations:
  cert-manager.io/inject-ca-from: {{ .Release.Namespace }}/{{ include "cloud-vinyl.fullname" . }}-webhook-cert
```

### Option B: Manuelles TLS

CA-Zertifikat wird als `caBundle` direkt in die WebhookConfiguration geschrieben. TLS-Secret wird aus den Values erstellt.

**Test:** Beide Pfade müssen durch helm unittest und eine dedizierte CI-Job-Matrix abgedeckt sein.

---

## TDD-Workflow

### Schritt 1: helm unittest Grundstruktur

```yaml
# charts/cloud-vinyl/tests/helm-unittest/deployment_test.yaml

suite: Operator Deployment Tests
templates:
  - deployment.yaml
tests:
  - it: sollte ein Deployment erstellen
    asserts:
      - isKind:
          of: Deployment
      - equal:
          path: spec.template.spec.containers[0].image
          value: ghcr.io/bluedynamics/cloud-vinyl-operator:latest

  - it: sollte Non-Root Security Context setzen
    asserts:
      - equal:
          path: spec.template.spec.securityContext.runAsNonRoot
          value: true

  - it: sollte readOnlyRootFilesystem setzen
    asserts:
      - equal:
          path: spec.template.spec.containers[0].securityContext.readOnlyRootFilesystem
          value: true

  - it: sollte Leader-Election-Flag setzen wenn aktiviert
    set:
      leaderElection.enabled: true
    asserts:
      - contains:
          path: spec.template.spec.containers[0].args
          content: "--leader-elect=true"
```

### Schritt 2: RBAC-Tests

```yaml
# charts/cloud-vinyl/tests/helm-unittest/rbac_test.yaml

suite: RBAC Tests
tests:
  - it: ClusterRole enthält VinylCache-Permissions
    template: clusterrole.yaml
    asserts:
      - contains:
          path: rules
          content:
            apiGroups: ["vinyl.bluedynamics.eu"]
            resources: ["vinylcaches"]

  - it: ClusterRoleBinding verknüpft ServiceAccount
    template: clusterrolebinding.yaml
    asserts:
      - equal:
          path: subjects[0].kind
          value: ServiceAccount
```

### Schritt 3: Monitoring opt-in Tests

```yaml
# charts/cloud-vinyl/tests/helm-unittest/monitoring_test.yaml

suite: Monitoring Tests
tests:
  - it: PrometheusRule wird nicht gerendert wenn deaktiviert
    template: prometheusrule.yaml
    set:
      monitoring.prometheusRules.enabled: false
    asserts:
      - hasDocuments:
          count: 0

  - it: PrometheusRule enthält 10 Alerts wenn aktiviert
    template: prometheusrule.yaml
    set:
      monitoring.prometheusRules.enabled: true
    asserts:
      - isKind:
          of: PrometheusRule
      - lengthEqual:
          path: spec.groups[0].rules
          count: 10
```

### Schritt 4: cert-manager Conditional Tests

```yaml
# charts/cloud-vinyl/tests/helm-unittest/certmanager_test.yaml

suite: cert-manager Tests
tests:
  - it: Certificate wird nur gerendert wenn cert-manager enabled
    template: certificate.yaml
    set:
      webhook.certManager.enabled: false
    asserts:
      - hasDocuments:
          count: 0

  - it: ValidatingWebhook hat cert-manager Annotation wenn enabled
    template: validatingwebhook.yaml
    set:
      webhook.certManager.enabled: true
    asserts:
      - isNotEmpty:
          path: metadata.annotations["cert-manager.io/inject-ca-from"]
```

---

## JSON Schema Validierung

```json
// values.schema.json (Auszug)
{
  "$schema": "http://json-schema.org/draft-07/schema",
  "type": "object",
  "additionalProperties": false,
  "required": ["image"],
  "properties": {
    "replicaCount": {
      "type": "integer",
      "minimum": 1,
      "maximum": 10,
      "description": "Anzahl Operator-Replicas. >1 erfordert leaderElection.enabled: true"
    },
    "leaderElection": {
      "type": "object",
      "properties": {
        "enabled": {"type": "boolean"}
      }
    },
    "webhook": {
      "type": "object",
      "properties": {
        "certManager": {
          "type": "object",
          "properties": {
            "enabled": {"type": "boolean"}
          }
        }
      }
    },
    "monitoring": {
      "type": "object",
      "properties": {
        "prometheusRules": {
          "type": "object",
          "properties": {
            "enabled": {"type": "boolean"}
          }
        },
        "serviceMonitor": {
          "type": "object",
          "properties": {
            "enabled": {"type": "boolean"},
            "interval": {"type": "string", "pattern": "^[0-9]+(s|m|h)$"}
          }
        }
      }
    }
  }
}
```

---

## CI-Integration

```yaml
# .github/workflows/ci.yml (Helm-spezifische Jobs)

helm-lint:
  runs-on: ubuntu-latest
  steps:
    - uses: azure/setup-helm@v4
    - run: helm lint charts/cloud-vinyl/

helm-unittest:
  runs-on: ubuntu-latest
  steps:
    - run: helm plugin install https://github.com/helm-unittest/helm-unittest
    - run: helm unittest charts/cloud-vinyl/

helm-install-kind:
  runs-on: ubuntu-latest
  steps:
    - uses: helm/kind-action@v1
    - run: |
        # cert-manager installieren (Prerequisite für Webhook-TLS)
        helm install cert-manager jetstack/cert-manager \
          --namespace cert-manager --create-namespace \
          --set installCRDs=true
        # cloud-vinyl installieren
        helm install cloud-vinyl ./charts/cloud-vinyl \
          --namespace cloud-vinyl-system --create-namespace \
          --set webhook.certManager.enabled=true \
          --wait --timeout 120s
    - run: |
        # Smoke-Test: Operator läuft
        kubectl get deployment -n cloud-vinyl-system cloud-vinyl-operator
        # Smoke-Test: CRD vorhanden
        kubectl get crd vinylcaches.vinyl.bluedynamics.eu
```

---

## Dokumentations-Deliverables

| Datei | Diataxis | Inhalt | Wann |
|-------|----------|--------|------|
| `docs/sources/how-to/install.md` | How-To | Helm-Installation, Prerequisiten (cert-manager), Namespace-Setup | Nach Chart |
| `docs/sources/how-to/upgrade.md` | How-To | Upgrade-Prozess, CRD-Migration, Rollback | Nach Chart |
| `docs/sources/reference/helm-values.md` | Reference | Vollständige Values-Referenz (generiert aus values.schema.json) | Nach Chart |

---

## Akzeptanzkriterien

- [ ] `helm lint` ohne Warnungen
- [ ] `helm unittest` alle Tests grün
- [ ] `helm install` in leerem kind-Cluster mit cert-manager: Operator läuft, Webhook registered
- [ ] `helm install` ohne cert-manager (manuelle TLS Values): funktioniert
- [ ] `values.schema.json` lehnt ungültige Values ab (z. B. `replicaCount: 0`, ungültiges Interval-Format)
- [ ] `helm upgrade` von vorheriger Chart-Version: kein Downtime für laufende VinylCaches
- [ ] Alle Templates durch helm unittest abgedeckt (100% Template-Coverage)
- [ ] Non-Root Security Context in Deployment gesetzt
- [ ] `installCRDs: false` → CRDs werden nicht gerendert (für GitOps-Workflows)

---

## Offene Fragen

1. **CRD-Lifecycle:** Helm löscht CRDs beim `helm uninstall` standardmäßig nicht (seit Helm 3 — CRDs in `crds/` werden nie gelöscht). Das ist korrekt für Produktionsbetrieb. Für Test-Teardown braucht CI einen expliziten `kubectl delete crd` Schritt.
2. **Webhook-Readiness:** Der Operator-Pod muss ready sein bevor die WebhookConfiguration aktiv wird, sonst lehnt Kubernetes alle API-Requests ab die den Webhook betreffen. Helm chart-hooks (`post-install`) können helfen — oder `--wait` in der Installation.
3. **OCI Registry:** Chart kann als OCI-Artifact in `ghcr.io/bluedynamics/cloud-vinyl-chart` gepusht werden (ab Helm 3.8). Alternative: ArtifactHub.io Listing.
