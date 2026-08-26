# E2E Rewrite Phases 1–2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Put the E2E suite on a policy-enforcing cluster, split it into a fast and a full variant, and give the product's central promise — objects are cached and a PURGE clears them everywhere — its first real test.

**Architecture:** Two layers with a mechanically checkable boundary. chainsaw owns Kubernetes state and never touches HTTP; a new `cmd/vinylprobe` binary speaks only HTTP and never imports `k8s.io/*`. Cache hits are detected by echoing a per-request header back through the cache, which needs no product change.

**Tech Stack:** Go, chainsaw, kind, Calico, GitHub Actions.

**Spec:** `docs/superpowers/specs/2026-08-26-e2e-rewrite-design.md`

## Global Constraints

- Go toolchain: `1.26.7` (from `go.mod`; CI derives it via `go-version-file`)
- kind CLI `v0.32.0` and node image `kindest/node:v1.36.1` — these two must come from the same kind release, see the comment already in `e2e-chainsaw.yml`
- Calico `v3.31.1`, with `CALICO_IPV4POOL_CIDR=10.244.0.0/16` to match `podSubnet` in `e2e/setup/kind-config.yaml`
- chainsaw release `v0.2.12`, installed by `kyverno/action-install-chainsaw@v0.2.15`
- golangci-lint `v2.13.1` — must stay identical in `.github/workflows/ci.yml` and `GOLANGCI_LINT_VERSION` in the `Makefile`
- varnish image in fixtures: `varnish:8.0.2`
- Every new chainsaw test carries exactly one `metadata.labels.suite`, value `fast` or `full`
- `cmd/vinylprobe` and everything it imports must not depend on `k8s.io/*`

---

### Task 1: Run the E2E cluster on Calico

**Files:**
- Modify: `e2e/setup/kind-config.yaml`
- Create: `e2e/setup/install-calico.sh`
- Modify: `.github/workflows/e2e-chainsaw.yml`

**Interfaces:**
- Consumes: nothing
- Produces: an E2E cluster that enforces NetworkPolicies. No Go symbols.

- [ ] **Step 1: Disable the default CNI**

Add `disableDefaultCNI: true` under `networking:` in `e2e/setup/kind-config.yaml`. The file becomes:

```yaml
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
  - role: worker
  - role: worker
  - role: worker
networking:
  # kindnet ignores NetworkPolicies, so the four policies the operator ships
  # were never enforced in CI. Calico enforces them; see
  # docs/superpowers/specs/2026-08-26-e2e-rewrite-design.md.
  disableDefaultCNI: true
  podSubnet: "10.244.0.0/16"
  serviceSubnet: "10.96.0.0/12"
```

- [ ] **Step 2: Add the Calico install script**

Create `e2e/setup/install-calico.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

CALICO_VERSION="${CALICO_VERSION:-v3.31.1}"
POD_CIDR="${POD_CIDR:-10.244.0.0/16}"

kubectl apply -f "https://raw.githubusercontent.com/projectcalico/calico/${CALICO_VERSION}/manifests/calico.yaml"

# Calico defaults to 192.168.0.0/16; kind-config.yaml uses 10.244.0.0/16.
kubectl -n kube-system set env daemonset/calico-node "CALICO_IPV4POOL_CIDR=${POD_CIDR}"

# Wait on the DaemonSet, NOT on node readiness. Nodes report Ready as soon as the
# CNI binary is on disk while calico-node is still starting, which would let the
# suite run against a half-ready cluster.
kubectl -n kube-system rollout status daemonset/calico-node --timeout=420s
```

Then `chmod +x e2e/setup/install-calico.sh`.

- [ ] **Step 3: Wire it into the workflow**

In `.github/workflows/e2e-chainsaw.yml`, immediately after the `Create kind cluster` step, insert:

```yaml
      - name: Install Calico
        run: bash e2e/setup/install-calico.sh
```

- [ ] **Step 4: Pre-pull the Calico images**

The cold pull of `calico/cni` cost 33s when measured. Extend the existing pre-pull step (currently `Pre-pull varnish image into Kind`) so it reads:

```yaml
      - name: Pre-pull images into Kind
        run: |
          docker pull varnish:8.0.2
          kind load docker-image varnish:8.0.2 --name cloud-vinyl-e2e
          for img in \
            quay.io/calico/cni:v3.31.1 \
            quay.io/calico/node:v3.31.1 \
            quay.io/calico/kube-controllers:v3.31.1; do
            docker pull "$img"
            kind load docker-image "$img" --name cloud-vinyl-e2e
          done
```

Move this step to sit *before* `Install Calico`.

- [ ] **Step 5: Verify enforcement actually happens**

This is the check that makes the task worth anything. Run locally:

