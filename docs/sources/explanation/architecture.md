# Architecture

cloud-vinyl is a Kubernetes operator that manages [Vinyl Cache](https://vinyl-cache.org/) clusters (the FOSS HTTP cache formerly known as Varnish Cache) as first-class Kubernetes resources.
It is designed around the Kubernetes operator pattern with a central controller instead of per-pod sidecars.

(architecture-components)=

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

The operator is a single Deployment that runs the reconcile loop.
It does the following:


- Watches `VinylCache` custom resources.
- Reconciles StatefulSets, Services, Secrets, EndpointSlices, and NetworkPolicies.
- Generates VCL from the `VinylCache` spec using Go templates.
- Pushes generated VCL to each ready pod via the **vinyl-agent** HTTP API.
- Exposes metrics on port `8080` and serves admission webhooks on port `9443`.
- Runs a Purge/BAN proxy on port `8090` that broadcasts cache-invalidation requests to all pods.

### vinyl-agent

A lightweight HTTP server running as a sidecar in each Vinyl Cache pod.
It wraps the Vinyl Cache admin interface (port 6082) and exposes:

- `POST /vcl/push`: compile and activate a new VCL.
- `GET /vcl/active`: return the hash of the currently active VCL.
- `POST /ban`: issue a ban command.

Communication between the operator and vinyl-agent is authenticated with a Bearer token.
The token is stored in a per-namespace Kubernetes Secret (`cloud-vinyl-agent-token`), shared by all VinylCache instances in the same namespace.
The operator reads the token from the Secret before each push request.

### Purge/BAN proxy

The operator exposes an HTTP endpoint on port `8090` that accepts:

- `PURGE /<path>`: HTTP PURGE broadcast to all Vinyl Cache pods.
- `POST /ban` or `BAN` method: validated ban expression, forwarded to vinyl-agent `/ban` on all pods.
- `POST /purge/xkey`: xkey-based purge, one `PURGE` per key with `X-Xkey-Purge` header.

Upstream services send a single request.
The operator fans it out to all pods in parallel.

#### How the proxy finds the right cache

The operator is a single cluster-wide Deployment.
A request hitting port 8090 could belong to *any* `VinylCache` in *any* namespace.
The proxy disambiguates via the HTTP **`Host` header**.
The operator bends Kubernetes DNS to make that work for callers.

For each `VinylCache` named `my-cache` in namespace `app`, the reconciler creates:

1. A `Service` named `my-cache-invalidation` in `app` (ClusterIP, port 8090).
2. An `EndpointSlice` for that Service whose only endpoint is **the operator's own pod IP** (sourced from the `POD_IP` environment variable via the downward API).

Result: a caller that sends

```text
PURGE /path  Host: my-cache-invalidation.app.svc.cluster.local
```

hits the Service, then kube-proxy, then the operator pod, because the Service's only endpoint is the operator.
The proxy inside the operator runs a `RegisteredRouter` that the controller fills with one entry per `VinylCache`:

```text
my-cache-invalidation.app                     → {app, my-cache}
my-cache-invalidation.app.svc.cluster.local   → {app, my-cache}
other-cache-invalidation.billing              → {billing, other-cache}
other-cache-invalidation.billing.svc.cluster.local → {billing, other-cache}
```

On each request, the proxy:

1. Reads the `Host` header (stripping port).
2. Looks it up in the `RegisteredRouter` to obtain `{namespace, cacheName}`.
   No match returns `404`.
3. Looks up the list of ready pod IPs for that cache in a parallel `PodMap`.
4. Broadcasts PURGE/BAN to every pod.
   It aggregates per-pod results into the JSON response.

The discriminator is the FQDN.
Two caches with the same name in different namespaces (`foo` in `a` and `foo` in `b`) are therefore unambiguous.
The hosts `foo-invalidation.a` and `foo-invalidation.b` are distinct.

The bare Service name (`foo-invalidation`) is never routed, even while only one namespace has a cache of that name.
It is the same string from every namespace.
Accepting it would let a working client break, or purge the wrong cache, as soon as another namespace creates a `foo`.
The proxy rejects it with a `404` that names the namespaced host or hosts.
It counts the rejection in `vinyl_invalidation_unknown_host_total`.
The cache's `status.invalidationURL` carries the address to use.

**Gotcha, source IPs after SNAT.**
The proxy's source-CIDR ACL (`spec.invalidation.{purge,ban}.allowedSources`) sees the IP of whoever connected to the Service.
For most CNIs that is the **kube-proxy SNAT source**.
It is not the original caller.
If you want tight source filtering, place it at an ingress or policy layer that preserves the client IP.

(architecture-why-central-operator)=

## Why a central operator instead of sidecars?

A per-pod signaller sidecar design that watches the Kubernetes Endpoints API and triggers VCL reloads has several structural problems:

1. **Chicken-and-egg at startup**: pods with readiness gates cannot become ready until the sidecar receives the first VCL push.
   The sidecar needs other pods to be ready to build the peer list.
2. **Silent drop on failure**: if a VCL push fails, the sidecar logs and continues.
   Pods silently run stale VCL.
3. **No debouncing**: rapid endpoint churn (rolling restarts) triggers many VCL regenerations back-to-back.

cloud-vinyl solves all three:

1. The operator pushes VCL after the pod passes its health probes.
   No readiness gate deadlock occurs.
2. The reconcile loop retries on failure with exponential backoff (configurable via `spec.retry`).
3. Debouncing is built in (`spec.debounce.duration`, default 1 s).

(architecture-rbac-scope)=

## RBAC scope

The operator requires a `ClusterRole` because it creates resources in user namespaces.
These are the namespaces where the `VinylCache` resources live, which may differ from the operator namespace.
It manages StatefulSets, Services, Secrets, EndpointSlices, NetworkPolicies, and Leases (for leader election).
