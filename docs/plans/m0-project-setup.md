# M0: Project Setup & Tooling

**Status:** Bereit zur Implementierung — startet als erstes
**Voraussetzungen:** keine
**Parallelisierbar mit:** nichts — M0 ist Blocking-Dependency für alle anderen Module
**Geschätzte Team-Größe:** 1 Person

---

## Kontext

M0 erzeugt das lauffähige Repo-Skeleton. Alle anderen Module bauen darauf auf. Ohne M0 kann kein anderes Modul starten.

**Relevante Architektur-Abschnitte:**
- §3.2 — Operator-Komponenten (Überblick: was wird gebaut)
- §8.3 — Container-Images (Varnish-Image, Operator-Image, Agent-Image — beeinflusst Dockerfile-Struktur)
- §9.1 — REST als Protokoll (beeinflusst welche HTTP-Libs benötigt werden)
- §9.2 — text/template als Template-Engine (beeinflusst Go-Dependencies)
- §11.1 — Versionsplan (API-Gruppe `vinyl.bluedynamics.eu/v1alpha1` muss im kubebuilder-Init stehen)

---

## Ziel

Nach M0 existiert:
- Ein kompilierbares Go-Modul mit kubebuilder-Scaffolding
- CI-Pipeline die bei jedem Push lint + test + docs prüft
- Sphinx-Docs-Skeleton das baut
- Makefile mit allen benötigten Targets
- Kein Produktionscode — nur Infrastruktur

---

## Repository-Struktur

```
cloud-vinyl/
├── .github/
│   └── workflows/
│       ├── ci.yml          # lint, test, coverage, docs-build
│       └── release.yml     # goreleaser, container images (später)
├── cmd/
│   ├── operator/
│   │   └── main.go         # kubebuilder-generiert, vorerst leer
│   └── agent/
│       └── main.go         # manuell angelegt, vorerst leer
├── internal/               # alle Packages kommen hier rein (M1–M6)
├── api/
│   └── v1alpha1/           # kubebuilder-generiert, leer bis M1
├── config/                 # kubebuilder-generierte Kustomize-Manifeste
│   ├── crd/
│   ├── rbac/
│   ├── manager/
│   └── default/
├── docs/
│   ├── Makefile            # mxmake-generiert (wie plone-pgcatalog)
│   ├── mx.ini
│   └── sources/
│       ├── conf.py         # Sphinx-Konfiguration (Shibuya, MyST, mermaid, ...)
│       ├── index.md        # Landing Page mit Diataxis-Grid
│       ├── _static/
│       ├── _templates/
│       ├── tutorials/
│       │   └── index.md
│       ├── how-to/
│       │   └── index.md
│       ├── reference/
│       │   └── index.md
│       └── explanation/
│           └── index.md
├── hack/
│   ├── generate.sh         # controller-gen aufrufen
│   └── verify-generate.sh  # prüfen ob generated code aktuell ist
├── go.mod
├── go.sum
├── Makefile
└── pyproject.toml          # Docs-Tooling: Sphinx, mxmake
```

---

## Schlüssel-Schritte

### 1. kubebuilder-Scaffolding

```bash
# Im leeren Repo-Root:
kubebuilder init \
  --domain bluedynamics.eu \
  --repo github.com/bluedynamics/cloud-vinyl

kubebuilder create api \
  --group vinyl \
  --version v1alpha1 \
  --kind VinylCache \
  --resource \
  --controller

kubebuilder create webhook \
  --group vinyl \
  --version v1alpha1 \
  --kind VinylCache \
  --defaulting \
  --programmatic-validation
```

Das erzeugt `api/v1alpha1/`, `internal/controller/`, `cmd/operator/main.go`, `config/`.

### 2. Agent-Binary-Skeleton

`cmd/agent/main.go` manuell anlegen — kubebuilder kennt keinen zweiten Binary-Einstiegspunkt:

```go
package main

func main() {
    // M3 füllt das aus
}
```

### 3. Sphinx-Docs-Setup

Identisch zu `plone-pgcatalog` (Referenz: https://github.com/bluedynamics/plone-pgcatalog/tree/main/docs):
- `docs/Makefile` via mxmake
- `docs/mx.ini` mit `threads = 5`
- `docs/sources/conf.py`: Shibuya-Theme, MyST, mermaid, sphinx_design, sphinx_copybutton
- `docs/sources/index.md`: Diataxis-Grid (vier Karten: Tutorials, How-To, Reference, Explanation)

### 4. pyproject.toml

Docs-Dependencies und Linting:

```toml
[project]
name = "cloud-vinyl-docs"

[tool.mxmake]
# Sphinx-Build-Konfiguration

[tool.ruff]
line-length = 120  # nur für Python in docs/conf.py relevant
```

### 5. golangci-lint Konfiguration

`.golangci.yml` anlegen. Aktivierte Linter:
- `staticcheck`, `errcheck`, `revive`, `gosec`, `govet`, `gofmt`
- `wrapcheck` für Fehler aus externen Packages
- Ausnahmen für generierte Dateien (`zz_generated*.go`, `*_test.go` für gosec)

### 6. CI-Pipeline

```yaml
# .github/workflows/ci.yml
name: CI
on: [push, pull_request]

jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: golangci/golangci-lint-action@v6

  test:
    runs-on: ubuntu-latest
    steps:
      - run: go test ./... -race -coverprofile=coverage.out
      - uses: codecov/codecov-action@v4
        with:
          fail_ci_if_error: true
          threshold: 80%  # Muss-Grenze

  docs:
    runs-on: ubuntu-latest
    steps:
      - run: make -C docs install && make -C docs docs
        # sphinx-build -W: Warnungen = Fehler

  build:
    runs-on: ubuntu-latest
    steps:
      - run: go build ./cmd/operator ./cmd/agent
```

### 7. Makefile

```makefile
.PHONY: generate lint test test-int test-e2e coverage docs build

generate:
	controller-gen rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases
	controller-gen object:headerFile="hack/boilerplate.go.txt" paths="./..."

lint:
	golangci-lint run ./...

test:
	go test ./... -race -coverprofile=coverage.out

test-int:
	go test ./... -race -tags=integration -coverprofile=coverage-int.out

coverage:
	go tool cover -html=coverage.out

docs:
	$(MAKE) -C docs docs

docs-live:
	$(MAKE) -C docs docs-live

build:
	go build -o bin/operator ./cmd/operator
	go build -o bin/agent ./cmd/agent
```

---

## Dokumentations-Deliverables

| Datei | Inhalt |
|-------|--------|
| `docs/sources/index.md` | Landing Page mit Diataxis-Grid, Projekt-Kurzbeschreibung |
| `docs/sources/explanation/architecture.md` | Stub: Verweis auf `docs/plans/architektur.md`, wird in M4 ausgebaut |
| `docs/sources/reference/operator-flags.md` | Stub: wird in M4 ausgebaut |

---

## Akzeptanzkriterien

- [ ] `git clone` + `make build` kompiliert ohne Fehler
- [ ] `make lint` grün (auch auf generiertem Code)
- [ ] `make test` grün (keine Tests = 0 Failures, nicht Error)
- [ ] CI-Pipeline läuft durch bei jedem Push auf `main`
- [ ] `make docs` baut Sphinx ohne Warnungen
- [ ] `make generate` erzeugt CRD-YAML und DeepCopy-Funktionen
- [ ] Kein Produktionscode eingecheckt (nur Skeleton)
- [ ] `README.md` mit `make build`, `make test`, `make docs` Anweisungen
