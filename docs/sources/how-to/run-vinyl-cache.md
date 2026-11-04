(run-vinyl-cache)=

# Run Vinyl Cache

cloud-vinyl runs two lineages of the cache daemon: Varnish Cache from the docker-library
`varnish` image (`varnishd`) and Vinyl Cache from the open source project, in an image
we publish (`vinyld`). A VinylCache picks one with `spec.flavor`. For the background see
{ref}`explanation-lineages`.

## Choose the flavor

Set `flavor` and, optionally, `version`. You do not write an image reference:

```yaml
apiVersion: vinyl.bluedynamics.eu/v1
kind: VinylCache
metadata:
  name: my-cache
spec:
  replicas: 2
  flavor: vinyl
  version: 9.1.0
  backends:
    - name: app
      serviceRef:
        name: app-service
```

The operator looks up `flavor` and `version` in its runtime catalog and uses the daemon,
exporter and tracer images listed there, all pinned by digest. See
{ref}`helm-values-runtimes` for the catalog.

- `flavor` is `varnish` (the default) or `vinyl`.
- `version` must be a version the catalog lists for the flavor. The admission webhook
  rejects any other value and names the versions it does know.
- An empty `version` means the catalog's default version for the flavor. It is resolved at
  reconcile time and not written back to the spec, so it follows the chart's default when
  you upgrade the operator. Pin `version` to stay on a release.

The release ships `varnish` 9.1.0 and `vinyl` 9.1.0, both as default.

## See what runs

```bash
kubectl get vinylcache my-cache
```

The columns `Flavor` and `Version` come from `status.runtime`, which records the resolved
flavor and version and the image references in use:

```bash
kubectl get vinylcache my-cache -o jsonpath='{.status.runtime}'
```

`status.runtime.exporter` and `status.runtime.tracer` are filled when
`spec.monitoring.exporter` and `spec.tracing` are enabled. The condition
`RuntimeResolved` is `True` when the catalog entry was found.

## Switch an existing cache

Every cache created before 1.1 has `spec.image: varnish:9.1.0`. That image overrides the
daemon, so changing only `flavor` would run `vinyld` on a Varnish image and crash-loop the
first pod. Switch the flavor and remove the image in one patch:

```bash
kubectl patch vinylcache my-cache --type merge \
  -p '{"spec":{"flavor":"vinyl","version":"9.1.0","image":null}}'
```

This is a rolling restart, like an image change: the pods restart one at a time and each
comes back with an empty cache. Plan it for a quiet period. During the roll, pods of both
lineages can run side by side. That is safe because the peers only talk HTTP to each
other. Each pod starts empty.

If you want to keep an image override, replace it with an image of the new lineage. See
{ref}`replace-an-image`.

## What the flavor changes

The flavor decides the daemon command, the exporter's statistics tool and the tracer's log
tool. Everything else is identical, including the working directory
`/var/lib/varnish/varnishd` and the generated VCL. See the table in
{ref}`reference-flavor`.

## When the catalog changes under a cache

If a catalog entry that a cache uses disappears, for example because someone edited the
chart values, the operator sets the condition `RuntimeResolved=False` on that cache and
leaves its StatefulSet as it is. The condition `Progressing` then has the reason
`RuntimeNotInCatalog`. Restore the entry or set a `flavor` and `version` the catalog has.
The cache keeps serving traffic in the meantime. Deleting the cache still works.

## Pin images by digest

A VinylCache that names only `flavor` and `version` gets digest-pinned sidecars. The
Vinyl daemon image has the immutable tag `ghcr.io/bluedynamics/vinyl-cache:9.1.0-r1`
and a moving tag `ghcr.io/bluedynamics/vinyl-cache:9.1.0` that points at the newest
rebuild. If you set `spec.image` yourself, prefer the `-rN` tag or a digest. See
{ref}`explanation-lineages` for why.
