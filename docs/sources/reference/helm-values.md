# Helm Values Reference

All configurable values for the `cloud-vinyl` Helm chart.

## Top-level

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `replicaCount` | integer | `1` | Number of operator replicas. Values >1 require `leaderElection.enabled: true`. |
| `nameOverride` | string | `""` | Override chart name component of resource names. |
| `fullnameOverride` | string | `""` | Override full resource name (replaces `<release>-<chart>`). |
| `imagePullSecrets` | list | `[]` | Image pull secrets for private registries. |
| `installCRDs` | boolean | `true` | Install the VinylCache CRD. Set `false` for GitOps workflows. |

## image

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `image.operator.repository` | string | `ghcr.io/bluedynamics/cloud-vinyl-operator` | Operator image repository. |
| `image.operator.tag` | string | `""` | Operator image tag. Defaults to Chart `appVersion`. |
| `image.operator.pullPolicy` | string | `IfNotPresent` | Image pull policy. |
| `image.varnish.repository` | string | `ghcr.io/bluedynamics/cloud-vinyl-varnish` | Varnish image repository. |
| `image.varnish.tag` | string | `7.6` | Varnish image tag. Pin explicitly for production. |
| `image.varnish.pullPolicy` | string | `IfNotPresent` | Image pull policy. |

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
| `operatorFlags.agentClientTimeout` | string | `30s` | HTTP timeout for vinyl-agent API calls. |

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
| `monitoring.serviceMonitor.enabled` | boolean | `false` | Deploy a `ServiceMonitor` for Prometheus Operator. |
| `monitoring.serviceMonitor.interval` | string | `30s` | Prometheus scrape interval. Format: `[0-9]+(s\|m\|h)`. |
| `monitoring.serviceMonitor.scrapeTimeout` | string | `10s` | Prometheus scrape timeout. |
| `monitoring.serviceMonitor.additionalLabels` | object | `{}` | Extra labels on the ServiceMonitor (for label selectors). |

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
