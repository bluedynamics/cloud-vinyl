# Operator Pod IP in VCL PURGE ACL — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Flow the operator's pod IP into the generated VCL so PURGE requests the operator forwards (via the invalidation proxy → invalidation Service → varnish `:8080`) pass Varnish's `vinyl_purge_allowed` ACL check.

**Architecture:** The reconciler already holds `OperatorIP` (populated from the `POD_IP` env var). It is currently used by [reconcileEndpointSlice](internal/controller/endpointslice.go) to target the invalidation Service at the operator pod but never reaches the generator. This plan plumbs it through `generator.Input` and emits it as an additional ACL entry in `acls.vcl.tmpl`, alongside the existing `127.0.0.1` literal and user-configured `spec.invalidation.purge.allowedSources` CIDRs. Empty `OperatorIP` (tests / pre-GA startup) produces no extra ACL entry.

**Tech Stack:** Go (controller-runtime), Go `text/template`, testify.

**Out of scope:** BAN ACL is tracked separately — the VCL currently has no `vinyl_ban_allowed` ACL nor BAN handling in `vcl_recv.vcl.tmpl`. Adding them is a separate feature, not this bug fix. This plan only closes the PURGE gap called out in #18.

---

## File Structure

- [internal/generator/generator.go](internal/generator/generator.go) — add `OperatorIP string` to `Input`.
- [internal/generator/templates/acls.vcl.tmpl](internal/generator/templates/acls.vcl.tmpl) — emit `"{{ .OperatorIP }}";` under `vinyl_purge_allowed` when non-empty.
- [internal/generator/generator_test.go](internal/generator/generator_test.go) — unit tests: operator IP present when set; absent when empty; coexists with `allowedSources`.
- [internal/controller/vinylcache_controller.go](internal/controller/vinylcache_controller.go) — pass `OperatorIP: r.OperatorIP` when calling `Generator.Generate`.

Each file has one responsibility; no restructuring is needed.

---

### Task 1: Add `OperatorIP` to `generator.Input`

**Files:**
- Modify: [internal/generator/generator.go](internal/generator/generator.go) (around lines 22-29, the `Input` struct)

- [ ] **Step 1: Extend the struct**

Edit the `Input` struct so it reads:

```go
// Input contains everything the generator needs to produce VCL.
type Input struct {
	Spec      *vinylv1alpha1.VinylCacheSpec
	Peers     []PeerBackend         // cluster peers (other pods in the StatefulSet)
	Endpoints map[string][]Endpoint // backend name -> list of endpoints
	Namespace string                // VinylCache namespace (for VCL name)
	Name      string                // VinylCache name (for VCL name)

	// OperatorIP is the pod IP of the cloud-vinyl operator instance that
	// is reconciling this VinylCache. When non-empty it is added to the
	// vinyl_purge_allowed ACL so the operator's invalidation proxy can
	// forward PURGE requests into Varnish. Empty in tests / pre-GA startup.
	OperatorIP string
}
```

The field is embedded in `TemplateData` (via `Input`) automatically — templates can reference `.OperatorIP` directly.

- [ ] **Step 2: Build clean**

Run: `go build ./...`
Expected: no output, exit 0.

- [ ] **Step 3: Commit**

```bash
git add internal/generator/generator.go
git commit -m "feat(generator): add OperatorIP field to Input

Plumbs the operator pod IP through the generator so the PURGE ACL
can include it (issue #18). No behaviour change yet — template and
caller updates land in follow-up commits.

Refs #18"
```

---

### Task 2: Failing tests for ACL emission

**Files:**
- Modify: [internal/generator/generator_test.go](internal/generator/generator_test.go) (append)

- [ ] **Step 1: Write the three tests**

Append at the end of the file:

