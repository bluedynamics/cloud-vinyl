# Set up monitoring

This guide enables Prometheus monitoring for a `VinylCache`: operator metrics,
generated `ServiceMonitor`/`PrometheusRule` objects, and the Varnish exporter
sidecar that surfaces the cache hit rate.

## Prerequisites

- A running cloud-vinyl operator (see [install](install.md)).
- The [Prometheus Operator](https://prometheus-operator.dev/) installed in the
  cluster, providing the `monitoring.coreos.com` CRDs (`ServiceMonitor`,
  `PrometheusRule`). Without these CRDs the operator skips creating those objects
  (it logs and continues — it does not error).

## Enable monitoring on a VinylCache

Add a `monitoring` section to the spec:

```yaml
apiVersion: vinyl.bluedynamics.eu/v1alpha1
kind: VinylCache
metadata:
  name: my-cache
spec:
  # ... backends, replicas, etc.
  monitoring:
    serviceMonitor:
      enabled: true        # generate a ServiceMonitor for Prometheus to scrape
    prometheusRules:
      enabled: true        # generate the default alerting rules
    exporter:
      enabled: true        # add the prometheus_varnish_exporter sidecar
```

What each toggle does:

- **`serviceMonitor.enabled`** — the operator creates a `ServiceMonitor` (owned by
  the `VinylCache`, so it is garbage-collected with it) targeting the cache's
  `exporter` service port.
- **`prometheusRules.enabled`** — the operator creates a `PrometheusRule` with the
  default cloud-vinyl alerts (see [metrics reference](../reference/metrics.md)).
- **`exporter.enabled`** — adds the `prometheus_varnish_exporter` sidecar to each
  Varnish pod. It mounts the Varnish working directory read-only and exposes
  native `varnish_*` metrics (cache hit/miss, backend health) on port `9131`.

The exporter image and resources can be overridden:

```yaml
    exporter:
      enabled: true
      image:
        repository: ghcr.io/bluedynamics/varnish-exporter
        tag: "1.6.1"
      port: 9131
      resources:
        requests: {cpu: 50m, memory: 32Mi}
        limits: {cpu: 200m, memory: 64Mi}
```

```{note}
The exporter shells out to `varnishstat`, so the default image
`ghcr.io/bluedynamics/varnish-exporter:1.6.1` bundles a `varnishstat` built for
**Varnish 7.x** (matching the `varnish:7.6` cache image used by default). If you
run a different Varnish major version, override `exporter.image` with a matching
build — a mismatched `varnishstat` cannot read the VSM and the sidecar will not
produce metrics.
```

## Show the cache hit rate in Grafana

The hit ratio is computed from the exporter's raw counters, not from an operator
gauge. Use this PromQL for a hit-rate panel:

```promql
sum(rate(varnish_main_cache_hit[5m])) by (namespace)
  / (sum(rate(varnish_main_cache_hit[5m])) by (namespace)
     + sum(rate(varnish_main_cache_miss[5m])) by (namespace))
```

## Verify

```console
$ kubectl get servicemonitor,prometheusrule -l app.kubernetes.io/name=cloud-vinyl
$ kubectl get pod <cache-pod> -o jsonpath='{.spec.containers[*].name}'
varnish vinyl-agent vinyl-exporter
```

Operator metrics (`vinyl_*`) are always available on the operator's `/metrics`
endpoint regardless of these toggles — see the [metrics reference](../reference/metrics.md).
