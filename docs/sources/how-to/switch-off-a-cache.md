(switch-off-a-cache)=
# Switch off a cache

Teams switch off some environments on purpose, for example a staging site over the weekend or a preview environment nobody uses.
Set `spec.replicas` to `0` to stop the cache pods without deleting the VinylCache.

(switch-off-scale-to-zero)=

## Scale to zero

```shell
kubectl scale vinylcache my-cache --replicas=0
```

Or set `spec.replicas: 0` in the manifest.
The operator scales the StatefulSet to `0`.
The Services, the Secret and the other resources stay in place.
The operator pushes no {term}`VCL` because there is no pod.

(switch-off-status)=

## What the status shows

```shell
kubectl get vinylcache my-cache -o wide
```

- `status.phase` is `Stopped`.
- `Ready` is `True` with reason `ScaledToZero` (message `scaled to zero replicas`).
  `VCLSynced` and `Progressing` carry the same reason.
- `Stopped` wins over `Degraded`.
  A switched-off site typically has backends without endpoints.
  `BackendsAvailable` still reports the true state, but the operator does not mark the cache as degraded.

The invalidation service answers `503` while there is no pod to forward to.

(switch-off-argo-cd)=

## Argo CD

The Argo CD health check for VinylCache maps `Stopped` to `Suspended`.
A switched-off environment does not show up as unhealthy.
Argo CD needs the check in its `argocd-cm` ConfigMap, see {ref}`argocd-health`.

(switch-off-switch-on)=

## Switch it on again

```shell
kubectl scale vinylcache my-cache --replicas=3
```

The pods start, receive the VCL, and the phase follows the usual rules (`Pending`, then `Ready`).
The cache starts cold.
The switch-off drops every object cached before it.

See {ref}`vinylcache-conditions` for all conditions.
