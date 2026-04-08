# Invalidation Proxy Integration Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire the existing invalidation proxy code (`internal/proxy/`) into the operator so PURGE/BAN/xkey requests are broadcast to all VinylCache pods.

**Architecture:** The proxy HTTP server runs on `:8090` in the operator pod (ALL replicas, not just leader). The controller reconciler updates a shared `PodMap` and `RegisteredRouter` whenever VinylCache objects or their pods change. BAN requests are forwarded to the vinyl-agent API (port 9090) with Bearer auth read from the per-namespace Secret.

**Tech Stack:** Go 1.25, controller-runtime, `internal/proxy` package (already implemented)

**Reference:** Issue #15, architecture doc §6

---

## File Map

| File | Responsibility | Changes |
|------|---------------|---------|
| `cmd/operator/main.go` | Operator entry point | Create proxy singletons, start proxy server, pass to reconciler |
| `internal/controller/vinylcache_controller.go` | Reconcile loop | Add PodMap/Router fields, update on reconcile/delete |
| `internal/controller/vcl_push.go` | Pod IP collection | Already has `collectReadyPeers` — reuse for PodMap |
| `internal/proxy/broadcast.go` | HTTP broadcast | Add Bearer token support for agent auth |

---

### Task 1: Add Bearer token support to HTTPBroadcaster

**Files:**
- Modify: `internal/proxy/broadcast.go`
- Modify: `internal/proxy/broadcast_test.go`

BAN requests are broadcast to the agent on port 9090, which requires Bearer auth. The broadcaster needs to include an `Authorization` header when one is provided in the `BroadcastRequest.Headers`.

This already works — `BroadcastRequest.Headers` is a `map[string]string` that gets set on the outgoing HTTP request via `httpReq.Header.Set(k, v)` in `callPod()`. The handler in `handler.go` sets headers for BAN requests but currently only sets `Content-Type`. We need the proxy to include the agent token.

The token needs to be injected by the controller into the proxy. The simplest approach: the handler reads the token from a `TokenProvider` before broadcasting.

- [ ] **Step 1: Add TokenProvider interface and field to Server**

In `internal/proxy/server.go`, add a `TokenProvider` interface and field:

```go
// TokenProvider returns the agent Bearer token for a given namespace.
// Returns empty string if no token is available (requests will be unauthenticated).
type TokenProvider interface {
	GetToken(namespace string) string
}

// NoopTokenProvider returns empty tokens (no auth).
type NoopTokenProvider struct{}

func (n *NoopTokenProvider) GetToken(_ string) string { return "" }
```

Add `tokenProvider TokenProvider` field to `Server` struct and to `NewServer`:

```go
type Server struct {
	addr          string
	router        Router
	podMap        PodIPProvider
	broadcaster   Broadcaster
	tokenProvider TokenProvider
	acl           map[string]*ACL
	rateLimiter   RateLimiter
}

func NewServer(addr string, router Router, pods PodIPProvider, b Broadcaster, tp TokenProvider) *Server {
	if tp == nil {
		tp = &NoopTokenProvider{}
	}
	return &Server{
		addr:          addr,
		router:        router,
		podMap:        pods,
		broadcaster:   b,
		tokenProvider: tp,
		acl:           make(map[string]*ACL),
		rateLimiter:   &NoopRateLimiter{},
	}
}
```

- [ ] **Step 2: Pass namespace to handlers and inject token for BAN/xkey**

In `internal/proxy/server.go` `ServeHTTP`, pass namespace to handlers. Change the dispatch calls:

```go
	// 5. Dispatch.
	switch {
	case r.Method == "PURGE":
		s.handlePurge(w, r, pods)
	case r.Method == "BAN":
		s.handleBAN(w, r, pods, namespace)
	case r.Method == http.MethodPost && r.URL.Path == "/ban":
		s.handleBAN(w, r, pods, namespace)
	case r.Method == http.MethodPost && r.URL.Path == "/purge/xkey":
		s.handleXkey(w, r, pods)
	default:
		writeJSONError(w, http.StatusNotFound, "no route for "+r.Method+" "+r.URL.Path)
	}
```

In `internal/proxy/handler.go`, update `handleBAN` to accept namespace and add the token:

```go
func (s *Server) handleBAN(w http.ResponseWriter, r *http.Request, pods []string, namespace string) {
	// ... existing validation code unchanged ...

	podAddrs := withPort(pods, agentPort)
	bodyBytes, _ := json.Marshal(banRESTRequest{Expression: expression})

	headers := map[string]string{"Content-Type": "application/json"}
	if token := s.tokenProvider.GetToken(namespace); token != "" {
		headers["Authorization"] = "Bearer " + token
	}

	req := BroadcastRequest{
		Method:  http.MethodPost,
		Path:    "/ban",
		Headers: headers,
		Body:    bodyBytes,
	}

	ctx, cancel := context.WithTimeout(r.Context(), broadcastTimeout)
	defer cancel()

	result := s.broadcaster.Broadcast(ctx, podAddrs, req)
	WriteResult(w, result)
}
```

