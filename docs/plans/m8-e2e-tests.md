# M8: E2E Tests

**Status:** Bereit nach M0–M7
**Voraussetzungen:** Alle vorhergehenden Module (M0–M7)
**Parallelisierbar mit:** Dokumentations-Finalisierung
**Geschätzte Team-Größe:** 1 Person

---

## Kontext

E2E-Tests sind das Release-Gate für cloud-vinyl. Sie laufen gegen einen echten Kubernetes-Cluster (via kind) mit dem vollständig installierten Helm Chart. Sie testen das System als Black-Box — kein Zugriff auf interne Go-Strukturen.

**Relevante Architektur-Abschnitte:**
- §7.2 — Reconcile-Loop: 11 Schritte die alle in E2E abgedeckt werden müssen
- §7.3 — Fehlerbehandlung: VCL-Push-Fehler, Kompilierungsfehler, Backend-Down
- §7.5 — Leader-Election: Proxy läuft auf allen Replicas, Controller nur auf Leader
- §7.7 — Blast-Radius: canary/pause/rollback Annotations
- §7.8 — Drift-Erkennung: periodischer Reconcile erkennt manuelle Änderungen
- §5.2 — Shard-Director: warmup=0.1, rampup=30s — Verhalten bei Scale-Up
- §6.4 — Purge/BAN-Protokoll: 200/207/503 Response-Format bei Broadcast
- §6.5 — Sicherheit: Source-IP-Check, Host-Header-Routing

---

## Ziel

Nach M8 existiert:
- Vollständige E2E-Testsuite mit 8 Szenarien (chainsaw)
- CI-Job der alle Tests in einem kind-Cluster ausführt
- Quickstart-Tutorial (Docs) das den E2E-Aufbau als Grundlage nutzt

---

## Tool: Chainsaw (Kyverno)

Deklarative E2E-Tests ohne Go-Testcode. YAML-basiert, Git-diffbar, wartungsarm:

```yaml
# e2e/tests/basic-lifecycle/chainsaw-test.yaml
apiVersion: chainsaw.kyverno.io/v1alpha1
kind: Test
metadata:
  name: basic-lifecycle
spec:
  timeouts:
    apply: 5s
    assert: 120s
    delete: 30s
  steps:
    - name: apply-vinylcache
      try:
        - apply:
            file: vinylcache.yaml
    - name: wait-ready
      try:
        - assert:
            file: expected-ready.yaml
    - name: cleanup
      try:
        - delete:
            file: vinylcache.yaml
```

**Installation:** `go install github.com/kyverno/chainsaw@latest` oder via Makefile-Target `make setup-e2e`.

---

## Verzeichnisstruktur

```
e2e/
├── setup/
│   ├── kind-config.yaml          # kind-Cluster-Konfiguration (multi-node)
│   ├── install-cert-manager.sh   # cert-manager installieren
│   └── install-operator.sh       # Helm Chart installieren
│
├── fixtures/
│   ├── backends/
│   │   ├── echo-service.yaml     # Einfacher echo-Server als Backend
│   │   └── httpbin.yaml          # httpbin für Header-Tests
│   └── vinylcaches/
│       ├── minimal.yaml          # 1 Replica, 1 Backend
│       ├── standard.yaml         # 3 Replicas, 2 Backends, Shard-Director
│       └── full-featured.yaml    # Alle Features aktiviert
│
└── tests/
    ├── basic-lifecycle/
    ├── cluster-routing/
    ├── purge-broadcast/
    ├── xkey-invalidation/
    ├── vcl-validation/
    ├── ha-operator/
    ├── scaling/
    └── drift-detection/
```

---

## Test-Szenarien

### Szenario 1: Basic Lifecycle

**Ziel:** Vollständiger CRUD-Zyklus eines VinylCache-Objekts.

```
e2e/tests/basic-lifecycle/
├── chainsaw-test.yaml
├── vinylcache.yaml          # 1 Replica, 1 Backend
├── expected-ready.yaml      # VinylCache.status.phase == "Ready"
├── expected-resources.yaml  # StatefulSet, Services, EndpointSlice vorhanden
└── expected-deleted.yaml    # Alle Ressourcen weg nach Deletion
```

