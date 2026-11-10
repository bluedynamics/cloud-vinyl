# Create a `VinylCache` cluster

This guide creates a minimal Vinyl Cache cluster using a `VinylCache` custom resource.

(create-cache-minimal-example)=

## Minimal example

```yaml
apiVersion: vinyl.bluedynamics.eu/v1
kind: VinylCache
metadata:
  name: my-cache
  namespace: default
spec:
  replicas: 2
  backends:
    - name: app
      serviceRef:
        name: app-service
      port: 8080
```

`spec.image` is optional.
Without it, the operator runs the default `varnish` version of its {term}`runtime catalog`.
To run Vinyl Cache instead, set `flavor: vinyl` (see {ref}`run-vinyl-cache`).

Requests that no `vclRecv` snippet routes go to the first backend.
The backend's {term}`director` spreads them across its pods.
The director is `shard` by default.
The same URL stays on the same pod.
With two or more backends, choose between them in a `vclRecv` snippet.
See {ref}`default-routing`.

Apply it:

```shell
kubectl apply -f vinylcache.yaml
```

Watch the cluster come up:

```shell
kubectl get vinylcache my-cache -w
```

The operator creates:

- A `StatefulSet` with 2 Vinyl Cache pods (plus a vinyl-agent sidecar per pod).
- A headless `Service` for pod-to-pod communication.
- A traffic `Service` for upstream connections.
- An invalidation `Service` and `EndpointSlice` for the operator's {term}`Purge <PURGE>`/{term}`BAN` proxy.
- `NetworkPolicies` restricting inter-pod and operator communication.
- A `Secret` with the agent authentication token.

(create-cache-clustering)=

## With clustering (shard director)

For 3 or more replicas, the shard director distributes requests consistently across pods.
The operator enables it automatically when `replicas > 1` and `cluster.enabled` is `true`:

```yaml
spec:
  replicas: 3
  cluster:
    enabled: true
  director:
    type: shard
    shard:
      warmup: 0.1
      rampup: 30s
```

(create-cache-xkey)=

## With xkey invalidation

```yaml
spec:
  replicas: 2
  backends:
    - name: app
      serviceRef:
        name: app-service
      port: 8080
  invalidation:
    purge:
      soft: true
    xkey:
      enabled: true
```

(create-cache-check-status)=

## Check status

```shell
kubectl describe vinylcache my-cache
```

The status shows:

- `Ready` condition: all pods running and {term}`VCL` synced.
- `VCLSynced` condition: VCL hash matches on all pods.
- `readyReplicas`: number of healthy pods.
- `phase`: one of `Pending`, `Ready`, `Degraded`, `Error`, `Stopped`.
