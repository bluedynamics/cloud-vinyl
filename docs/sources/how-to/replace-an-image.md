(replace-an-image)=

# Replace an image in an emergency

Normally new daemon versions and security rebuilds arrive with a chart release. If a fix
is needed before our next release, you can replace an image yourself, for one cache or for
the whole cluster. Both ways are an ordinary rolling restart.

## For one cache: `spec.image`

`spec.image` replaces the **daemon** image of the resolved catalog entry:

```yaml
spec:
  flavor: vinyl
  version: 9.1.0
  image: registry.example/vinyl-cache@sha256:...
```

The exporter and tracer still come from the catalog entry for `flavor` and `version`.
Always set `version` together with `image`: without it, the sidecars follow the catalog's
default version, which may not match the image you run, and the webhook warns about it.
It also warns when the last segment of the image repository names the other lineage, for
example a `varnish:*` image on a `vinyl` cache, which would fail to start with
"executable file not found". A reference without a digest gets no warning.

Remove `spec.image` once the fix is part of the catalog.

## For the whole cluster: the catalog in the chart values

Override one catalog entry. All caches on that flavor and version roll to the new image.
Dots in the version key must be escaped for `--set`, and the quotes keep the shell from
eating the backslashes:

```bash
helm upgrade cloud-vinyl oci://ghcr.io/bluedynamics/charts/cloud-vinyl \
  --namespace cloud-vinyl-system \
  --reset-then-reuse-values \
  --set 'runtimes.vinyl.versions.9\.1\.0.daemon=registry.example/vinyl-cache@sha256:...' \
  --wait --timeout 120s
```

The form `runtimes.vinyl.versions."9.1.0".daemon` does not work in Helm. In a values file
no escaping is needed:

```yaml
runtimes:
  vinyl:
    versions:
      "9.1.0":
        daemon: registry.example/vinyl-cache@sha256:...
```

The same works for `exporter` and `tracer`, and for the `varnish` flavor. A changed
catalog restarts the operator, which then reconciles every cache; each StatefulSet rolls
one pod at a time, with no throttling across caches.

## Remove the override

An override stays in your values after the fix is released. Remove it when you upgrade to
the release that contains the fix, or the cluster keeps running your image instead of the
tested one. `--reset-then-reuse-values` reuses the user-supplied values of the last
release, so removing a key from your values file is not enough, and `--set ...=null`
deletes the chart default, after which the chart fails. For that upgrade use
`--reset-values -f <your full values file>` without the override. Keep your overrides in
a values file under version control.

## Own builds and versions we do not ship

Add an entry under `runtimes.<flavor>.versions` for a version the chart does not list.
`default` stays as it is unless you want it to change. The entry needs `daemon` and
`exporter`; the chart fails without them. An empty `tracer` defaults to this release's
tracer, built for 9.1.0. It cannot read the log of another version, so tracing goes
silent: set `tracer` too if you use `spec.tracing`. A VinylCache can then select the
version with `spec.version`. See {ref}`helm-values-runtimes`.
