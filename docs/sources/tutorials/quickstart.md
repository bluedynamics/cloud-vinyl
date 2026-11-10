# Quickstart

This tutorial deploys a complete Vinyl Cache cluster in a local kind cluster in under 10 minutes.

(quickstart-prerequisites)=

## Prerequisites

- [kind](https://kind.sigs.k8s.io/) installed
- [kubectl](https://kubernetes.io/docs/tasks/tools/) installed
- [Helm](https://helm.sh/docs/intro/install/) ≥ 3.12 installed

(quickstart-create-kind-cluster)=

## 1. Create a kind cluster

```shell
kind create cluster --name vinyl-quickstart
```

(quickstart-install-cert-manager)=

## 2. Install cert-manager

```shell
helm repo add jetstack https://charts.jetstack.io && helm repo update
helm install cert-manager jetstack/cert-manager \
  --namespace cert-manager --create-namespace \
  --set installCRDs=true --wait
```

(quickstart-install-cloud-vinyl)=

## 3. Install cloud-vinyl

```shell
helm install cloud-vinyl oci://ghcr.io/bluedynamics/charts/cloud-vinyl \
  --namespace cloud-vinyl-system --create-namespace \
  --set webhook.certManager.enabled=true --wait
```

Check the operator is running:

```shell
kubectl get pods -n cloud-vinyl-system
```

The output looks like this:

```text
NAME                            READY   STATUS    RESTARTS   AGE
cloud-vinyl-7d8f9b6c4-xk9pq    1/1     Running   0          30s
```

(quickstart-deploy-test-backend)=

## 4. Deploy a test backend

```shell
kubectl create deployment nginx --image=nginx:alpine --port=80
kubectl expose deployment nginx --port=80 --name=nginx
```

(quickstart-create-vinylcache)=

## 5. Create a VinylCache

Save the following to `quickstart.yaml`:

```yaml
apiVersion: vinyl.bluedynamics.eu/v1
kind: VinylCache
metadata:
  name: quickstart
  namespace: default
spec:
  replicas: 2
  backends:
    - name: nginx
      serviceRef:
        name: nginx
      port: 80
```

Apply it:

```shell
kubectl apply -f quickstart.yaml
```

(quickstart-watch-cluster)=

## 6. Watch the cluster come up

```shell
kubectl get vinylcache quickstart -w
```

The output looks like this:

```text
NAME         READY   REPLICAS   VCL    PHASE     AGE
quickstart   True    2/2        True   Ready     45s
```

(quickstart-send-request)=

## 7. Send a request through Vinyl Cache

Port-forward the Vinyl Cache traffic service:

```shell
kubectl port-forward svc/quickstart-traffic 8080:8080 &
```

The first request is a cache miss:

```console
$ curl -v http://localhost:8080/
< X-Cache: MISS
```

The second request is a cache hit:

```console
$ curl -v http://localhost:8080/
< X-Cache: HIT
```

(quickstart-purge-cache)=

## 8. Purge the cache

Port-forward the operator proxy:

```shell
kubectl port-forward svc/cloud-vinyl 8090:8090 -n cloud-vinyl-system &
```

Send the purge:

```console
$ curl -X PURGE http://localhost:8090/
{"status":"ok","total":2,"succeeded":2}
```

(quickstart-clean-up)=

## Clean up

```shell
kubectl delete vinylcache quickstart
kind delete cluster --name vinyl-quickstart
```