```bash
kind create cluster --name verify-calico --image kindest/node:v1.36.1 --config e2e/setup/kind-config.yaml
kubectl config use-context kind-verify-calico
bash e2e/setup/install-calico.sh
kubectl create ns np-check
kubectl -n np-check run target --image=nginx:alpine --port=80 -l app=target
kubectl -n np-check expose pod target --port=80
kubectl -n np-check run client --image=busybox:1.36 --command -- sleep 3600
kubectl -n np-check wait --for=condition=Ready pod/target pod/client --timeout=180s
kubectl -n np-check exec client -- wget -q -T5 -O- http://target >/dev/null && echo "reachable (expected)"
kubectl apply -f - <<'EOF'
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata: {name: deny-all, namespace: np-check}
spec:
  podSelector: {matchLabels: {app: target}}
  policyTypes: [Ingress]
EOF
sleep 5
kubectl -n np-check exec client -- wget -q -T5 -O- http://target >/dev/null && echo "FAIL: policy ignored" || echo "blocked (expected)"
kind delete cluster --name verify-calico
```

Expected: `reachable (expected)` then `blocked (expected)`.

- [ ] **Step 6: Commit**

```bash
git add e2e/setup/kind-config.yaml e2e/setup/install-calico.sh .github/workflows/e2e-chainsaw.yml
git commit -m "test(e2e): run the cluster on Calico so NetworkPolicies are enforced

kindnet ignores NetworkPolicies, so the four policies the operator ships were
never exercised: one that blocked all traffic would have passed CI. Measured
cost is ~17s, most of it a cold image pull now removed by pre-pulling.

Waits on the calico-node DaemonSet rather than node readiness. Nodes report
Ready as soon as the CNI binary is on disk, which would let the suite run
against a half-ready cluster."
```

---

### Task 2: Label the suite and select on labels

**Files:**
- Modify: all ten `e2e/tests/*/chainsaw-test.yaml`
- Create: `hack/check-suite-labels.sh`
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: nothing
- Produces: every chainsaw test carries `metadata.labels.suite`; `hack/check-suite-labels.sh` exits non-zero if any does not.

- [ ] **Step 1: Write the failing check**

Create `hack/check-suite-labels.sh`:

```bash
#!/usr/bin/env bash
# Every chainsaw test must carry exactly one suite label. Without this, a typo
# means a test runs in neither suite and disappears silently — the same class of
# failure as the --include-test-regex prefix trap documented in
# .github/workflows/e2e-chainsaw.yml.
set -euo pipefail

fail=0
for f in e2e/tests/*/chainsaw-test.yaml; do
  value="$(grep -E '^\s+suite:\s*(fast|full)\s*$' "$f" | wc -l)"
  if [ "$value" -ne 1 ]; then
    echo "FAIL: $f has $value suite labels, expected exactly 1"
    fail=1
  fi
done
[ "$fail" -eq 0 ] && echo "OK: all chainsaw tests carry exactly one suite label"
exit "$fail"
```

Then `chmod +x hack/check-suite-labels.sh`.

- [ ] **Step 2: Run it to verify it fails**

Run: `bash hack/check-suite-labels.sh`
Expected: ten `FAIL:` lines, exit 1. No test carries a label yet.

- [ ] **Step 3: Label the existing tests**

In each `e2e/tests/<name>/chainsaw-test.yaml`, add a `labels` block under `metadata`. For example `e2e/tests/basic-lifecycle/chainsaw-test.yaml` becomes:

```yaml
apiVersion: chainsaw.kyverno.io/v1alpha1
kind: Test
metadata:
  name: basic-lifecycle
  labels:
    suite: fast
```

Use `suite: fast` for `basic-lifecycle`, `purge-broadcast` and `vcl-validation`. Use `suite: full` for the other seven: `cluster-routing`, `drift-detection`, `ha-operator`, `per-backend-directors`, `scaling`, `volumes-and-pvc`, `xkey-invalidation`.

These assignments are provisional — the tests themselves are replaced in phases 3 and 4. The point of this task is that the mechanism works before its content changes.

- [ ] **Step 4: Run the check to verify it passes**

Run: `bash hack/check-suite-labels.sh`
Expected: `OK: all chainsaw tests carry exactly one suite label`, exit 0.

- [ ] **Step 5: Add the check to CI**

In `.github/workflows/ci.yml`, add a step to the `lint` job after the golangci-lint step:

```yaml
      - name: Check chainsaw suite labels
        run: bash hack/check-suite-labels.sh
```

- [ ] **Step 6: Replace regex selection with label selection**

In `.github/workflows/e2e-chainsaw.yml`, replace the two test-running steps and the long `NOTE on the "chainsaw/" prefix` comment above them with:

```yaml
      # Selection is by label rather than by --include-test-regex. The regex form
      # maps onto Go's -test.run and silently matches nothing when the
      # "chainsaw/" prefix is missing, which once let a step run zero tests and
      # still exit 0 (#63). hack/check-suite-labels.sh guards the replacement.
      - name: Run E2E tests (parallel)
        run: |
          chainsaw test \
            --test-dir e2e/tests \
            --parallel 2 \
            --selector "suite in (fast,full)" \
            --exclude-test-regex "chainsaw/(ha-operator|drift-detection)"

      - name: Run sequential E2E tests
        run: |
          chainsaw test \
            --test-dir e2e/tests \
            --parallel 1 \
            --include-test-regex "chainsaw/(ha-operator|drift-detection)"
```

The sequential step keeps its regex: it selects two named tests that must not run concurrently, which is a different concern from suite membership. Task 3 introduces the fast/full switch.

