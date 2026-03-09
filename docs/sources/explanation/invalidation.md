# Cache Invalidation

cloud-vinyl supports three invalidation mechanisms, all broadcast to every pod in the cluster.

## PURGE (object-level)

A standard HTTP `PURGE` request removes a single cached object by URL.
Varnish must have `return(purge)` in `vcl_recv` for the PURGE method (generated automatically).

**Soft purge** (default, `spec.invalidation.purge.soft: true`) marks objects as stale instead
of removing them. Subsequent requests revalidate in the background (stale-while-revalidate).
This prevents cache stampedes on busy endpoints.

Send a purge via the operator proxy:

```
PURGE http://<operator-svc>:8090/path/to/resource
```

## BAN (expression-based)

Bans invalidate sets of objects matching a VCL expression.
The most common pattern is banning by URL prefix or response header:

```
POST http://<operator-svc>:8090/ban
Content-Type: application/json

{"expression": "obj.http.X-Cache-Tag ~ \"product-42\""}
```

The operator validates the expression before forwarding (only `obj.http.*` LHS is allowed;
wildcard-only expressions are rejected). The validated expression is forwarded to
`vinyl-agent POST /ban` on each pod, which issues `varnishadm ban <expr>`.

Alternatively, use the native BAN HTTP method with the `X-Ban-Expression` header:

```
BAN http://<operator-svc>:8090/
X-Ban-Expression: obj.http.X-Cache-Tag ~ "product-42"
```

## Xkey (surrogate-key)

Xkey (also called surrogate keys or cache tags) allows grouping objects under arbitrary string keys.
When the cached response includes an `Xkey: key1 key2` header, all objects with that key can be
purged with a single request.

```
POST http://<operator-svc>:8090/purge/xkey
Content-Type: application/json

{"keys": ["product-42", "category-home"]}
```

The operator issues one `PURGE` with `X-Xkey-Purge: <key>` per key to every pod.

Xkey requires the `vmod_xkey` Varnish module. Enable it in the spec:

```yaml
spec:
  invalidation:
    xkey:
      softPurge: true  # default
```

## ACL

The proxy enforces an allowlist of source CIDRs. By default all sources are allowed.
Configure allowed CIDRs in the VinylCache spec (see reference documentation).
