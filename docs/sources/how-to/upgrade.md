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
