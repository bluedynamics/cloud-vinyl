# Upgrade cloud-vinyl

(upgrade-operator)=

## Upgrade the operator

```shell
helm upgrade cloud-vinyl oci://ghcr.io/bluedynamics/charts/cloud-vinyl \
  --namespace cloud-vinyl-system \
  --reset-then-reuse-values \
  --wait --timeout 120s
```

Use `--reset-then-reuse-values` (Helm 3.14+).
`--reuse-values` keeps the values of the previous release and drops the defaults new chart versions add, such as the `runtimes` catalog of 1.1.
The chart then refuses to render.

The operator Deployment is updated with a rolling strategy.
Existing VinylCache clusters continue to serve traffic during the upgrade.
The operator pushes new VCL only when the spec changes or a pod restarts.

Each operator release also restarts every cache pod once.
The agent sidecar image (and, with `spec.tracing`, the tracer image) follows the release.
The StatefulSets therefore roll.
The pods restart one at a time.
Each one comes back with an empty cache, and a storage on an `emptyDir` starts cold.
Plan upgrades for a quiet period.
Pinning `image.agent.tag` avoids the roll but leaves the agent on a different version than the operator.
That combination is not supported.

(upgrade-1-1-crd-first)=

## Upgrading to 1.1: apply the CRD before `helm upgrade`

1.1 adds `spec.flavor`, `spec.version` and `status.runtime`.
Apply the new CRD (see {ref}`upgrade-crd`) **before** you run `helm upgrade`.
Until the CRD is applied, the API server prunes these fields from every VinylCache it stores.

(upgrade-1-1-runtime-catalog)=

## Upgrading to 1.1: runtime catalog and Vinyl Cache

1.1 adds the runtime catalog (see {ref}`helm-values-runtimes`) and the `vinyl` flavor (see {ref}`run-vinyl-cache`).

**Existing caches need no spec change.**
A cache with only `spec.image` keeps working.
It is admitted as flavor `varnish` with the catalog's default version.
It runs its own image as the daemon and takes its sidecars from the default catalog entry.
The webhook warns on apply that `spec.image` is set without `spec.version`.
Set `version` to silence it.

**Their pods roll once**, as with every operator release, and this release changes more than the agent:

- the tracer image comes from the catalog and the operator no longer reads a `TRACER_IMAGE` environment variable.
- the exporter moves from 1.6.1 to 1.8.3 with the same metric names.
- all sidecar images are pinned by digest.

If you pinned `image.agent.tag` to avoid rolls, only caches with the exporter or tracing enabled (or with provisioned file storage) roll once in 1.1.
The others stay put.

**An empty `spec.version` follows the chart.**
A cache without `version` runs the catalog's default version for its flavor.
It moves with the catalog when a later chart release changes the default.
Pin `version` to stay put.
`status.runtime` and the printer columns `Flavor` and `Version` show what runs.

**A catalog change rolls every affected cache.**
The operator restarts when the catalog in the chart values changes, and then reconciles all caches.
Each StatefulSet rolls one pod at a time, as on every operator upgrade, with no throttling across caches.

**`image.tracer` is deprecated** and planned for removal in 1.2.
While set, it overrides the tracer of the `varnish` entries only, wins over an explicit `runtimes.varnish.versions."9.1.0".tracer`, and drops the release's tracer digest.
Move the value to `runtimes…tracer` and unset `image.tracer.repository` and `image.tracer.tag`.
Do this in a values file, or with the escaped `--set` form shown in {ref}`replace-an-image`.
The reuse flags keep the old value.
Upgrade once with `--reset-values -f <your full values file>`.
The chart prints a deprecation note while it is set.

**Use `--reset-then-reuse-values`.**
Plain `--reuse-values` fails from 1.0 to 1.1 with an error from the chart, because it drops the `runtimes` defaults.
From 1.1 onward it silently keeps the old catalog: no new digests, no security rebuilds, and an empty `version` stays where it was.

(upgrade-1-1-metric-labels)=

## Upgrading to 1.1: renamed metric labels

1.1 renames two labels of the operator's `vinyl_*` metrics (#155).
Before, they collided with the target labels Prometheus attaches to every series scraped from the operator pod.
Prometheus renamed them to `exported_namespace` and `exported_pod`, and `namespace` on every `vinyl_*` series was the operator's namespace instead of the cache's.