```yaml
# chainsaw-test.yaml
spec:
  steps:
    - name: create-vinylcache
      try:
        - apply:
            file: vinylcache.yaml
        - assert:
            file: expected-resources.yaml      # StatefulSet, Services erscheinen

    - name: wait-ready
      try:
        - assert:
            file: expected-ready.yaml          # phase: Ready, VCLSynced: True

    - name: verify-vcl-active
      try:
        - script:
            content: |
              POD=$(kubectl get pod -n e2e-test -l vinyl.bluedynamics.eu/cache=my-cache -o jsonpath='{.items[0].metadata.name}')
              kubectl exec -n e2e-test $POD -c vinyl-agent -- \
                curl -s -H "Authorization: Bearer $(cat /run/vinyl/agent-token)" \
                http://localhost:9090/vcl/active | jq -r '.name' | grep -q "cloud-vinyl"

    - name: update-spec
      try:
        - patch:
            file: vinylcache-updated.yaml     # Backend weight geändert
        - assert:
            file: expected-vcl-synced.yaml    # VCLSynced: True nach Update

    - name: delete-vinylcache
      try:
        - delete:
            file: vinylcache.yaml
        - assert:
            file: expected-deleted.yaml        # Alle Ressourcen weg (inkl. EndpointSlice)
```

**Bezug:** §7.2 (Reconcile-Loop vollständig), §7.2 Schritt 11 (Status schreiben)

---

### Szenario 2: Cluster-Routing (Shard-Director)

**Ziel:** 3-Replica-Cluster mit Shard-Director funktioniert korrekt — URLs werden konsistent zum selben Pod geroutet.

```
e2e/tests/cluster-routing/
├── chainsaw-test.yaml
├── vinylcache-3replicas.yaml    # replicas: 3, director: shard
└── expected-shard-consistent.yaml
```

```yaml
spec:
  steps:
    - name: create-cluster
      try:
        - apply:
            file: vinylcache-3replicas.yaml
        - assert:
            file: expected-ready.yaml          # Alle 3 Pods ready

    - name: verify-shard-routing
      try:
        - script:
            content: |
              # Gleiche URL 10x anfragen → immer gleicher Pod antwortet
              SVC=$(kubectl get svc -n e2e-test my-cache -o jsonpath='{.spec.clusterIP}')
              FIRST_POD=""
              for i in $(seq 1 10); do
                POD=$(curl -s -H "X-Forwarded-For: 10.0.0.1" \
                  http://$SVC/product/123 -D - | grep x-served-by | awk '{print $2}')
                if [ -z "$FIRST_POD" ]; then FIRST_POD=$POD; fi
                [ "$POD" = "$FIRST_POD" ] || (echo "Shard routing inconsistent!" && exit 1)
              done

    - name: verify-warmup-rampup
      try:
        - script:
            content: |
              # Nach Scale-Up: neuer Pod erhält nur ~10% der Requests initial (warmup: 0.1)
              # Verifikation: VCL enthält korrekten warmup-Wert
              POD=$(kubectl get pod -n e2e-test -l vinyl.bluedynamics.eu/cache=my-cache \
                -o jsonpath='{.items[0].metadata.name}')
              kubectl exec -n e2e-test $POD -c varnish -- \
                varnishadm vcl.show active | grep -q "warmup=0.1"
```

**Bezug:** §5.2 (Shard-Director, warmup=0.1, rampup=30s)

---

### Szenario 3: Purge Broadcast (200/207/503)

**Ziel:** Broadcast-Verhalten und Response-Format bei PURGE-Requests verifizieren.

```
e2e/tests/purge-broadcast/
├── chainsaw-test.yaml
├── vinylcache-3replicas.yaml
└── scripts/
    ├── send-purge.sh
    └── simulate-pod-failure.sh
```

```yaml
spec:
  steps:
    - name: setup
      try:
        - apply:
            file: vinylcache-3replicas.yaml
        - assert:
            file: expected-ready.yaml

    - name: purge-all-success-200
      try:
        - script:
            content: |
              PURGE_SVC=$(kubectl get svc -n e2e-test my-cache-invalidation \
                -o jsonpath='{.spec.clusterIP}')
              STATUS=$(curl -s -o /dev/null -w "%{http_code}" \
                -X PURGE http://$PURGE_SVC/product/123 \
                -H "Host: my-cache-invalidation.e2e-test.svc.cluster.local")
              [ "$STATUS" = "200" ] || (echo "Expected 200, got $STATUS" && exit 1)

    - name: simulate-partial-failure-207
      try:
        - script:
            content: |
              # Einen Pod suspendieren (kein echtes Löschen — sonst Reconcile-Trigger)
              kubectl exec -n e2e-test my-cache-0 -c varnish -- \
                kill -STOP 1  # SIGSTOP → Pod reagiert nicht mehr auf HTTP
              sleep 2
              STATUS=$(curl -s -o /dev/null -w "%{http_code}" \
                -X PURGE http://$PURGE_SVC/product/123 \
                -H "Host: my-cache-invalidation.e2e-test.svc.cluster.local")
              kubectl exec -n e2e-test my-cache-0 -c varnish -- kill -CONT 1
              [ "$STATUS" = "207" ] || (echo "Expected 207, got $STATUS" && exit 1)
```

