# Upgrade cloud-vinyl

## Upgrade the operator

```bash
helm upgrade cloud-vinyl oci://ghcr.io/bluedynamics/charts/cloud-vinyl \
  --namespace cloud-vinyl-system \
  --reuse-values \
  --wait --timeout 120s
```

The operator Deployment is updated with a rolling strategy. Existing VinylCache clusters
continue to serve traffic during the upgrade — the operator pushes new VCL only when the
spec changes or a pod restarts.

Each operator release also restarts every cache pod once. The agent sidecar image (and,
with `spec.tracing`, the tracer image) follows the release, so the StatefulSets roll. The
pods restart one at a time, but each one comes back with an empty cache, and a storage
on an `emptyDir` starts cold. Plan upgrades for a quiet period. Pinning
`image.agent.tag` avoids the roll, but leaves the agent on a different version than the
operator, which is not supported.

(upgrade-1-1-metric-labels)=

## Upgrading to 1.1: renamed metric labels

1.1 renames two labels of the operator's `vinyl_*` metrics (#155). Before, they
collided with the target labels Prometheus attaches to every series scraped
from the operator pod, so Prometheus renamed them to `exported_namespace` and
`exported_pod`, and `namespace` on every `vinyl_*` series was the operator's
namespace instead of the cache's.

| Metric | Before 1.1 | As scraped before 1.1 | Since 1.1 |
|---|---|---|---|
| all `vinyl_*` with a cache's namespace | `namespace` | `exported_namespace` | `cache_namespace` |
| `vinyl_broadcast_total` | `pod` (an `ip:port`) | `exported_pod` | `pod_addr` |
| `vinyl_broadcast_total` | (none) | (none) | `cache`, `cache_namespace` (new) |

The alert rules from the chart and from the operator use the new labels. Update
your own dashboards, recording rules and alert routes: replace
`exported_namespace` with `cache_namespace` and `exported_pod` with `pod_addr`
on `vinyl_*` metrics. The exporter's `varnish_*` metrics are unchanged.

## Upgrade the CRD

CRDs in `charts/cloud-vinyl/crds/` are installed on `helm install` but are **not** updated
automatically by `helm upgrade`. This is intentional — Helm never deletes CRDs, and CRD
schema changes require careful migration.

To update the CRD manually, use server-side apply:

```bash
kubectl apply --server-side --force-conflicts \
  -f https://raw.githubusercontent.com/bluedynamics/cloud-vinyl/main/config/crd/bases/vinyl.bluedynamics.eu_vinylcaches.yaml
```

Since 1.0 the CRD serves two API versions, `v1` and the deprecated `v1alpha1`, each with
the full schema. That makes it too large for a plain `kubectl apply`, which stores the
whole object in the `last-applied-configuration` annotation and fails with
`metadata.annotations: Too long: may not be more than 262144 bytes`.
`--force-conflicts` takes over fields that an earlier client-side apply owned.

For GitOps workflows, manage the CRD separately and render or install the chart with
`--skip-crds`. The tool has to apply the CRD server-side as well, for example with the
Argo CD sync option `ServerSideApply=true`.

## Rollback

```bash
helm rollback cloud-vinyl --namespace cloud-vinyl-system
```

After rollback, the operator downgrades automatically. VinylCache clusters are not affected
unless the CRD schema changed between versions.
