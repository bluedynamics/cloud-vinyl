# Architecture

cloud-vinyl is a Kubernetes operator that manages [Vinyl Cache](https://vinyl-cache.org/) clusters
(the FOSS HTTP cache formerly known as Varnish Cache) as first-class Kubernetes resources.
It is designed around the Kubernetes operator pattern with a central controller instead of per-pod sidecars.

## Components

```{mermaid}
graph TD
    subgraph Kubernetes cluster
        OP[cloud-vinyl operator<br/>Deployment]
        VC[VinylCache CR]
        SS[StatefulSet<br/>Vinyl Cache pods]
        AG[vinyl-agent<br/>sidecar per pod]
        PX[Purge/BAN proxy<br/>port 8090]
        WH[Admission webhook<br/>port 9443]
    end
    VC -->|reconcile| OP
    OP -->|manages| SS
    SS -->|contains| AG
    OP -->|pushes VCL| AG
    AG -->|manages Vinyl Cache| VCL[Vinyl Cache process]
    PX -->|PURGE/BAN broadcast| SS
    WH -->|defaults + validates| VC
```

### cloud-vinyl operator

The operator is a single Deployment that runs the reconcile loop. It:

- Watches `VinylCache` custom resources.
- Reconciles StatefulSets, Services, Secrets, EndpointSlices, and NetworkPolicies.
- Generates VCL from the `VinylCache` spec using Go templates.
- Pushes generated VCL to each ready pod via the **vinyl-agent** HTTP API.
- Exposes metrics on port `8080` and serves admission webhooks on port `9443`.
- Runs a Purge/BAN proxy on port `8090` that broadcasts cache-invalidation requests to all pods.

### vinyl-agent

A lightweight HTTP server running as a sidecar in each Vinyl Cache pod.
It wraps the Vinyl Cache admin interface (port 6082) and exposes:

- `POST /vcl/push` — compile and activate a new VCL.
- `GET /vcl/active` — return the hash of the currently active VCL.
- `POST /ban` — issue a ban command.

Communication between the operator and vinyl-agent is authenticated with a pod-scoped token stored in a Kubernetes Secret.

### Purge/BAN proxy

The operator exposes an HTTP endpoint on port `8090` that accepts:

- `PURGE /<path>` — HTTP PURGE broadcast to all Vinyl Cache pods.
- `POST /ban` or `BAN` method — validated ban expression, forwarded to vinyl-agent `/ban` on all pods.
- `POST /purge/xkey` — xkey-based purge, one `PURGE` per key with `X-Xkey-Purge` header.

Upstream services send a single request; the operator fans it out to all pods in parallel.

## Why a central operator instead of sidecars?

A per-pod signaller sidecar design that watches the Kubernetes Endpoints API and triggers VCL reloads
has several structural problems:

1. **Chicken-and-egg at startup**: pods with readiness gates cannot become ready until the sidecar
   receives the first VCL push — but the sidecar needs other pods to be ready to build the peer list.
2. **Silent drop on failure**: if a VCL push fails, the sidecar logs and continues.
   Pods silently run stale VCL.
3. **No debouncing**: rapid endpoint churn (rolling restarts) triggers many VCL regenerations back-to-back.

cloud-vinyl solves all three:

1. The operator pushes VCL after the pod passes its health probes — no readiness gate deadlock.
2. The reconcile loop retries on failure with exponential backoff (configurable via `spec.retry`).
3. Debouncing is built in (`spec.debounce.duration`, default 5 s).

## RBAC scope

The operator requires a `ClusterRole` because it creates resources in user namespaces
(the namespace where the `VinylCache` resource lives, which may differ from the operator namespace).
Specifically it manages: StatefulSets, Services, Secrets, EndpointSlices, NetworkPolicies,
and Leases (for leader election).
