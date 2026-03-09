# Purge and BAN the cache

This guide shows how to send cache-invalidation requests to a running VinylCache cluster.

## Find the proxy endpoint

The operator exposes an invalidation proxy on port `8090` of the operator Service:

```bash
kubectl get svc -n cloud-vinyl-system cloud-vinyl
# NAME           TYPE        CLUSTER-IP    EXTERNAL-IP   PORT(S)
# cloud-vinyl    ClusterIP   10.0.0.1      <none>        8080/TCP,8090/TCP
```

For in-cluster requests use `http://cloud-vinyl.cloud-vinyl-system.svc:8090`.

## PURGE a URL

```bash
curl -X PURGE http://cloud-vinyl.cloud-vinyl-system.svc:8090/path/to/resource
```

Response:

```
{"status":"ok","total":3,"succeeded":3,"results":[...]}
```

`status` is one of:
- `ok` — all pods purged successfully.
- `partial` — some pods failed (cache is partially stale).
- `failed` — all pods failed.

## BAN by expression

```bash
curl -X POST http://cloud-vinyl.cloud-vinyl-system.svc:8090/ban \
  -H "Content-Type: application/json" \
  -d '{"expression": "obj.http.X-Cache-Tag ~ \"product-42\""}'
```

Only `obj.http.*` expressions on the left-hand side are accepted.
Wildcard-only expressions (`.` or `.*`) are rejected.

Alternatively, use the native BAN HTTP method:

```bash
curl -X BAN http://cloud-vinyl.cloud-vinyl-system.svc:8090/ \
  -H "X-Ban-Expression: obj.http.X-Cache-Tag ~ \"product-42\""
```

## Purge by xkey (surrogate key)

Requires `spec.invalidation.xkey` to be enabled in the `VinylCache`.

```bash
curl -X POST http://cloud-vinyl.cloud-vinyl-system.svc:8090/purge/xkey \
  -H "Content-Type: application/json" \
  -d '{"keys": ["product-42", "category-home"]}'
```

The operator issues one `PURGE` with `X-Xkey-Purge` per key to every pod.
