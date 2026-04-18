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

Two routes are available, with different semantics. Pick based on where your client sits and how much control you want over the validation step.

### Route 1 — via the operator invalidation proxy (recommended for most callers)

The proxy accepts BAN as either a JSON POST or the native BAN HTTP method, validates the expression, and forwards to `varnishadm ban` on every pod via the agent:

```bash
# JSON form:
curl -X POST http://cloud-vinyl.cloud-vinyl-system.svc:8090/ban \
  -H "Content-Type: application/json" \
  -d '{"expression": "obj.http.X-Cache-Tag ~ \"product-42\""}'

# Native BAN HTTP method (equivalent):
curl -X BAN http://cloud-vinyl.cloud-vinyl-system.svc:8090/ \
  -H "X-Ban-Expression: obj.http.X-Cache-Tag ~ \"product-42\""
```

The proxy enforces: only `obj.http.*` expressions on the left-hand side; wildcard-only expressions (`.` / `.*`) are rejected. This validation is a defence-in-depth layer on top of Varnish's own ban-expression compiler.

Does **not** require any opt-in on the VinylCache spec.

### Route 2 — directly to Varnish (BAN method on the traffic port)

Available from v0.4.2 onwards. Requires opt-in on the VinylCache:

```yaml
spec:
  invalidation:
    ban:
      enabled: true
      allowedSources:
        - "10.0.0.0/8"   # CIDRs permitted to send BAN directly
```

Then send the BAN with the `X-Vinyl-Ban` header:

```bash
curl -X BAN http://my-cache-traffic.my-namespace.svc:8080/ \
  -H 'X-Vinyl-Ban: obj.http.x-url ~ "^/news/"'
```

Responses:
- `200 Banned` — expression compiled and registered.
- `400 Missing X-Vinyl-Ban header` — header absent.
- `400 Invalid ban expression: <compiler message>` — `std.ban()` rejected the expression; the compiler error is returned verbatim for diagnosis.
- `403 Forbidden` — client IP is not in `vinyl_ban_allowed` (the localhost + operator IP + `ban.allowedSources`).

Use `obj.http.x-url` / `obj.http.x-host` rather than `req.*` — only the former can be processed by the Varnish ban lurker, so `req.*` bans accumulate and degrade performance O(n*m). The operator emits these helper headers on every cached object when `ban.enabled: true` is set (or when xkey is enabled).

**Security**: any client inside `vinyl_ban_allowed` can invalidate the entire cache with an arbitrary expression. Scope `ban.allowedSources` tightly.

## Purge by xkey (surrogate key)

Requires `spec.invalidation.xkey` to be enabled in the `VinylCache`.

```bash
curl -X POST http://cloud-vinyl.cloud-vinyl-system.svc:8090/purge/xkey \
  -H "Content-Type: application/json" \
  -d '{"keys": ["product-42", "category-home"]}'
```

The operator issues one `PURGE` with `X-Xkey-Purge` per key to every pod.