```go
func TestGenerate_OperatorIP_PresentInPurgeACL(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.OperatorIP = "10.244.1.7"
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "acl vinyl_purge_allowed {")
	assert.Contains(t, r.VCL, `"10.244.1.7";`,
		"operator IP must appear in vinyl_purge_allowed")
	assert.Contains(t, r.VCL, `"127.0.0.1";`,
		"localhost entry must remain")
}

func TestGenerate_OperatorIP_OmittedWhenEmpty(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	// OperatorIP left at zero value.
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "acl vinyl_purge_allowed {")
	// Only localhost must be in the ACL (no user allowedSources in the minimal input).
	purgeACL := strings.SplitAfter(r.VCL, "acl vinyl_purge_allowed {")[1]
	purgeACL = strings.SplitN(purgeACL, "}", 2)[0]
	assert.Equal(t, 1, strings.Count(purgeACL, `"127.0.0.1";`),
		"minimal input must emit exactly one ACL entry (localhost)")
	assert.NotContains(t, purgeACL, "10.244.",
		"no operator IP entry when OperatorIP is empty")
}

func TestGenerate_OperatorIP_CoexistsWithAllowedSources(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.OperatorIP = "10.244.1.7"
	input.Spec.Invalidation.Purge = &vinylv1alpha1.PurgeSpec{
		Enabled:        true,
		AllowedSources: []string{"10.0.0.0/8", "192.168.1.0/24"},
	}
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, `"10.244.1.7";`)
	assert.Contains(t, r.VCL, `"10.0.0.0/8";`)
	assert.Contains(t, r.VCL, `"192.168.1.0/24";`)
	assert.Contains(t, r.VCL, `"127.0.0.1";`)
}
```

If `"strings"` is not yet imported in the test file, add it.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/generator/ -run TestGenerate_OperatorIP -v`
Expected: all three FAIL — the template does not emit `.OperatorIP` yet.

- [ ] **Step 3: Commit (red state is intentional — template lands next)**

```bash
git add internal/generator/generator_test.go
git commit -m "test(generator): red tests for OperatorIP in purge ACL

Three cases locking in the emission contract: present when set,
absent when empty, coexists with user-configured allowedSources.
Template lands in the next commit.

Refs #18"
```

---

### Task 3: Emit `OperatorIP` in `acls.vcl.tmpl`

**Files:**
- Modify: [internal/generator/templates/acls.vcl.tmpl](internal/generator/templates/acls.vcl.tmpl)

- [ ] **Step 1: Update the template**

Replace the current content of `internal/generator/templates/acls.vcl.tmpl` with:

```
acl vinyl_purge_allowed {
    "127.0.0.1";  // localhost always allowed
{{- if .OperatorIP }}
    "{{ .OperatorIP }}";  // operator pod IP (invalidation proxy source)
{{- end }}
{{- if .Spec.Invalidation.Purge }}{{ range .Spec.Invalidation.Purge.AllowedSources }}
    "{{ . }}";
{{- end }}{{ end }}
}
{{- if .HasCluster }}

acl vinyl_cluster_peers {
{{- range .PeerDefs }}
    "{{ .IP }}";
{{- end }}
}
{{- end }}
```

- [ ] **Step 2: Run the new tests — they must pass**

Run: `go test ./internal/generator/ -run TestGenerate_OperatorIP -v`
Expected: all three PASS.

- [ ] **Step 3: Run the whole generator suite — no regressions**

Run: `go test ./internal/generator/ -count=1`
Expected: `ok  github.com/bluedynamics/cloud-vinyl/internal/generator`.

- [ ] **Step 4: Commit**

```bash
git add internal/generator/templates/acls.vcl.tmpl
git commit -m "feat(generator): emit operator IP in vinyl_purge_allowed

ACL now includes the operator pod IP (when Input.OperatorIP is set)
between the localhost entry and the user-configured allowedSources.
Keeps the CRD surface unchanged — users don't need to add the
operator IP to spec.invalidation.purge.allowedSources by hand.

Refs #18"
```

---

### Task 4: Pass `OperatorIP` from reconciler to generator

**Files:**
- Modify: [internal/controller/vinylcache_controller.go](internal/controller/vinylcache_controller.go) (around lines 172-178, the `r.Generator.Generate(generator.Input{...})` call)

- [ ] **Step 1: Thread the field through**

Replace the call:

```go
	genResult, err := r.Generator.Generate(generator.Input{
		Spec:      &vc.Spec,
		Peers:     peers,
		Endpoints: endpoints,
		Namespace: vc.Namespace,
		Name:      vc.Name,
	})
