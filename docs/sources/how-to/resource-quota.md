(resource-quota)=
# Run a cache in a namespace with a ResourceQuota

A `ResourceQuota` on CPU or memory admits a pod only when every container in it has requests and limits for the quota's resources.
A cache pod has up to four containers.

| Container | Resources from | Default |
|---|---|---|
| `varnish` | `spec.resources` | none |
| `vinyl-agent` | `spec.agent.resources` | Requests: `10m` CPU, `16Mi` memory. Limits: `100m` CPU, `64Mi` memory. |
| `vinyl-exporter`, with `spec.monitoring.exporter.enabled` | `spec.monitoring.exporter.resources` | Requests: `10m` CPU, `32Mi` memory. Limits: `200m` CPU, `128Mi` memory. |
| `vinyl-tracer`, with `spec.tracing.enabled` | `spec.tracing.resources` | none |

(resource-quota-set)=

## Set what has no default

Set `spec.resources` for the cache daemon, see {ref}`size-memory-budget` for the memory limit.
With tracing on, set `spec.tracing.resources` too.
The tracer gets no default, because its load follows the traffic of the cache.

```yaml
spec:
  resources:
    requests: {cpu: 100m, memory: 1Gi}
    limits: {cpu: "1", memory: 2Gi}
```

The agent and the exporter work with their defaults.
In production they stayed below 10Mi and 22Mi of memory and near 1m CPU.

(resource-quota-override)=

## Override a default

Set the sidecar's `resources` block.
The operator uses a set block unchanged and does not merge it with the default, so set every request and limit the quota asks for:

```yaml
spec:
  agent:
    resources:
      requests: {cpu: 10m, memory: 16Mi}
      limits: {cpu: 200m, memory: 128Mi}
```

A `LimitRange` in the namespace is the alternative: Kubernetes then fills in defaults for every container that sets none.
In Rancher this is the container default resource limit of a project or namespace.
