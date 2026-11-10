# Helm values reference

All configurable values for the `cloud-vinyl` Helm chart.

(helm-values-top-level)=

## Top-level

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `replicaCount` | integer | `1` | Number of operator replicas. Values >1 require `leaderElection.enabled: true`. |
| `nameOverride` | string | `""` | Override chart name component of resource names. |
| `fullnameOverride` | string | `""` | Override full resource name (replaces `<release>-<chart>`). |
| `imagePullSecrets` | list | `[]` | Image pull secrets for private registries. |

The VinylCache CRD is not controlled by a value.
It ships in the chart's `crds/` directory, which Helm installs with the chart but never upgrades or deletes.
Pass `--skip-crds` to manage the CRD yourself, and see [Upgrade cloud-vinyl](../how-to/upgrade.md) for applying it.
The former `installCRDs` value never had an effect.
It is still accepted, and ignored, until 2.0.

(helm-values-image)=

## image

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `image.operator.repository` | string | `ghcr.io/bluedynamics/cloud-vinyl-operator` | Operator image repository. |
| `image.operator.tag` | string | `""` | Operator image tag. Defaults to Chart `appVersion`. |
| `image.operator.pullPolicy` | string | `IfNotPresent` | Image pull policy. |
| `image.agent.repository` | string | `ghcr.io/bluedynamics/cloud-vinyl-agent` | vinyl-agent sidecar image repository. |
| `image.agent.tag` | string | `""` | Agent image tag. Defaults to Chart `appVersion`. |
| `image.agent.pullPolicy` | string | `IfNotPresent` | Image pull policy. |
| `image.tracer.repository` | string | `""` | **Deprecated**, planned for removal in 1.2. Use `runtimes.<flavor>.versions."<version>".tracer`. While set, it overrides the tracer of the `varnish` entries only (the `vinyl` tracer is not affected), repository defaulting to `ghcr.io/bluedynamics/cloud-vinyl-tracer`. `helm install` and `helm upgrade` print a deprecation note. |
| `image.tracer.tag` | string | `""` | **Deprecated**, see above. Defaults to the chart `appVersion`. |

(helm-values-runtimes)=

## `runtimes`

The runtime catalog: the flavor and version combinations a VinylCache can run, each with its daemon, exporter and tracer image.
The chart renders it into a ConfigMap that the operator reads at startup.
An invalid catalog stops the operator with an error.
The chart refuses to render a missing or broken one.
A change restarts the operator, which then reconciles every cache.
Leave it alone in the normal case.
Each chart release ships entries that were tested together, pinned by digest.

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `runtimes.<flavor>.default` | string | `"9.1.0"` | Version used by caches of this flavor that leave `spec.version` empty. |
| `runtimes.<flavor>.versions."<version>".daemon` | string | see below | Daemon image. |
| `runtimes.<flavor>.versions."<version>".exporter` | string | see below | Exporter image. |
| `runtimes.<flavor>.versions."<version>".tracer` | string | `""` | Tracer image. Empty uses the tracer of this chart release: `ghcr.io/bluedynamics/cloud-vinyl-tracer:<appVersion>` for `varnish`, `...:<appVersion>-vinyl` for `vinyl`. Pinned by digest in the released chart. |

`<flavor>` is `varnish` or `vinyl`.
No other flavor exists.
Version keys are quoted strings.
The chart ships these entries:

| Flavor and version | `daemon` | `exporter` |
|---|---|---|
| `varnish` 9.1.0 | `docker.io/library/varnish:9.1.0@sha256:<digest>` | `ghcr.io/bluedynamics/varnish-exporter:1.8.3-varnish9.1.0@sha256:<digest>` |
| `vinyl` 9.1.0 | `ghcr.io/bluedynamics/vinyl-cache:9.1.0-r1@sha256:<digest>` | `ghcr.io/bluedynamics/varnish-exporter:1.8.3-vinyl9.1.0@sha256:<digest>` |

See the chart's `values.yaml` for the digests of the release you installed.

The chart schema accepts tags as well as digests.
CI and end-to-end runs point entries at images built in the same job, and you may need a tag in an emergency.
The release workflow refuses to package a chart whose catalog has an image without digest.

To override an entry on the command line, escape the dots of the version key and quote the backslashes:

```shell
--set 'runtimes.vinyl.versions.9\.1\.0.daemon=registry.example/vinyl-cache@sha256:<digest>'
```

See {ref}`replace-an-image`.

(helm-values-serviceaccount)=

## `serviceAccount`

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `serviceAccount.create` | boolean | `true` | Create a dedicated ServiceAccount. |
| `serviceAccount.annotations` | object | `{}` | Annotations for the ServiceAccount (for example IRSA). |
| `serviceAccount.name` | string | `""` | Override ServiceAccount name. |

(helm-values-resources)=

## resources

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `resources.requests.cpu` | string | `100m` | CPU request for the operator container. |
| `resources.requests.memory` | string | `128Mi` | Memory request. |
| `resources.limits.cpu` | string | `500m` | CPU limit. |
| `resources.limits.memory` | string | `256Mi` | Memory limit. |

