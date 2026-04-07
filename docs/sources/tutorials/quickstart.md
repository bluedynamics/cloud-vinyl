# Quickstart

This tutorial deploys a complete Vinyl Cache cluster in a local kind cluster in under 10 minutes.

## Prerequisites

- [kind](https://kind.sigs.k8s.io/) installed
- [kubectl](https://kubernetes.io/docs/tasks/tools/) installed
- [Helm](https://helm.sh/docs/intro/install/) ≥ 3.12 installed

## 1. Create a kind cluster

```bash
kind create cluster --name vinyl-quickstart
```

## 2. Install cert-manager

```bash
helm repo add jetstack https://charts.jetstack.io && helm repo update
helm install cert-manager jetstack/cert-manager \
  --namespace cert-manager --create-namespace \
  --set installCRDs=true --wait
```

## 3. Install cloud-vinyl

```bash
helm install cloud-vinyl oci://ghcr.io/bluedynamics/charts/cloud-vinyl \
  --namespace cloud-vinyl-system --create-namespace \
  --set webhook.certManager.enabled=true --wait
```

Check the operator is running:

```bash
kubectl get pods -n cloud-vinyl-system
# NAME                            READY   STATUS    RESTARTS   AGE
# cloud-vinyl-7d8f9b6c4-xk9pq    1/1     Running   0          30s
```

## 4. Deploy a test backend

```bash
kubectl create deployment nginx --image=nginx:alpine --port=80
kubectl expose deployment nginx --port=80 --name=nginx
```

## 5. Create a VinylCache

Save the following to `quickstart.yaml`:

```yaml
apiVersion: vinyl.bluedynamics.eu/v1alpha1
kind: VinylCache
metadata:
  name: quickstart
  namespace: default
spec:
  replicas: 2
  backends:
    - name: nginx
      host: nginx.default.svc.cluster.local
      port: 80
```

Apply it:

```bash
kubectl apply -f quickstart.yaml
```

## 6. Watch the cluster come up

```bash
kubectl get vinylcache quickstart -w
# NAME         READY   REPLICAS   VCL    PHASE     AGE
# quickstart   True    2/2        True   Ready     45s
```

## 7. Send a request through Vinyl Cache

```bash
# Port-forward the Vinyl Cache traffic service
kubectl port-forward svc/quickstart-traffic 8080:8080 &

# First request: cache MISS
curl -v http://localhost:8080/
# < X-Cache: MISS

# Second request: cache HIT
curl -v http://localhost:8080/
# < X-Cache: HIT
```

## 8. Purge the cache

```bash
# Port-forward the operator proxy
kubectl port-forward svc/cloud-vinyl 8090:8090 -n cloud-vinyl-system &

curl -X PURGE http://localhost:8090/
# {"status":"ok","total":2,"succeeded":2}
```

## Clean up

```bash
kubectl delete vinylcache quickstart
kind delete cluster --name vinyl-quickstart
```
