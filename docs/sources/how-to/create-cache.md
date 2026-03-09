# Create a VinylCache cluster

This guide creates a minimal Vinyl Cache cluster using a `VinylCache` custom resource.

## Minimal example

```yaml
apiVersion: vinyl.bluedynamics.eu/v1alpha1
kind: VinylCache
metadata:
  name: my-cache
  namespace: default
spec:
  replicas: 2
  backends:
    - name: app
      host: app-service.default.svc.cluster.local
      port: 8080
```

Apply it:

```bash
kubectl apply -f vinylcache.yaml
```

Watch the cluster come up:

```bash
kubectl get vinylcache my-cache -w
```

The operator creates:
- A `StatefulSet` with 2 Vinyl Cache pods (+ vinyl-agent sidecar per pod).
- A headless `Service` for pod-to-pod communication.
- A traffic `Service` for upstream connections.
- An invalidation `Service` + `EndpointSlice` for the operator's Purge/BAN proxy.
- `NetworkPolicies` restricting inter-pod and operator communication.
- A `Secret` with the agent authentication token.

## With clustering (shard director)

For 3+ replicas, the shard director distributes requests consistently across pods.
It is enabled automatically when `replicas > 1` and the `cluster.enabled: true` flag is set:

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

## With xkey invalidation

```yaml
spec:
  replicas: 2
  backends:
    - name: app
      host: app-service.default.svc.cluster.local
      port: 8080
  invalidation:
    purge:
      soft: true
    xkey:
      softPurge: true
```

## Check status

```bash
kubectl describe vinylcache my-cache
```

The status shows:
- `Ready` condition — all pods running and VCL synced.
- `VCLSynced` condition — VCL hash matches on all pods.
- `readyReplicas` — number of healthy pods.
- `phase` — one of `Pending`, `Ready`, `Degraded`, `Error`.