**Bezug:** §6.4 (Response-Format 200/207/503), §6.5 (Host-Header-Routing)

---

### Szenario 4: xkey-Invalidierung

**Ziel:** xkey-Purge via Operator-Proxy funktioniert end-to-end.

```yaml
spec:
  steps:
    - name: cache-item-with-xkey
      try:
        - script:
            content: |
              # Item mit X-Cache-Tags Header cachen
              # (Backend muss X-Xkey Header setzen — httpbin fixture)
              SVC_IP=$(kubectl get svc -n e2e-test my-cache -o jsonpath='{.spec.clusterIP}')
              curl -s http://$SVC_IP/ -H "X-Xkey: article-123 category-sports"
              # Zweite Anfrage → muss aus Cache kommen (Age > 0)
              AGE=$(curl -s -I http://$SVC_IP/ | grep -i "^age:" | awk '{print $2}')
              [ "$AGE" -gt "0" ] || (echo "Item not cached" && exit 1)

    - name: purge-by-xkey
      try:
        - script:
            content: |
              PURGE_SVC=$(kubectl get svc -n e2e-test my-cache-invalidation \
                -o jsonpath='{.spec.clusterIP}')
              curl -s -X POST http://$PURGE_SVC/purge/xkey \
                -H "Host: my-cache-invalidation.e2e-test.svc.cluster.local" \
                -H "Content-Type: application/json" \
                -d '{"keys": ["article-123"]}' | jq -e '.status == "ok"'

    - name: verify-cache-miss-after-purge
      try:
        - script:
            content: |
              AGE=$(curl -s -I http://$SVC_IP/ | grep -i "^age:" | awk '{print $2}')
              [ "$AGE" = "0" ] || [ -z "$AGE" ] || (echo "Item still cached" && exit 1)
```

**Bezug:** §6.4 (xkey via Agent), §3.4 (VCL-Funktion nicht Admin-Protokoll)

---

### Szenario 5: VCL-Validierung (Webhook)

**Ziel:** Syntaxfehler in VCL-Snippets werden vom Webhook vor der Speicherung abgelehnt.

```yaml
spec:
  steps:
    - name: reject-invalid-vcl-snippet
      try:
        - apply:
            file: vinylcache-invalid-vcl.yaml
          catch:
            - error:
                message: ".*VCL compilation failed.*|.*invalid VCL.*"

    - name: reject-forbidden-varnish-param
      try:
        - apply:
            file: vinylcache-forbidden-param.yaml   # vcc_allow_inline_c: "on"
          catch:
            - error:
                message: ".*varnishParameters.*not allowed.*"

    - name: reject-invalid-cidr
      try:
        - apply:
            file: vinylcache-invalid-cidr.yaml      # allowedSources: ["not-an-ip"]
          catch:
            - error:
                message: ".*invalid CIDR.*"

    - name: accept-valid-vinylcache
      try:
        - apply:
            file: vinylcache-valid.yaml
        - assert:
            file: expected-created.yaml
```

**Bezug:** §10.6 (Webhook-Validierung, CEL-Regeln)

---

### Szenario 6: HA-Operator (Leader-Election)

**Ziel:** 2 Operator-Replicas — nur einer ist Leader (Controller), beide laufen den Proxy.

```yaml
spec:
  steps:
    - name: install-ha-operator
      try:
        - script:
            content: |
              helm upgrade cloud-vinyl ./charts/cloud-vinyl \
                --set replicaCount=2 \
                --set leaderElection.enabled=true \
                --wait

    - name: verify-leader-election
      try:
        - script:
            content: |
              # Exakt ein Leader-Lease vorhanden
              kubectl get lease -n cloud-vinyl-system cloud-vinyl-leader-election \
                -o jsonpath='{.spec.holderIdentity}' | grep -q "cloud-vinyl"

    - name: verify-proxy-on-both-pods
      try:
        - script:
            content: |
              # Beide Pods antworten auf Port 8090 (Proxy)
              for POD in $(kubectl get pods -n cloud-vinyl-system -l app=cloud-vinyl \
                -o jsonpath='{.items[*].metadata.name}'); do
                STATUS=$(kubectl exec -n cloud-vinyl-system $POD -- \
                  curl -s -o /dev/null -w "%{http_code}" http://localhost:8090/health)
                [ "$STATUS" = "200" ] || (echo "Proxy not running on $POD" && exit 1)
              done

    - name: kill-leader-and-verify-failover
      try:
        - delete:
            resource:
              apiVersion: v1
              kind: Pod
              labelSelector:
                vinyl.bluedynamics.eu/role: leader    # falls vorhanden
        - script:
            content: |
              # Kurze Pause für Leader-Election
              sleep 15
              # Neuer Leader übernimmt — VinylCache bleibt Ready
              kubectl get vinylcache -n e2e-test my-cache \
                -o jsonpath='{.status.phase}' | grep -q "Ready"
```