- [ ] **Step 7: Commit**

```bash
git add e2e/tests hack/check-suite-labels.sh .github/workflows/ci.yml .github/workflows/e2e-chainsaw.yml
git commit -m "test(e2e): select chainsaw tests by label instead of regex

Every test now carries exactly one suite label, checked in CI. Label selection
replaces --include-test-regex for suite membership, which chainsaw maps onto
Go's -test.run: a pattern missing the chainsaw/ prefix matches nothing silently,
and once let a step run zero tests while exiting 0 (#63).

Labels can still be mistyped, so hack/check-suite-labels.sh makes that a build
failure rather than a disappearing test."
```

---

### Task 3: Split fast and full, and make the full suite triggerable per PR

**Files:**
- Modify: `.github/workflows/e2e-chainsaw.yml`

**Interfaces:**
- Consumes: the `suite` labels from Task 2
- Produces: a workflow whose check name states which suite ran

- [ ] **Step 1: Rewrite the trigger block**

Replace the `on:` block of `.github/workflows/e2e-chainsaw.yml` with:

```yaml
on:
  push:
    branches: [main]
    paths:
      - "**.go"
      - "go.mod"
      - "go.sum"
      - "e2e/**"
      - "charts/**"
      - "config/**"
      - "Dockerfile.*"
      - ".github/workflows/e2e-chainsaw.yml"
  pull_request:
    branches: [main]
    types: [opened, synchronize, reopened, labeled]
    paths:
      - "**.go"
      - "go.mod"
      - "go.sum"
      - "e2e/**"
      - "charts/**"
      - "config/**"
      - "Dockerfile.*"
      - ".github/workflows/e2e-chainsaw.yml"
  # Manual trigger so main can be re-run on demand. When a PR goes red there is
  # otherwise no cheap way to check whether unmodified main is red too (#63).
  workflow_dispatch:
```

- [ ] **Step 2: Guard the labelled trigger and name the job by variant**

Replace the `jobs:` header and the job's first lines with:

```yaml
jobs:
  e2e:
    # A labelled event fires for every label. Without this guard, adding
    # "documentation" to a PR would start the full suite.
    if: >-
      github.event_name != 'pull_request' ||
      github.event.action != 'labeled' ||
      github.event.label.name == 'e2e-full'
    name: >-
      Chainsaw E2E
      (${{ (github.event_name == 'push' || github.event_name == 'workflow_dispatch' || contains(github.event.pull_request.labels.*.name, 'e2e-full')) && 'full' || 'fast' }})
    runs-on: ubuntu-latest
    timeout-minutes: 45
    env:
      SUITE: >-
        ${{ (github.event_name == 'push' || github.event_name == 'workflow_dispatch' || contains(github.event.pull_request.labels.*.name, 'e2e-full')) && 'full' || 'fast' }}
```

The job name carries the variant so a green check is unambiguous about which suite passed.

- [ ] **Step 3: Use the variant when selecting tests**

Change the parallel test step from Task 2 to:

```yaml
      - name: Run E2E tests (parallel)
        run: |
          if [ "$SUITE" = "full" ]; then
            SELECTOR="suite in (fast,full)"
          else
            SELECTOR="suite=fast"
          fi
          echo "Running suite: $SUITE (selector: $SELECTOR)"
          chainsaw test \
            --test-dir e2e/tests \
            --parallel 2 \
            --selector "$SELECTOR" \
            --exclude-test-regex "chainsaw/(ha-operator|drift-detection)"
```

And guard the sequential step, whose two tests are both `full`:

```yaml
      - name: Run sequential E2E tests
        if: env.SUITE == 'full'
        run: |
          chainsaw test \
            --test-dir e2e/tests \
            --parallel 1 \
            --include-test-regex "chainsaw/(ha-operator|drift-detection)"
```

- [ ] **Step 4: Create the label**

```bash
gh label create e2e-full --description "Run the full E2E suite on this PR" --color 1d76db
```

- [ ] **Step 5: Verify the expressions parse**

Run: `python3 -c "import yaml;yaml.safe_load(open('.github/workflows/e2e-chainsaw.yml'));print('YAML ok')"`
Expected: `YAML ok`

Push the branch and confirm the PR check is named `Chainsaw E2E (fast)`. Then add the label and confirm a new run appears named `Chainsaw E2E (full)`.

- [ ] **Step 6: Commit**

```bash
git add .github/workflows/e2e-chainsaw.yml
git commit -m "ci(e2e): split fast and full suites, add per-PR full run

Pull requests run the fast core; main runs everything. Adding the e2e-full
label to a PR runs the full suite there too, intended for use just before merge.

The labelled trigger is guarded on the label name, otherwise adding any label
would start the suite. The job name carries the variant so a green check says
which suite actually passed.

Also adds the path filter the workflow never had: documentation-only merges no
longer start a multi-node cluster."
```

---

### Task 4: The cache-hit detector

**Files:**
- Create: `internal/probe/cache.go`
- Test: `internal/probe/cache_test.go`