| Metric | Before 1.1 | As scraped before 1.1 | Since 1.1 |
|---|---|---|---|
| all `vinyl_*` with a cache's namespace | `namespace` | `exported_namespace` | `cache_namespace` |
| `vinyl_broadcast_total` | `pod` (an `ip:port`) | `exported_pod` | `pod_addr` |
| `vinyl_broadcast_total` | (none) | (none) | `cache`, `cache_namespace` (new) |

The alert rules from the chart and from the operator use the new labels.
Update your own dashboards, recording rules and alert routes.
Replace `exported_namespace` with `cache_namespace` and `exported_pod` with `pod_addr` on `vinyl_*` metrics.
The exporter's `varnish_*` metrics are unchanged.

(upgrade-1-1-default-route)=

## Upgrading to 1.1: default route through the first backend's director

Before 1.1, requests that no `vclRecv` snippet routed all went to the first pod of the first backend, whatever the director settings said (#173).
Since 1.1 they go through that backend's director (see {ref}`default-routing`).

- Traffic now spreads across the first backend's pods.
  `shard` (the default) keeps URL stickiness: each URL still lands on one pod.
  With `round_robin` or `random` you lose the accidental affinity to a single pod.
- `shard.healthy` and `hash.header` now take effect for this route.
  A `hash.header` must start with a letter and contain only letters, digits, `-` and `_` (what VCL accepts after `req.http.`).
  The CRD rejects other values on apply.
- If the first backend has no endpoints, requests without a snippet answer `503`.
  Before, they silently reached a pod of another backend.
- Caches whose snippets always set the backend hint are not affected.

(upgrade-crd)=

## Upgrade the CRD

CRDs in `charts/cloud-vinyl/crds/` are installed on `helm install` but are **not** updated automatically by `helm upgrade`.
This is intentional: Helm never deletes CRDs, and CRD schema changes require careful migration.

To update the CRD manually, use server-side apply:

```shell
kubectl apply --server-side --force-conflicts \
  -f https://raw.githubusercontent.com/bluedynamics/cloud-vinyl/main/config/crd/bases/vinyl.bluedynamics.eu_vinylcaches.yaml
```

Since 1.0 the CRD serves two API versions, `v1` and the deprecated `v1alpha1`, each with the full schema.
That makes it too large for a plain `kubectl apply`.
A plain apply stores the whole object in the `last-applied-configuration` annotation and fails with `metadata.annotations: Too long: may not be more than 262144 bytes`.
`--force-conflicts` takes over fields that an earlier client-side apply owned.

For GitOps workflows, manage the CRD separately and render or install the chart with `--skip-crds`.
The tool has to apply the CRD server-side as well, for example with the Argo CD sync option `ServerSideApply=true`.

(upgrade-rollback)=

## Rollback

```shell
helm rollback cloud-vinyl --namespace cloud-vinyl-system
```

After rollback, the operator downgrades automatically.
VinylCache clusters are not affected unless the CRD schema changed between versions.

```{warning}
Do not roll back from 1.1 to 1.0 while any cache lacks `spec.image`.
The 1.0 operator passes `spec.image` straight into the StatefulSet, and the API server rejects an empty image.
For such a cache the 1.0 operator fails on every reconcile.
Its Services, network policies, secrets, VCL pushes and status are not reconciled any more.
Existing pods keep running on the StatefulSet built by 1.1.
A cache created without image during the rollback gets no StatefulSet at all.
Such a cache also cannot be deleted: 1.0 writes `image: ""` on every update, the 1.1 CRD rejects it, and finalizer removal fails.
The cache stays in `Terminating`.

Before you roll back:

- list the daemon image of every cache:
  `kubectl get vinylcache -A -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name}{"\t"}{.status.runtime.daemon}{"\n"}{end}'`.
- set `spec.image` on every cache that has none.
- move every `flavor: vinyl` cache to `flavor: varnish` with an image of that flavor.
  1.0 only knows `varnishd`.
  A Vinyl image would crash-loop the first pod.

A cache that 1.0 updates successfully loses `spec.version` and has `spec.flavor` reset to `varnish`.
```