**Bezug:** §7.5 (Leader-Election, Proxy auf allen Replicas)

---

### Szenario 7: Scaling

**Ziel:** Scale-Up von 1→3 und Scale-Down 3→1 funktioniert — VCL wird aktualisiert, kein Datenverlust.

```yaml
spec:
  steps:
    - name: start-single-replica
      try:
        - apply:
            file: vinylcache-1replica.yaml
        - assert:
            file: expected-ready-1.yaml         # 1/1 ready

    - name: scale-to-3
      try:
        - patch:
            resource:
              apiVersion: vinyl.bluedynamics.eu/v1alpha1
              kind: VinylCache
              name: my-cache
            patch: |
              {"spec": {"replicas": 3}}
        - assert:
            file: expected-ready-3.yaml         # 3/3 ready
        - script:
            content: |
              # VCL enthält jetzt 3 Peer-Backends
              kubectl exec -n e2e-test my-cache-0 -c varnish -- \
                varnishadm vcl.show active | grep -c "backend peer_" | grep -q "3"

    - name: scale-to-1
      try:
        - patch:
            resource:
              apiVersion: vinyl.bluedynamics.eu/v1alpha1
              kind: VinylCache
              name: my-cache
            patch: |
              {"spec": {"replicas": 1}}
        - assert:
            file: expected-ready-1.yaml
        - script:
            content: |
              # VCL enthält jetzt 1 Peer-Backend (oder keinen Cluster-Block)
              kubectl exec -n e2e-test my-cache-0 -c varnish -- \
                varnishadm vcl.show active | grep -c "backend peer_" | grep -qE "^0$|^1$"
```

**Bezug:** §7.4 (Debouncing bei Scale-Events), §5.2 (Shard-Director Warmup)

---

### Szenario 8: Drift-Erkennung

**Ziel:** Wenn ein Varnish-Pod manuell auf eine andere VCL gewechselt wird, erkennt der Operator die Drift und korrigiert sie.

```yaml
spec:
  steps:
    - name: setup
      try:
        - apply:
            file: vinylcache-standard.yaml
        - assert:
            file: expected-ready.yaml

    - name: simulate-drift
      try:
        - script:
            content: |
              # Direkt via Agent eine andere VCL laden (simuliert manuellen Eingriff)
              TOKEN=$(kubectl get secret -n e2e-test my-cache-agent-token \
                -o jsonpath='{.data.token}' | base64 -d)
              AGENT_IP=$(kubectl get pod -n e2e-test my-cache-0 \
                -o jsonpath='{.status.podIP}')
              curl -s -X POST http://$AGENT_IP:9090/vcl/push \
                -H "Authorization: Bearer $TOKEN" \
                -H "Content-Type: application/json" \
                -d '{"name": "manual-override", "vcl": "vcl 4.1;\ndefault: return(pass);"}'
              # Jetzt weicht der aktive VCL-Hash vom status.activeVCL.hash ab

    - name: wait-for-drift-detection
      try:
        - script:
            content: |
              # Operator erkennt Drift beim nächsten periodischen Reconcile (<= 5min)
              # VCLConsistent Condition wird False dann wieder True
              sleep 10   # Kurz warten (periodischer Requeue kann früher kommen)
              # Im realen Test: poll bis VCLSynced == True wieder
              for i in $(seq 1 30); do
                STATUS=$(kubectl get vinylcache -n e2e-test my-cache \
                  -o jsonpath='{.status.conditions[?(@.type=="VCLSynced")].status}')
                [ "$STATUS" = "True" ] && echo "Drift corrected" && exit 0
                sleep 10
              done
              echo "Drift not corrected within 5 minutes"
              exit 1
```

**Bezug:** §7.8 (Drift-Erkennung via periodischem Requeue nach 5min)

---

## kind-Cluster Konfiguration

```yaml
# e2e/setup/kind-config.yaml
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
  - role: worker
  - role: worker
  - role: worker    # 3 Worker-Nodes für HA-Tests und Shard-Director-Tests
networking:
  podSubnet: "10.244.0.0/16"
  serviceSubnet: "10.96.0.0/12"
```