```

with:

```go
	genResult, err := r.Generator.Generate(generator.Input{
		Spec:       &vc.Spec,
		Peers:      peers,
		Endpoints:  endpoints,
		Namespace:  vc.Namespace,
		Name:       vc.Name,
		OperatorIP: r.OperatorIP,
	})
```

Note the alignment realignment — `gofmt` will adjust colons automatically; no manual tweak needed.

- [ ] **Step 2: Build clean**

Run: `go build ./...`
Expected: no output.

- [ ] **Step 3: Run the full controller suite — envtest must not regress**

Run: `go test ./internal/controller/ -count=1 -timeout 3m`
Expected: `ok  github.com/bluedynamics/cloud-vinyl/internal/controller`.

- [ ] **Step 4: Commit**

```bash
git add internal/controller/vinylcache_controller.go
git commit -m "feat(controller): pass OperatorIP to the VCL generator

Closes the plumbing for #18: the reconciler already tracks its own
pod IP (POD_IP env), now it reaches the VCL so PURGE requests from
the invalidation proxy pass the vinyl_purge_allowed ACL.

Refs #18"
```

---

### Task 5: End-to-end verification + PR

**Files:** none modified; commands only.

- [ ] **Step 1: Run the entire test suite**

Run: `go test ./... -count=1 -timeout 5m`
Expected:

```
ok  github.com/bluedynamics/cloud-vinyl/internal/agent
ok  github.com/bluedynamics/cloud-vinyl/internal/controller
ok  github.com/bluedynamics/cloud-vinyl/internal/generator
ok  github.com/bluedynamics/cloud-vinyl/internal/monitoring
ok  github.com/bluedynamics/cloud-vinyl/internal/proxy
ok  github.com/bluedynamics/cloud-vinyl/internal/webhook
ok  github.com/bluedynamics/cloud-vinyl/internal/webhook/v1alpha1
```

- [ ] **Step 2: Build docs (strict warnings)**

Run: `cd docs && make docs && cd ..`
Expected: `build succeeded.`

No docs need editing — the behavioural change is operator-internal and does not surface as a new CRD field.

- [ ] **Step 3: Push branch and open PR**

```bash
git push -u origin feat/operator-ip-in-acl
gh pr create --title "feat: include operator pod IP in vinyl_purge_allowed ACL (#18)" \
  --body "$(cat <<'BODY'
## Summary

Plumbs the operator's pod IP through \`generator.Input\` into the emitted \`vinyl_purge_allowed\` ACL so PURGE requests forwarded by the invalidation proxy pass Varnish's source check. Previously the reconciler held the IP but never handed it to the generator, so operators had to add it to \`spec.invalidation.purge.allowedSources\` by hand.

## Behaviour

- When \`POD_IP\` is set (production — always), the generated ACL now contains the operator IP:
  \`\`\`vcl
  acl vinyl_purge_allowed {
      \"127.0.0.1\";
      \"10.244.1.7\";   // operator pod IP
      \"10.0.0.0/8\";   // user allowedSources (unchanged)
  }
  \`\`\`
- When \`OperatorIP\` is empty (tests, pre-GA startup), the template emits only the previous entries.

## Tests

- \`TestGenerate_OperatorIP_PresentInPurgeACL\` — IP appears when set.
- \`TestGenerate_OperatorIP_OmittedWhenEmpty\` — no extra entry when empty.
- \`TestGenerate_OperatorIP_CoexistsWithAllowedSources\` — user-configured CIDRs still land.

Full \`go test ./...\` green.

## Out of scope

BAN ACL and BAN handling in \`vcl_recv\` are not present in the repo today. Adding them is a separate feature.

Closes #18
BODY
)"
```

- [ ] **Step 4: Confirm PR URL is printed**

Expected: `https://github.com/bluedynamics/cloud-vinyl/pull/<N>`.

---

## Release hook-in

No tag is cut by this plan. Once the PR merges, the next scheduled release (or a manual patch release per operator decision) ships the fix. Since #18 is a feature-adjacent bug-fix (operator-side convenience; no CRD change, no breaking change), bundling with other fixes under `v0.4.2` is reasonable.