**Interfaces:**
- Consumes: nothing
- Produces:
  - `type Outcome int` with constants `Miss Outcome = 0`, `Hit Outcome = 1`
  - `func (o Outcome) String() string` returning `"miss"` or `"hit"`
  - `func Detect(ctx context.Context, c *http.Client, url string) (Outcome, error)`

- [ ] **Step 1: Write the failing test**

Create `internal/probe/cache_test.go`:

```go
package probe

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// cachingServer answers every request with the first X-Probe value it ever saw,
// which is what a cache in front of an echoing backend looks like.
func cachingServer() *httptest.Server {
	var first string
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if first == "" {
			first = r.Header.Get("X-Probe")
		}
		fmt.Fprintf(w, `{"headers":{"x-probe":%q}}`, first)
	}))
}

// echoingServer answers with the current X-Probe value, which is what an
// uncached path looks like.
func echoingServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"headers":{"x-probe":%q}}`, r.Header.Get("X-Probe"))
	}))
}

func TestDetectReportsHitWhenSecondResponseCarriesTheFirstToken(t *testing.T) {
	srv := cachingServer()
	defer srv.Close()

	got, err := Detect(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("Detect returned error: %v", err)
	}
	if got != Hit {
		t.Fatalf("got %v, want hit", got)
	}
}

func TestDetectReportsMissWhenEachResponseCarriesItsOwnToken(t *testing.T) {
	srv := echoingServer()
	defer srv.Close()

	got, err := Detect(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("Detect returned error: %v", err)
	}
	if got != Miss {
		t.Fatalf("got %v, want miss", got)
	}
}

func TestDetectErrorsWhenTheBackendDoesNotEchoTheHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "no probe header here")
	}))
	defer srv.Close()

	_, err := Detect(context.Background(), srv.Client(), srv.URL)
	if err == nil {
		t.Fatal("expected an error when the backend echoes nothing, got nil")
	}
	if !strings.Contains(err.Error(), "did not echo") {
		t.Fatalf("error should explain the cause, got: %v", err)
	}
}

func TestDetectUsesDistinctTokensPerCall(t *testing.T) {
	seen := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.Header.Get("X-Probe")] = true
		fmt.Fprintf(w, `{"headers":{"x-probe":%q}}`, r.Header.Get("X-Probe"))
	}))
	defer srv.Close()

	if _, err := Detect(context.Background(), srv.Client(), srv.URL); err != nil {
		t.Fatalf("first Detect: %v", err)
	}
	if _, err := Detect(context.Background(), srv.Client(), srv.URL); err != nil {
		t.Fatalf("second Detect: %v", err)
	}
	// Two calls, two requests each, all tokens distinct: otherwise a second
	// Detect against a warm cache could not tell hit from miss.
	if len(seen) != 4 {
		t.Fatalf("expected 4 distinct probe tokens across two calls, got %d", len(seen))
	}
}

func TestDetectRespectsContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if _, err := Detect(ctx, srv.Client(), srv.URL); err == nil {
		t.Fatal("expected an error when the context expires, got nil")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOTOOLCHAIN=go1.26.7 go test ./internal/probe/ -v`
Expected: FAIL — `undefined: Detect`, `undefined: Hit`, `undefined: Miss`.

- [ ] **Step 3: Write the implementation**

Create `internal/probe/cache.go`:

```go
// Package probe detects cache behaviour over plain HTTP.
//
// It deliberately depends on nothing from k8s.io: the E2E boundary is that
// chainsaw owns Kubernetes state and this package owns HTTP. See
// docs/sources/explanation/testing-strategy.md.
package probe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Outcome is whether a response came from cache.
type Outcome int

const (
	// Miss means the response was produced for this request.
	Miss Outcome = iota
	// Hit means the response was served from cache.
	Hit
)

func (o Outcome) String() string {
	if o == Hit {
		return "hit"
	}
	return "miss"
}

// probeHeader is echoed back by the backend, which is how a cached response
// gives itself away: it still carries the token of the request that filled it.
const probeHeader = "X-Probe"

// Detect issues two GETs to url carrying distinct probe tokens and reports
// whether the second was served from cache.
//
// The generated VCL sets no debug headers, and adding one would be a product
// change made for tests, so this infers the answer from the backend echo
// instead.
func Detect(ctx context.Context, c *http.Client, url string) (Outcome, error) {
	first, err := token()
	if err != nil {
		return Miss, err
	}
	second, err := token()
	if err != nil {
		return Miss, err
	}

	if _, err := fetch(ctx, c, url, first); err != nil {
		return Miss, fmt.Errorf("first request: %w", err)
	}
	body, err := fetch(ctx, c, url, second)
	if err != nil {
		return Miss, fmt.Errorf("second request: %w", err)
	}

	switch {
	case strings.Contains(body, first):
		return Hit, nil
	case strings.Contains(body, second):
		return Miss, nil
	default:
		return Miss, fmt.Errorf("backend did not echo the %s header; cannot tell hit from miss", probeHeader)
	}
}

func fetch(ctx context.Context, c *http.Client, url, tok string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set(probeHeader, tok)

	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func token() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating probe token: %w", err)
	}
	return "probe-" + hex.EncodeToString(b[:]), nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `GOTOOLCHAIN=go1.26.7 go test ./internal/probe/ -v`
Expected: all five tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/probe
git commit -m "test(probe): add a cache-hit detector that needs no product change

The generated VCL sets no debug headers, so a test cannot tell HIT from MISS
from a response header, and adding one would be a product change made for tests.

Instead this sends two requests carrying distinct X-Probe tokens and reads the
backend's echo back out of the body: a cached response still carries the token
of the request that filled it. Validated against a real varnish before being
adopted, see the design doc."
```

---

### Task 5: The `vinylprobe` command and its image

**Files:**
- Create: `cmd/vinylprobe/main.go`
- Create: `Dockerfile.probe`
- Create: `hack/check-e2e-boundary.sh`
- Modify: `.github/workflows/ci.yml`
- Modify: `.github/workflows/e2e-chainsaw.yml`

**Interfaces:**
- Consumes: `probe.Detect`, `probe.Hit`, `probe.Miss` from Task 4
- Produces: an image `cloud-vinyl-probe:dev` in the kind cluster whose entrypoint accepts `-url`, `-expect`, `-timeout` and exits 0 on match, 1 on mismatch, 2 on error

- [ ] **Step 1: Write the command**

Create `cmd/vinylprobe/main.go`:

```go
// Command vinylprobe checks cache behaviour over HTTP from inside the cluster.
//
// It must not import k8s.io/*: chainsaw owns Kubernetes state, this owns HTTP.
// hack/check-e2e-boundary.sh enforces that.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/bluedynamics/cloud-vinyl/internal/probe"
)

