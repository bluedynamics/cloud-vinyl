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
helm install cloud-vinyl oci://ghcr.io/bluedynamics/cloud-vinyl-chart \
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
helm install cloud-vinyl oci://ghcr.io/bluedynamics/cloud-vinyl-chart \
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