(helm-values-leaderelection)=

## `leaderElection`

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `leaderElection.enabled` | boolean | `true` | Enable leader election. Required when `replicaCount > 1`. |

(helm-values-operatorflags)=

## `operatorFlags`

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `operatorFlags.metricsAddr` | string | `:8080` | Metrics server bind address. |
| `operatorFlags.probeAddr` | string | `:8081` | Health probe bind address. |
| `operatorFlags.agentClientTimeout` | string | `30s` | HTTP timeout for vinyl-agent API calls, as a Go duration (`30s`, `1m30s`). Before 1.0.2 the value was ignored and the operator always used 30s. |
| `operatorFlags.watchNamespaces` | list of strings | `[]` | Namespaces the operator watches. Empty watches the whole cluster. VinylCaches in other namespaces are ignored, and the webhook still admits them. See {ref}`install-watch-namespaces`. |

(helm-values-defaults)=

## defaults

Defaults the operator applies to every VinylCache.
See [Spread cache pods across nodes](../how-to/spread-pods.md).

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `defaults.podAntiAffinity.type` | string | `preferred` | Pod anti-affinity between the pods of one VinylCache: `preferred`, `required` or `none`. |
| `defaults.podAntiAffinity.topologyKey` | string | `kubernetes.io/hostname` | Node label whose values are the domains to spread across. |
| `defaults.podAntiAffinity.weight` | integer | `100` | Weight of a preferred rule, 1 to 100. |

(helm-values-webhook)=

## webhook

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `webhook.certManager.enabled` | boolean | `true` | Manage TLS via cert-manager (recommended). |
| `webhook.tls.caCert` | string | `""` | Base64-encoded CA cert (used when `certManager.enabled: false`). |
| `webhook.tls.cert` | string | `""` | Base64-encoded TLS certificate. |
| `webhook.tls.key` | string | `""` | Base64-encoded TLS private key. |

(helm-values-monitoring)=

## monitoring

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `monitoring.prometheusRules.enabled` | boolean | `false` | Deploy a `PrometheusRule` with 10 pre-defined alerts. |
| `monitoring.prometheusRules.lowHitRatio.threshold` | number | `0.5` | `VinylCacheLowHitRatio` fires when a pod's hit ratio stays below this value (above 0, at most 1). Lower it for sites with a lot of uncacheable traffic. |
| `monitoring.prometheusRules.lowHitRatio.for` | string | `15m` | How long the hit ratio has to stay below the threshold. Prometheus duration. |
| `monitoring.serviceMonitor.enabled` | boolean | `false` | Deploy a `ServiceMonitor` for Prometheus Operator. |
| `monitoring.serviceMonitor.interval` | string | `30s` | Prometheus scrape interval. Format: `[0-9]+(s\|m\|h)`. |
| `monitoring.serviceMonitor.scrapeTimeout` | string | `10s` | Prometheus scrape timeout. |
| `monitoring.serviceMonitor.additionalLabels` | object | `{}` | Extra labels on the ServiceMonitor (for label selectors). |
| `monitoring.serviceMonitor.tlsConfig` | object | `{}` | TLS settings for a secure scrape. Empty: verify against the chart's certificate with `monitoring.metrics.tls.certManager`, otherwise skip verification. |
| `monitoring.serviceMonitor.endpointConfig` | object | `{}` | Merged into the scrape endpoint as is, for example `relabelings`, `metricRelabelings`, or an `authorization` block that replaces the default service account token. |
| `monitoring.metrics.secure` | boolean | `true` | Serve the operator's `/metrics` over HTTPS with Kubernetes authentication and authorization. `false` serves plain HTTP without auth. The ServiceMonitor and the operator's RBAC follow the setting. |
| `monitoring.metrics.readerSubjects` | list | `[]` | RBAC subjects bound to the `<release>-metrics-reader` ClusterRole when secure, for example the Prometheus service account. |
| `monitoring.metrics.tls.certManager` | boolean | `false` | Issue the metrics serving certificate from the chart's cert-manager issuer; the ServiceMonitor then verifies it. |
| `monitoring.metrics.tls.secretName` | string | `""` | Use an existing `kubernetes.io/tls` Secret as the metrics serving certificate. |

(helm-values-security-contexts)=

## Security contexts

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `podSecurityContext.runAsNonRoot` | boolean | `true` | Enforce non-root pod. |
| `podSecurityContext.runAsUser` | integer | `65532` | UID for the operator process. |
| `podSecurityContext.fsGroup` | integer | `65532` | GID for volume mounts. |
| `securityContext.allowPrivilegeEscalation` | boolean | `false` | Prevent privilege escalation. |
| `securityContext.readOnlyRootFilesystem` | boolean | `true` | Mount root filesystem read-only. |
| `securityContext.capabilities.drop` | list | `["ALL"]` | Drop all Linux capabilities. |

(helm-values-scheduling)=

## Scheduling

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `nodeSelector` | object | `{}` | Node selector for the operator pod. |
| `tolerations` | list | `[]` | Tolerations for the operator pod. |
| `affinity` | object | `{}` | Affinity rules for the operator pod. |
