(switch-off-a-cache)=
# Switch off a cache

Some environments are switched off on purpose, for example a staging site over the weekend or a preview environment nobody uses.
Set `spec.replicas` to `0` to stop the cache pods without deleting the VinylCache.

(switch-off-scale-to-zero)=

## Scale to zero

```shell
kubectl scale vinylcache my-cache --replicas=0
```

Alternatively, set `spec.replicas: 0` in the manifest.
The operator scales the StatefulSet to `0`.
The Services, the Secret and the other resources stay in place.
No VCL is pushed because there is no pod.

(switch-off-status)=

## What the status shows

```shell
kubectl get vinylcache my-cache -o wide
```

- `status.phase` is `Stopped`.
- `Ready` is `True` with reason `ScaledToZero` (message `scaled to zero replicas`).
  `VCLSynced` and `Progressing` carry the same reason.
- `Stopped` wins over `Degraded`.
  A switched-off site usually has backends without endpoints.
  `BackendsAvailable` still reports that truthfully, but the cache is not shown as degraded.

The invalidation service answers `503` while there is no pod to forward to.

(switch-off-argo-cd)=

## Argo CD

With `argocd.resourceCustomizations.enabled`, the health script reads the phase first.
`Stopped` is shown as `Suspended`, `Degraded` and `Error` as `Degraded`, `Ready` as `Healthy`.
A switched-off environment therefore does not show up as unhealthy.

(switch-off-switch-on)=

## Switch it on again

```shell
kubectl scale vinylcache my-cache --replicas=3
```

The pods start, receive the VCL, and the phase follows the usual rules (`Pending`, then `Ready`).
The cache starts cold: objects cached before the switch-off are gone.

See {ref}`vinylcache-conditions` for all conditions.