---

## CI-Integration

```yaml
# .github/workflows/e2e.yml
name: E2E Tests

on:
  push:
    branches: [main]
  pull_request:
    branches: [main]

jobs:
  e2e:
    runs-on: ubuntu-latest
    timeout-minutes: 30

    steps:
      - uses: actions/checkout@v4

      - uses: helm/kind-action@v1
        with:
          config: e2e/setup/kind-config.yaml
          cluster_name: cloud-vinyl-e2e

      - name: Build Images
        run: |
          make docker-build
          kind load docker-image ghcr.io/bluedynamics/cloud-vinyl-operator:dev \
            --name cloud-vinyl-e2e

      - name: Install Prerequisites
        run: bash e2e/setup/install-cert-manager.sh

      - name: Install Operator
        run: bash e2e/setup/install-operator.sh

      - name: Install Chainsaw
        run: go install github.com/kyverno/chainsaw@latest

      - name: Run E2E Tests
        run: |
          chainsaw test \
            --test-dir e2e/tests \
            --parallel 2 \
            --report-format junit \
            --report-name e2e-results

      - name: Upload Test Results
        if: always()
        uses: actions/upload-artifact@v4
        with:
          name: e2e-results
          path: e2e-results.xml
```

---

## Backend-Fixture für Tests

```yaml
# e2e/fixtures/backends/echo-service.yaml
# Einfacher Echo-Server: antwortet mit Request-Headern — gut für Cache-Header-Tests
apiVersion: apps/v1
kind: Deployment
metadata:
  name: echo-backend
spec:
  replicas: 1
  selector:
    matchLabels:
      app: echo-backend
  template:
    metadata:
      labels:
        app: echo-backend
    spec:
      containers:
        - name: echo
          image: ealen/echo-server:latest
          ports:
            - containerPort: 80
          env:
            - name: PORT
              value: "80"
---
apiVersion: v1
kind: Service
metadata:
  name: echo-backend
spec:
  selector:
    app: echo-backend
  ports:
    - port: 80
      targetPort: 80
```

Das Backend setzt `X-Xkey`-Header und korrekte Cache-Control-Header damit Caching und xkey-Tests funktionieren.

---

## Dokumentations-Deliverables

| Datei | Diataxis | Inhalt | Wann |
|-------|----------|--------|------|
| `docs/sources/tutorials/quickstart.md` | Tutorial | Erstes VinylCache-Objekt: kind + Helm + echo-Backend + PURGE (identischer Aufbau wie E2E Szenario 1) | Nach M8 |
| `docs/sources/tutorials/cluster-setup.md` | Tutorial | Multi-Replica mit Traefik + PROXY Protocol (Aufbau wie Szenario 2/7) | Nach M8 |
| `docs/sources/how-to/debug-vcl.md` | How-To | VCL-Probleme diagnostizieren: Status, Events, kubectl exec in Agent | Nach M8 |

---

## Akzeptanzkriterien

- [ ] Alle 8 Szenarien in CI grün (kind-Cluster, GitHub Actions)
- [ ] Szenario 1 (basic-lifecycle) läuft in < 2 Minuten
- [ ] Szenario 6 (HA-Operator) läuft in < 5 Minuten
- [ ] Alle Szenarien sind idempotent (mehrfaches Ausführen ohne Cluster-Reset möglich)
- [ ] CI-Job schlägt bei Fehler fehl und publiziert JUnit-Report als Artifact
- [ ] Quickstart-Tutorial in Docs: Nutzer kann in < 10 Minuten einem funktionierenden VinylCache haben

---

## Offene Fragen

1. **Parallelisierung:** Chainsaw unterstützt `--parallel N` — welche Szenarien können parallel laufen? Szenarien 1-5, 7, 8 können in eigenen Namespaces parallel laufen. Szenario 6 (HA) braucht Exklusivzugriff auf den Operator-Namespace.
2. **Flakiness:** Timing-abhängige Tests (Szenario 8: Drift-Erkennung) können flaky sein wenn der periodische Requeue (5min) in CI zu lang dauert. Option: `--requeue-after` als Operator-Flag auf 30s für E2E reduzieren.
3. **Backend-VCL-Kompatibilität:** Die Echo-Backend-Fixtures müssen `Cache-Control: public, max-age=60` und `X-Xkey: ...` Header setzen — oder die VinylCache-Spec muss `vcl.snippets.vcl_backend_response` nutzen um TTL zu setzen.