func main() {
	url := flag.String("url", "", "URL to probe (required)")
	expect := flag.String("expect", "hit", `expected outcome: "hit" or "miss"`)
	timeout := flag.Duration("timeout", 30*time.Second, "overall deadline")
	flag.Parse()

	if *url == "" {
		fmt.Fprintln(os.Stderr, "error: -url is required")
		os.Exit(2)
	}
	var want probe.Outcome
	switch *expect {
	case "hit":
		want = probe.Hit
	case "miss":
		want = probe.Miss
	default:
		fmt.Fprintf(os.Stderr, "error: -expect must be \"hit\" or \"miss\", got %q\n", *expect)
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	got, err := probe.Detect(ctx, &http.Client{}, *url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	if got != want {
		fmt.Printf("FAIL: %s expected %s, got %s\n", *url, want, got)
		os.Exit(1)
	}
	fmt.Printf("OK: %s is a %s\n", *url, got)
}
```

- [ ] **Step 2: Verify it builds and the boundary holds**

Run:
```bash
GOTOOLCHAIN=go1.26.7 go build ./cmd/vinylprobe
GOTOOLCHAIN=go1.26.7 go list -deps ./cmd/vinylprobe | grep -c "k8s.io" || echo "0 k8s.io dependencies"
```
Expected: builds cleanly, and `0 k8s.io dependencies`.

- [ ] **Step 3: Write the boundary check**

Create `hack/check-e2e-boundary.sh`:

```bash
#!/usr/bin/env bash
# The E2E suite has two layers: chainsaw owns Kubernetes state, vinylprobe owns
# HTTP. A boundary that lives only in someone's head does not survive three
# sessions, so it is checked here. See
# docs/sources/explanation/testing-strategy.md.
set -euo pipefail

fail=0

if go list -deps ./cmd/vinylprobe 2>/dev/null | grep -q "^k8s.io/"; then
  echo "FAIL: cmd/vinylprobe pulls in k8s.io packages; it must speak only HTTP"
  go list -deps ./cmd/vinylprobe | grep "^k8s.io/" | sed 's/^/       /'
  fail=1
fi

if grep -rn "curl\|wget" e2e/tests/ >/dev/null 2>&1; then
  echo "FAIL: chainsaw tests invoke an HTTP client directly; use vinylprobe instead"
  grep -rn "curl\|wget" e2e/tests/ | sed 's/^/       /'
  fail=1
fi

[ "$fail" -eq 0 ] && echo "OK: E2E layer boundary intact"
exit "$fail"
```

Then `chmod +x hack/check-e2e-boundary.sh`.

- [ ] **Step 4: Run the check to verify it passes**

Run: `bash hack/check-e2e-boundary.sh`
Expected: `OK: E2E layer boundary intact`

- [ ] **Step 5: Add the image**

Create `Dockerfile.probe`, following the pattern of `Dockerfile.agent`:

```dockerfile
FROM golang:1.26 AS builder
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o vinylprobe ./cmd/vinylprobe

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /workspace/vinylprobe .
USER 65532:65532
ENTRYPOINT ["/vinylprobe"]
```

- [ ] **Step 6: Wire the image and the check into CI**

In `.github/workflows/e2e-chainsaw.yml`, after the `Build agent image` step:

```yaml
      - name: Build probe image
        run: |
          docker build -f Dockerfile.probe \
            -t cloud-vinyl-probe:dev .
          kind load docker-image cloud-vinyl-probe:dev \
            --name cloud-vinyl-e2e
```

In `.github/workflows/ci.yml`, next to the suite-label check from Task 2:

```yaml
      - name: Check E2E layer boundary
        run: bash hack/check-e2e-boundary.sh
```

- [ ] **Step 7: Commit**

```bash
git add cmd/vinylprobe Dockerfile.probe hack/check-e2e-boundary.sh .github/workflows/ci.yml .github/workflows/e2e-chainsaw.yml
git commit -m "test(e2e): add the vinylprobe command and enforce the layer boundary

vinylprobe speaks HTTP and nothing else; chainsaw keeps Kubernetes state. The
boundary is checked in CI rather than merely documented: vinylprobe must not
pull in k8s.io packages, and no chainsaw test may invoke curl or wget.

It runs as its own image rather than being folded into the agent, so a test
helper never ships inside a product image."
```

---

### Task 6: The `cache-and-invalidate` test

**Files:**
- Create: `e2e/tests/cache-and-invalidate/chainsaw-test.yaml`
- Create: `e2e/fixtures/probe/probe-pod.yaml`

**Interfaces:**
- Consumes: the `cloud-vinyl-probe:dev` image from Task 5, the `standard.yaml` fixture, `echo-service.yaml`
- Produces: the first E2E test that sends real traffic through the cache

- [ ] **Step 1: Add the probe pod fixture**

Create `e2e/fixtures/probe/probe-pod.yaml`:

```yaml
# Runs inside the test namespace so its traffic is subject to the same
# NetworkPolicies as any other client. A kubectl port-forward would bypass them
# and defeat the point of enforcing policy at all.
apiVersion: v1
kind: Pod
metadata:
  name: probe
  labels:
    app: probe
spec:
  restartPolicy: Never
  containers:
    - name: probe
      image: cloud-vinyl-probe:dev
      imagePullPolicy: Never
      command: ["sleep", "3600"]
```

`imagePullPolicy: Never` is required: the image is side-loaded with `kind load` and does not exist in any registry.

- [ ] **Step 2: Write the test**

Create `e2e/tests/cache-and-invalidate/chainsaw-test.yaml`:

```yaml
apiVersion: chainsaw.kyverno.io/v1alpha1
kind: Test
metadata:
  name: cache-and-invalidate
  labels:
    suite: fast
spec:
  description: |
    Send real traffic through the cache and prove the product's central promise:
    responses are cached, and a single PURGE clears the object on every pod.

    The old purge-broadcast test asserted that a Service object existed, which
    said nothing about whether a purge ever reached anything.
  timeouts:
    assert: 180s
    delete: 60s
    exec: 60s
  steps:
    - name: deploy-backend
      try:
        - apply:
            file: ../../fixtures/backends/echo-service.yaml
        - assert:
            resource:
              apiVersion: apps/v1
              kind: Deployment
              metadata:
                name: echo-backend
              status:
                readyReplicas: 1

    - name: create-cache
      try:
        - apply:
            file: ../../fixtures/vinylcaches/standard.yaml
        - assert:
            resource:
              apiVersion: vinyl.bluedynamics.eu/v1alpha1
              kind: VinylCache
              metadata:
                name: my-cache
              status:
                phase: Ready

    - name: start-probe
      try:
        - apply:
            file: ../../fixtures/probe/probe-pod.yaml
        - assert:
            resource:
              apiVersion: v1
              kind: Pod
              metadata:
                name: probe
              status:
                phase: Running

    - name: first-request-is-a-miss
      description: A cold cache must fetch from the backend.
      try:
        - script:
            content: |
              kubectl exec -n $NAMESPACE probe -- \
                /vinylprobe -url "http://my-cache-traffic/cache-and-invalidate/a" -expect miss

    - name: second-request-is-a-hit
      description: The object is now cached.
      try:
        - script:
            content: |
              kubectl exec -n $NAMESPACE probe -- \
                /vinylprobe -url "http://my-cache-traffic/cache-and-invalidate/a" -expect hit

    - name: purge-clears-every-pod
      description: |
        One PURGE through the invalidation service must clear the object on all
        three pods, not just the one that happens to receive it. Each pod is
        probed by its own stable StatefulSet DNS name.
      try:
        - script:
            content: |
              kubectl exec -n $NAMESPACE probe -- \
                /vinylprobe -url "http://my-cache-invalidation/cache-and-invalidate/a" -expect miss || true
              for i in 0 1 2; do
                kubectl exec -n $NAMESPACE probe -- \
                  /vinylprobe -url "http://my-cache-${i}.my-cache/cache-and-invalidate/a" -expect miss
              done

    - name: cleanup
      try:
        - delete:
            file: ../../fixtures/probe/probe-pod.yaml
        - delete:
            file: ../../fixtures/vinylcaches/standard.yaml
        - delete:
            file: ../../fixtures/backends/echo-service.yaml
```

- [ ] **Step 3: Verify the YAML parses and the boundary check still passes**

Run:
```bash
python3 -c "import yaml;yaml.safe_load(open('e2e/tests/cache-and-invalidate/chainsaw-test.yaml'));print('YAML ok')"
bash hack/check-suite-labels.sh
bash hack/check-e2e-boundary.sh
```
Expected: `YAML ok`, then both checks report OK. The boundary check matters here: the test drives HTTP through `vinylprobe` and never calls `curl`.

- [ ] **Step 4: Run it against a real cluster**

This step will need iteration; the purge step in particular encodes an assumption about how the invalidation service is addressed that must be confirmed against the running system rather than trusted.

```bash
kind create cluster --name cv-local --image kindest/node:v1.36.1 --config e2e/setup/kind-config.yaml
kubectl config use-context kind-cv-local
bash e2e/setup/install-calico.sh
docker build --network=host -f Dockerfile.operator -t ghcr.io/bluedynamics/cloud-vinyl-operator:dev .
docker build --network=host -f Dockerfile.agent -t ghcr.io/bluedynamics/cloud-vinyl-agent:dev .
docker build --network=host -f Dockerfile.probe -t cloud-vinyl-probe:dev .
for i in ghcr.io/bluedynamics/cloud-vinyl-operator:dev ghcr.io/bluedynamics/cloud-vinyl-agent:dev cloud-vinyl-probe:dev; do
  kind load docker-image "$i" --name cv-local
done
docker pull varnish:8.0.2 && kind load docker-image varnish:8.0.2 --name cv-local
bash e2e/setup/install-cert-manager.sh
bash e2e/setup/install-operator.sh
chainsaw test --test-dir e2e/tests --include-test-regex "chainsaw/cache-and-invalidate"
```

Expected: PASS. If the purge step fails, read the operator and agent logs before changing the assertion — a real failure here is exactly what this test exists to find.

Tear down with `kind delete cluster --name cv-local`.

- [ ] **Step 5: Retire the test it replaces**

Delete `e2e/tests/purge-broadcast/`. Its only assertions were that a `VinylCache` reaches `Ready` and that a Service object exists, both of which `cache-and-invalidate` covers on its way to proving something real.

```bash
git rm -r e2e/tests/purge-broadcast
bash hack/check-suite-labels.sh
```

- [ ] **Step 6: Commit**

```bash
git add e2e/tests/cache-and-invalidate e2e/fixtures/probe
git commit -m "test(e2e): prove caching and purge broadcast with real traffic

The first E2E test that sends an HTTP request through the cache. It asserts a
cold miss, then a hit, then that a single PURGE clears the object on all three
pods, each addressed by its own StatefulSet DNS name.

Replaces purge-broadcast, which asserted that a Service object existed and was
named after a behaviour it never exercised.

The probe runs as a pod in the test namespace rather than through port-forward,
so its traffic is subject to the same NetworkPolicies as any real client."
```

---

### Task 7: Write down the boundary

**Files:**
- Create: `docs/sources/explanation/testing-strategy.md`
- Modify: `docs/sources/explanation/index.md`
- Create: `CLAUDE.md`

**Interfaces:**
- Consumes: everything above
- Produces: documentation for humans and for agents

- [ ] **Step 1: Write the explanation page**

Create `docs/sources/explanation/testing-strategy.md`:

````markdown
# Testing strategy

cloud-vinyl tests at three layers. Each answers a question the layer below it
cannot, and putting a test at the wrong layer buys slower feedback for no extra
confidence.

## Unit tests

The bulk of the coverage: 76 test functions across `internal/controller/` alone,
using fake clients. They prove the operator *builds* the right objects — the
right StatefulSet, the right NetworkPolicies, the right VCL.

What they cannot prove is that a real API server *accepts* what was built.

## Integration tests (envtest)

Run against a real `kube-apiserver` and `etcd`, no container runtime. This is
where schema, defaulting, CRD validation and webhook admission belong.

Run them with `make test-int`.

## End-to-end tests (chainsaw)

Run against a real multi-node kind cluster with Calico. They cover **only what a
real cluster can prove**: real varnishd, real networking, real caching, real
policy enforcement.

E2E deliberately does not re-assert control-plane behaviour. A test that only
checks "did the operator create this object" belongs one or two layers down,
where it runs in seconds instead of minutes.

### The layer boundary

The E2E suite has two halves, and the line between them is enforced, not merely
agreed:

- **chainsaw** owns Kubernetes state: fixtures, waiting for `Ready`, namespace
  isolation, cleanup, failure diagnostics. It never speaks HTTP.
- **`cmd/vinylprobe`** owns HTTP. It never imports `k8s.io/*` and knows nothing
  about Kubernetes objects.

`hack/check-e2e-boundary.sh` fails the build if `vinylprobe` acquires a
Kubernetes dependency, or if a chainsaw test reaches for `curl`.

The reason for the rule is that both halves are individually tempting to extend
in the wrong direction. A quick `curl` in a chainsaw step looks harmless; so does
importing a client to look up a pod name. A few of those and the suite has no
structure left.

### How a cache hit is detected

The generated VCL sets no debug headers, and adding one purely for tests would be
a product change. Instead `vinylprobe` sends two requests to the same URL with
different `X-Probe` values and reads the backend's echo back out of the response:
a cached response still carries the token of the request that filled it.

Tests are written as `-expect hit` or `-expect miss`, so if that mechanism is ever
replaced, one file changes.

### Fast and full

Every test carries `metadata.labels.suite`, either `fast` or `full`.

| Trigger | Suite |
|---|---|
| Pull request | `fast` |
| Push to main | `full` |
| PR labelled `e2e-full` | `full` |

Add the `e2e-full` label to a pull request to get the whole suite before merging.
The check name states which suite ran, so a green tick is never ambiguous.

`hack/check-suite-labels.sh` fails the build if a test carries no label or more
than one, because a mislabelled test would otherwise run in neither suite and
disappear without a sound.
````

- [ ] **Step 2: Add it to the toctree**

In `docs/sources/explanation/index.md`, add `testing-strategy` to the `toctree` block, after `architecture`.

- [ ] **Step 3: Verify the docs build**

Run:
```bash
cd docs && uv venv .venv && uv pip install -q --python .venv sphinx myst_parser sphinxcontrib.mermaid shibuya sphinx-design sphinx-copybutton && .venv/bin/sphinx-build -W sources /tmp/docs-check && cd ..
```
Expected: builds with no warnings. `-W` turns warnings into errors, matching CI. Remove `docs/.venv` afterwards.

- [ ] **Step 4: Write the agent instructions**

Create `CLAUDE.md` at the repo root:

```markdown
# Working in this repository

## Testing layers

Three layers, described in full in
[docs/sources/explanation/testing-strategy.md](docs/sources/explanation/testing-strategy.md).
Read it before adding a test.

- **Unit tests** with fake clients: does the operator build the right objects?
- **envtest** (`make test-int`): does a real API server accept them?
- **E2E** (chainsaw + kind + Calico): only what a real cluster can prove — real
  varnishd, real networking, real caching, real policy enforcement.

Do not add control-plane assertions to E2E. If a test only checks that an object
was created, it belongs in a lower layer where it runs in seconds.

## The E2E layer boundary

This one is enforced by `hack/check-e2e-boundary.sh` and will fail the build:

- `cmd/vinylprobe` speaks HTTP only. It must not import `k8s.io/*`, directly or
  transitively.
- chainsaw tests own Kubernetes state only. They must not invoke `curl` or
  `wget`; drive HTTP through `vinylprobe`.

Both halves are tempting to extend in the wrong direction. Resist it.

## Chainsaw suite labels

Every test in `e2e/tests/` needs exactly one `metadata.labels.suite`, `fast` or
`full`. `hack/check-suite-labels.sh` enforces this, because a mislabelled test
runs in neither suite and vanishes silently.

## Version pins that must agree

- kind CLI and `kindest/node` image come from the same kind release. Bump both
  together, or `kind load` fails with an unreadable containerd config error.
- golangci-lint is pinned in both `.github/workflows/ci.yml` and the `Makefile`.
  They must match, or local and CI lint with different rule sets.
- Raising the `go` directive in `go.mod` forces a golangci-lint bump: the linter
  refuses to run when it was built with an older Go than the module targets.

See #87 for the general problem.
```

- [ ] **Step 5: Commit**

```bash
git add docs/sources/explanation/testing-strategy.md docs/sources/explanation/index.md CLAUDE.md
git commit -m "docs: write down the testing layers and the E2E boundary

Three layers, what each is for, and why E2E covers only what a real cluster can
prove. Plus the enforced boundary between chainsaw and vinylprobe, and the
reason it is enforced rather than agreed: both halves are individually tempting
to extend in the wrong direction.

CLAUDE.md carries the same rules for agent sessions, which otherwise rediscover
them or quietly violate them."
```

---

## Self-Review

**Spec coverage.** Phase 1 of the spec maps to Tasks 1–3 (Calico, labels and
selector, triggers with path filter and the `e2e-full` label). Phase 2 maps to
Tasks 4–7 (probe package, command with image and boundary check,
`cache-and-invalidate`, documentation including `CLAUDE.md`). Spec phases 3 and 4
are deliberately out of this plan and get their own once the pattern is proven.

**Deliberate deviation from the spec.** The spec's fast core lists three tests:
`acceptance`, `cache-and-invalidate` and `netpol-enforcement`. This plan
implements only `cache-and-invalidate`. The other two are phase 3 and 4 work:
`acceptance` may only be added once the tests it replaces can be deleted, which
the spec puts last on purpose, and `netpol-enforcement` belongs with the other
data-path tests. Meanwhile the existing tests keep running under their
provisional labels from Task 2, so coverage never dips.

**Placeholders.** None. Every code step carries the actual file content.

**Type consistency.** `probe.Detect`, `probe.Outcome`, `probe.Hit` and
`probe.Miss` are defined in Task 4 and used with those exact names in Task 5. The
image tag `cloud-vinyl-probe:dev` is identical in `Dockerfile.probe` (Task 5),
the workflow build step (Task 5) and `probe-pod.yaml` (Task 6). The scripts
`hack/check-suite-labels.sh` (Task 2) and `hack/check-e2e-boundary.sh` (Task 5)
keep their names where Tasks 6 and 7 refer to them.

**Known soft spot.** Task 6 Step 4 encodes an assumption about how the
invalidation service is addressed for a PURGE. It is marked as needing
confirmation against a running cluster rather than presented as fact, and the
step says to read the logs before weakening the assertion.
