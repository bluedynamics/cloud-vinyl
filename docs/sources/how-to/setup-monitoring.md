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

## Scrape the operator's own metrics

The operator exports `vinyl_*` metrics: VCL pushes, invalidations, objects purged,
rejected invalidation hosts. The chart scrapes them with a `ServiceMonitor` when
`monitoring.serviceMonitor.enabled` is set.

By default the operator serves `/metrics` over HTTPS and checks every scrape: the
scraper presents a service account token, and that account needs permission to `get`
the `/metrics` URL. Bind the Prometheus service account to the chart's reader role:

```yaml
monitoring:
  serviceMonitor:
    enabled: true
    additionalLabels:
      release: kube-prometheus-stack   # match your Prometheus' serviceMonitorSelector
  metrics:
    readerSubjects:
      - kind: ServiceAccount
        name: kube-prometheus-stack-prometheus
        namespace: monitoring
```

The ServiceMonitor then scrapes `https` with Prometheus' own token. The operator
generates a self-signed certificate, so the scrape skips verification. To verify it,
set `monitoring.metrics.tls.certManager: true`: the chart issues the certificate
through cert-manager and the ServiceMonitor checks it against the issuer's CA. An
existing TLS Secret works too, through `monitoring.metrics.tls.secretName` and your
own `monitoring.serviceMonitor.tlsConfig`.

If network policy already limits who reaches the operator, plain HTTP without
authentication is simpler:

```yaml
monitoring:
  metrics:
    secure: false
```

The ServiceMonitor follows that setting. Extra scrape settings, such as
`metricRelabelings` or a different `authorization`, go into
`monitoring.serviceMonitor.endpointConfig`.

To check, query `up` for the operator's target: its `job` is the chart's Service
name, `cloud-vinyl` for a release of that name, and the value must be `1`. A `401` or
`403` on Prometheus' target page means the token or the reader binding is missing.

## Enable monitoring on a VinylCache

Add a `monitoring` section to the spec:

```yaml
apiVersion: vinyl.bluedynamics.eu/v1
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
        tag: "1.6.1-varnish9.1.0"
      port: 9131
      resources:
        requests: {cpu: 50m, memory: 32Mi}
        limits: {cpu: 200m, memory: 64Mi}
```

```{note}
The exporter shells out to `varnishstat`, so the default image
`ghcr.io/bluedynamics/varnish-exporter:1.6.1-varnish9.1.0` bundles the
`varnishstat` of **Varnish 9.1.0**, matching the `varnish:9.1.0` cache image used
by default. The tag names the bundled Varnish version. It also reads Varnish 8.0
caches (see [Varnish version support](../reference/varnish-versions.md)). For
other versions, override `exporter.image` with a matching build. A mismatched
`varnishstat` cannot read the VSM, and the sidecar then reports `varnish_up 0`
and exports no `varnish_*` metrics at all.
```

## Show the cache hit rate in Grafana

The hit ratio is computed from the exporter's raw counters, not from an operator
gauge. Use this PromQL for a hit-rate panel:

```promql
sum(rate(varnish_main_cache_hit[5m])) by (namespace)
  / (sum(rate(varnish_main_cache_hit[5m])) by (namespace)
     + sum(rate(varnish_main_cache_miss[5m])) by (namespace))
```

## Watch fetch retries

Varnish has no counter for retries made in VCL. `varnish_main_backend_retry`
counts something else (the internal retry on a reused connection) and stays
at 0. A fetch that is retried on another backend increments
`varnish_main_backend_fail` and `varnish_main_fetch_failed` even though the
client gets a normal response. So a rise in those counters without a matching
rise in 5xx responses at your ingress means the retries are absorbing backend
failures:

```promql
sum(rate(varnish_main_backend_fail[5m])) by (namespace, pod)
```

To see individual retries, run `varnishlog` in the varnish container:

```bash
kubectl exec <pod> -c varnish -- varnishlog -g request -q 'VCL_return eq "retry"'
```

## Verify

```console
$ kubectl get servicemonitor,prometheusrule -l app.kubernetes.io/name=cloud-vinyl
$ kubectl get pod <cache-pod> -o jsonpath='{.spec.containers[*].name}'
varnish vinyl-agent vinyl-exporter
```

Operator metrics (`vinyl_*`) are always available on the operator's `/metrics`
endpoint regardless of these toggles — see the [metrics reference](../reference/metrics.md).
