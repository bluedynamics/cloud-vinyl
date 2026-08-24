# Install cloud-vinyl

This guide installs the cloud-vinyl operator using Helm.

## Prerequisites

- Kubernetes cluster ≥ 1.28
- `kubectl` configured with cluster-admin access
- Helm ≥ 3.12
- [cert-manager](https://cert-manager.io/) installed (for webhook TLS)

### Install cert-manager

```bash
helm repo add jetstack https://charts.jetstack.io
helm repo update
helm install cert-manager jetstack/cert-manager \
  --namespace cert-manager --create-namespace \
  --set installCRDs=true \
  --wait
```

## Install cloud-vinyl

```bash
helm install cloud-vinyl oci://ghcr.io/bluedynamics/charts/cloud-vinyl \
  --namespace cloud-vinyl-system --create-namespace \
  --set webhook.certManager.enabled=true \
  --wait --timeout 120s
```

Verify the operator is running:

```bash
kubectl get deployment -n cloud-vinyl-system cloud-vinyl
kubectl get crd vinylcaches.vinyl.bluedynamics.eu
```

## Without cert-manager (manual TLS)

Generate a self-signed certificate and key:

```bash
openssl req -x509 -newkey rsa:4096 -keyout webhook.key -out webhook.crt \
  -days 365 -nodes -subj "/CN=cloud-vinyl-webhook" \
  -addext "subjectAltName=DNS:cloud-vinyl-webhook.cloud-vinyl-system.svc"
```

Install with manual TLS:

```bash
helm install cloud-vinyl oci://ghcr.io/bluedynamics/charts/cloud-vinyl \
  --namespace cloud-vinyl-system --create-namespace \
  --set webhook.certManager.enabled=false \
  --set webhook.tls.cert="$(base64 -w0 webhook.crt)" \
  --set webhook.tls.key="$(base64 -w0 webhook.key)" \
  --set webhook.tls.caCert="$(base64 -w0 webhook.crt)" \
  --wait
```

## Enable monitoring

If you have Prometheus Operator installed:

```bash
helm upgrade cloud-vinyl ... \
  --set monitoring.prometheusRules.enabled=true \
  --set monitoring.serviceMonitor.enabled=true
```

(install-troubleshooting)=

## Troubleshoot a failed VCL push

The operator pushes the generated VCL to the `vinyl-agent` sidecar of every cache pod over HTTP on port 9090.
When that fails, the pods keep serving with the bootstrap VCL and the `VinylCache` reports phase `Error`:

```text
Message: VCL push failed on all 2 pods
Reason:  VCLPushFailed
```

Read the underlying error from the operator log:

```bash
kubectl -n cloud-vinyl-system logs deploy/cloud-vinyl | grep -i "VCL push failed"
```

A `connection refused` or `i/o timeout` against a pod IP on port 9090 means a NetworkPolicy blocks the operator.
The operator opens port 9090 for its own pod IP, which requires the `POD_IP` environment variable that the chart injects through the downward API.
If you deploy the operator without the chart, set it:

```yaml
env:
  - name: POD_IP
    valueFrom:
      fieldRef:
        fieldPath: status.podIP
```

Alternatively, label the namespace the operator runs in:

```bash
kubectl label namespace cloud-vinyl-system vinyl.bluedynamics.eu/operator-namespace=true
```

The reconciler retries every 30 seconds, so no restart is needed.

```{note}
Clusters differ in whether they enforce NetworkPolicies.
k3s enforces them by default through kube-router, as do Calico and Cilium.
A cluster that ignores NetworkPolicies never shows this failure, which is why it can appear only after you move a working setup to a new cluster.
```