- [ ] **Step 3: Update existing tests for NewServer signature change**

All `NewServer` calls in tests need the extra `TokenProvider` parameter. Search all test files and add `nil` (or `&NoopTokenProvider{}`):

Run: `grep -rn "NewServer(" internal/proxy/*_test.go`

Update each call. For example in `server_test.go`:
```go
s := NewServer(":0", router, podMap, broadcaster, nil)
```

- [ ] **Step 4: Build and run tests**

Run: `cd /home/jensens/ws/bda/cloud-vinyl && go build ./... && go test ./internal/proxy/ -v`
Expected: All pass.

- [ ] **Step 5: Commit**

```bash
cd /home/jensens/ws/bda/cloud-vinyl && git add internal/proxy/ && git commit -m "feat: add TokenProvider to proxy for agent BAN auth

BAN requests are forwarded to the agent on port 9090 which requires
Bearer auth. The proxy reads the token from a TokenProvider (injected
by the controller) and includes it in the Authorization header.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Add PodMap and Router fields to VinylCacheReconciler

**Files:**
- Modify: `internal/controller/vinylcache_controller.go`

The reconciler needs references to the proxy's PodMap and Router to update them during reconciliation.

- [ ] **Step 1: Add fields to VinylCacheReconciler**

In `internal/controller/vinylcache_controller.go`, add optional fields for the proxy components. Import the proxy package:

```go
import (
	// ... existing imports ...
	"github.com/bluedynamics/cloud-vinyl/internal/proxy"
)
```

Add fields to the struct:

```go
type VinylCacheReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	Generator   generator.Generator
	AgentClient AgentClient
	OperatorIP  string
	// Proxy integration (optional — nil when proxy is disabled).
	ProxyRouter *proxy.RegisteredRouter
	ProxyPodMap *proxy.PodMap
}
```

- [ ] **Step 2: Update reconcile loop to register routes and update pod IPs**

After the `collectReadyPeers` call (around line 127), add:

```go
	// 11b. Update proxy routing and pod map.
	if r.ProxyRouter != nil {
		r.ProxyRouter.Register(vc.Namespace, vc.Name)
	}
	if r.ProxyPodMap != nil {
		var podIPs []string
		for _, p := range peers {
			podIPs = append(podIPs, p.IP)
		}
		r.ProxyPodMap.Update(vc.Namespace, vc.Name, podIPs)
	}
```

- [ ] **Step 3: Update handleDeletion to clean up proxy state**

In `internal/controller/finalizer.go`, in `handleDeletion()`, before removing the finalizer, add cleanup:

```go
	// Clean up proxy routing and pod map.
	if r.ProxyRouter != nil {
		r.ProxyRouter.Unregister(vc.Namespace, vc.Name)
	}
	if r.ProxyPodMap != nil {
		r.ProxyPodMap.Delete(vc.Namespace, vc.Name)
	}
```

Note: `handleDeletion` is on `*VinylCacheReconciler` so it has access to these fields.

- [ ] **Step 4: Build and run tests**

Run: `cd /home/jensens/ws/bda/cloud-vinyl && go build ./... && go test ./...`
Expected: All pass. The fields are nil by default, so existing tests and operator startup are unaffected.

- [ ] **Step 5: Commit**

```bash
cd /home/jensens/ws/bda/cloud-vinyl && git add internal/controller/ && git commit -m "feat: wire proxy PodMap and Router into reconciler

Reconciler updates PodMap with ready peer IPs and registers/unregisters
routes on VinylCache create/delete. Fields are optional (nil-safe) so
the proxy can be disabled without code changes.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Implement K8sTokenProvider for agent auth

**Files:**
- Create: `internal/controller/token_provider.go`

The proxy needs to read the per-namespace agent token from the `cloud-vinyl-agent-token` Secret. This is the same Secret used by the `HTTPAgentClient`. Create a `TokenProvider` implementation that uses the K8s client.

- [ ] **Step 1: Create token_provider.go**

Create `internal/controller/token_provider.go`:

```go
/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
...
*/

package controller

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// K8sTokenProvider reads agent tokens from Kubernetes Secrets.
// It implements proxy.TokenProvider.
type K8sTokenProvider struct {
	client client.Reader
}

// NewK8sTokenProvider creates a new K8sTokenProvider.
func NewK8sTokenProvider(c client.Reader) *K8sTokenProvider {
	return &K8sTokenProvider{client: c}
}

// GetToken reads the agent-token from the per-namespace Secret.
// Returns empty string on error (unauthenticated fallback).
func (p *K8sTokenProvider) GetToken(namespace string) string {
	log := logf.Log.WithName("token-provider")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	secret := &corev1.Secret{}
	key := types.NamespacedName{Name: agentSecretName, Namespace: namespace}
	if err := p.client.Get(ctx, key, secret); err != nil {
		log.Error(err, "Failed to read agent secret", "namespace", namespace)
		return ""
	}

	token, ok := secret.Data["agent-token"]
	if !ok {
		log.Info("Agent secret missing 'agent-token' key", "namespace", namespace)
		return ""
	}
	return string(token)
}
```

