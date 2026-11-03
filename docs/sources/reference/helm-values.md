# Helm Values Reference

All configurable values for the `cloud-vinyl` Helm chart.

## Top-level

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `replicaCount` | integer | `1` | Number of operator replicas. Values >1 require `leaderElection.enabled: true`. |
| `nameOverride` | string | `""` | Override chart name component of resource names. |
| `fullnameOverride` | string | `""` | Override full resource name (replaces `<release>-<chart>`). |
| `imagePullSecrets` | list | `[]` | Image pull secrets for private registries. |

The VinylCache CRD is not controlled by a value. It ships in the chart's `crds/`
directory, which Helm installs with the chart but never upgrades or deletes. Pass
`--skip-crds` to manage the CRD yourself, and see
[Upgrade cloud-vinyl](../how-to/upgrade.md) for applying it. The former `installCRDs`
value never had an effect; it is still accepted, and ignored, until 2.0.

## image

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `image.operator.repository` | string | `ghcr.io/bluedynamics/cloud-vinyl-operator` | Operator image repository. |
| `image.operator.tag` | string | `""` | Operator image tag. Defaults to Chart `appVersion`. |
| `image.operator.pullPolicy` | string | `IfNotPresent` | Image pull policy. |
| `image.agent.repository` | string | `ghcr.io/bluedynamics/cloud-vinyl-agent` | vinyl-agent sidecar image repository. |
| `image.agent.tag` | string | `""` | Agent image tag. Defaults to Chart `appVersion`. |
| `image.agent.pullPolicy` | string | `IfNotPresent` | Image pull policy. |

## serviceAccount

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `serviceAccount.create` | boolean | `true` | Create a dedicated ServiceAccount. |
| `serviceAccount.annotations` | object | `{}` | Annotations for the ServiceAccount (e.g. IRSA). |
| `serviceAccount.name` | string | `""` | Override ServiceAccount name. |

## resources

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `resources.requests.cpu` | string | `100m` | CPU request for the operator container. |
| `resources.requests.memory` | string | `128Mi` | Memory request. |
| `resources.limits.cpu` | string | `500m` | CPU limit. |
| `resources.limits.memory` | string | `256Mi` | Memory limit. |

## leaderElection

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `leaderElection.enabled` | boolean | `true` | Enable leader election. Required when `replicaCount > 1`. |

## operatorFlags

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `operatorFlags.metricsAddr` | string | `:8080` | Metrics server bind address. |
| `operatorFlags.probeAddr` | string | `:8081` | Health probe bind address. |
| `operatorFlags.agentClientTimeout` | string | `30s` | HTTP timeout for vinyl-agent API calls, as a Go duration (`30s`, `1m30s`). Before 1.0.2 the value was ignored and the operator always used 30s. |
| `operatorFlags.watchNamespaces` | list of strings | `[]` | Namespaces the operator watches. Empty watches the whole cluster. VinylCaches in other namespaces are ignored, and the webhook still admits them. See {ref}`install-watch-namespaces`. |

## defaults

Defaults the operator applies to every VinylCache. See
[Spread cache pods across nodes](../how-to/spread-pods.md).

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `defaults.podAntiAffinity.type` | string | `preferred` | Pod anti-affinity between the pods of one VinylCache: `preferred`, `required` or `none`. |
| `defaults.podAntiAffinity.topologyKey` | string | `kubernetes.io/hostname` | Node label whose values are the domains to spread across. |
| `defaults.podAntiAffinity.weight` | integer | `100` | Weight of a preferred rule, 1 to 100. |

## webhook

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `webhook.certManager.enabled` | boolean | `true` | Manage TLS via cert-manager (recommended). |
| `webhook.tls.caCert` | string | `""` | Base64-encoded CA cert (used when `certManager.enabled: false`). |
| `webhook.tls.cert` | string | `""` | Base64-encoded TLS certificate. |
| `webhook.tls.key` | string | `""` | Base64-encoded TLS private key. |

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
| `monitoring.serviceMonitor.endpointConfig` | object | `{}` | Merged into the scrape endpoint as is, e.g. `relabelings`, `metricRelabelings`, or an `authorization` block that replaces the default service account token. |
| `monitoring.metrics.secure` | boolean | `true` | Serve the operator's `/metrics` over HTTPS with Kubernetes authentication and authorization. `false` serves plain HTTP without auth. The ServiceMonitor and the operator's RBAC follow the setting. |
| `monitoring.metrics.readerSubjects` | list | `[]` | RBAC subjects bound to the `<release>-metrics-reader` ClusterRole when secure, e.g. the Prometheus service account. |
| `monitoring.metrics.tls.certManager` | boolean | `false` | Issue the metrics serving certificate from the chart's cert-manager issuer; the ServiceMonitor then verifies it. |
| `monitoring.metrics.tls.secretName` | string | `""` | Use an existing `kubernetes.io/tls` Secret as the metrics serving certificate. |

## Security contexts

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `podSecurityContext.runAsNonRoot` | boolean | `true` | Enforce non-root pod. |
| `podSecurityContext.runAsUser` | integer | `65532` | UID for the operator process. |
| `podSecurityContext.fsGroup` | integer | `65532` | GID for volume mounts. |
| `securityContext.allowPrivilegeEscalation` | boolean | `false` | Prevent privilege escalation. |
| `securityContext.readOnlyRootFilesystem` | boolean | `true` | Mount root filesystem read-only. |
| `securityContext.capabilities.drop` | list | `["ALL"]` | Drop all Linux capabilities. |

## Scheduling

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `nodeSelector` | object | `{}` | Node selector for the operator pod. |
| `tolerations` | list | `[]` | Tolerations for the operator pod. |
| `affinity` | object | `{}` | Affinity rules for the operator pod. |
