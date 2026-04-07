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

To update the CRD manually:

```bash
kubectl apply -f https://raw.githubusercontent.com/bluedynamics/cloud-vinyl/main/config/crd/bases/vinyl.bluedynamics.eu_vinylcaches.yaml
```

For GitOps workflows, manage the CRD separately and set `installCRDs: false` in Helm values.

## Rollback

```bash
helm rollback cloud-vinyl --namespace cloud-vinyl-system
```

After rollback, the operator downgrades automatically. VinylCache clusters are not affected
unless the CRD schema changed between versions.