- [ ] **Step 2: Build**

Run: `cd /home/jensens/ws/bda/cloud-vinyl && go build ./...`
Expected: Success.

- [ ] **Step 3: Commit**

```bash
cd /home/jensens/ws/bda/cloud-vinyl && git add internal/controller/token_provider.go && git commit -m "feat: K8sTokenProvider reads agent token from Secret

Implements proxy.TokenProvider for the invalidation proxy. Reads
the Bearer token from the per-namespace cloud-vinyl-agent-token
Secret so BAN requests to the agent are authenticated.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Start proxy server in operator main.go

**Files:**
- Modify: `cmd/operator/main.go`

Initialize the proxy singletons and start the HTTP server. The proxy runs on ALL replicas (not just the leader) because any replica might receive an invalidation request via the Service.

- [ ] **Step 1: Add proxy initialization after manager creation**

In `cmd/operator/main.go`, after the manager is created (line 183) and before the reconciler is set up (line 185), add:

```go
	// --- Invalidation proxy (runs on all replicas, not just leader) ---
	proxyRouter := proxy.NewRegisteredRouter()
	proxyPodMap := proxy.NewPodMap()
	broadcaster := proxy.NewHTTPBroadcaster(10 * time.Second)
	tokenProvider := controller.NewK8sTokenProvider(mgr.GetClient())
	proxyServer := proxy.NewServer(":8090", proxyRouter, proxyPodMap, broadcaster, tokenProvider)

	// Start proxy in background goroutine. It must run on all replicas
	// because the invalidation Service load-balances across all operator pods.
	go func() {
		setupLog.Info("Starting invalidation proxy", "addr", ":8090")
		if err := proxyServer.Start(ctrl.SetupSignalHandler()); err != nil {
			setupLog.Error(err, "Invalidation proxy failed")
		}
	}()
```

Add imports:
```go
	"time"

	"github.com/bluedynamics/cloud-vinyl/internal/proxy"
```

- [ ] **Step 2: Pass proxy references to the reconciler**

Update the reconciler initialization to include the proxy fields:

```go
	if err := (&controller.VinylCacheReconciler{
		Client:      mgr.GetClient(),
		Scheme:      mgr.GetScheme(),
		Generator:   generator.New(),
		AgentClient: &controller.HTTPAgentClient{
			HTTPClient: &http.Client{},
			K8sClient:  mgr.GetClient(),
		},
		ProxyRouter: proxyRouter,
		ProxyPodMap: proxyPodMap,
	}).SetupWithManager(mgr); err != nil {
```

- [ ] **Step 3: Build and run tests**

Run: `cd /home/jensens/ws/bda/cloud-vinyl && go build ./... && go test ./...`
Expected: All pass.

- [ ] **Step 4: Commit**

```bash
cd /home/jensens/ws/bda/cloud-vinyl && git add cmd/operator/main.go && git commit -m "feat: start invalidation proxy on operator port 8090

Proxy runs on all replicas (not just leader) because the invalidation
Service load-balances across all operator pods. Router and PodMap
are shared with the reconciler for dynamic pod/route updates.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Final integration verification

- [ ] **Step 1: Run full test suite**

Run: `cd /home/jensens/ws/bda/cloud-vinyl && go test ./... -v`
Expected: All tests pass.

- [ ] **Step 2: Run pre-commit hooks**

Run: Attempt a commit (hooks run automatically) or `pre-commit run --all-files`
Expected: All hooks pass.

- [ ] **Step 3: Create PR**

```bash
cd /home/jensens/ws/bda/cloud-vinyl && git push -u origin feat/invalidation-proxy-integration
gh pr create --repo bluedynamics/cloud-vinyl \
  --title "feat: integrate invalidation proxy into operator (#15)" \
  --body "Wires the existing proxy code (internal/proxy/) into the operator:

1. **TokenProvider** — proxy reads agent Bearer token from K8s Secret for BAN auth
2. **Reconciler integration** — updates PodMap and Router on VinylCache create/update/delete
3. **Operator startup** — proxy HTTP server on :8090, runs on all replicas

The proxy code itself was already fully implemented. This PR only adds the integration glue.

Fixes #15."
```

---

## Verification checklist

After deployment, verify:

1. **Proxy starts**: Operator logs show "Starting invalidation proxy addr=:8090"
2. **PURGE works**: `curl -X PURGE http://<invalidation-service>:8090/some/path` returns 200 with broadcast result
3. **BAN works**: `curl -X POST http://<invalidation-service>:8090/ban -d '{"expression":"obj.http.x-url ~ ^/product/"}'` returns 200
4. **plone.cachepurging**: Edit content in Plone, verify PURGE request reaches all Varnish pods
5. **Pod changes**: Scale VinylCache up/down, verify PodMap updates (proxy broadcasts to correct pod count)
6. **Deletion**: Delete VinylCache CR, verify Router and PodMap are cleaned up (proxy returns 404 for that Host)
