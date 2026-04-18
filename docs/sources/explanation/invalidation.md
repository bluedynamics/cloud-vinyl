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

Bans invalidate sets of objects matching a VCL expression. Two routes are available — they differ in where the expression is validated and in how the ACL is enforced.

### Via the operator invalidation proxy (default, always available)

The proxy listens on port `8090` of the operator Service, validates the expression (only `obj.http.*` LHS; no wildcard-only), and forwards it to every pod via `vinyl-agent POST /ban` → `varnishadm ban <expr>`:

```
POST http://<operator-svc>:8090/ban
Content-Type: application/json

{"expression": "obj.http.X-Cache-Tag ~ \"product-42\""}
```

Equivalent as native BAN HTTP method with the `X-Ban-Expression` header:

```
BAN http://<operator-svc>:8090/
X-Ban-Expression: obj.http.X-Cache-Tag ~ "product-42"
```

No opt-in needed. Proxy-side ACL enforcement is currently advisory (see "ACLs" below).

### Directly to Varnish (BAN method on the traffic port)

From v0.4.2, VinylCache can expose a BAN handler directly in VCL, bypassing the operator proxy:

```yaml
spec:
  invalidation:
    ban:
      enabled: true
      allowedSources:
        - "10.0.0.0/8"
```

The generator then emits a `vinyl_ban_allowed` ACL (localhost + operator pod IP + `allowedSources`) plus a `vcl_recv` BAN handler that calls `std.ban(req.http.X-Vinyl-Ban)`:

```
BAN http://<cache-traffic-svc>:8080/
X-Vinyl-Ban: obj.http.x-url ~ "^/news/"
```

On malformed expressions the handler returns `400 Invalid ban expression: <std.ban_error()>` rather than silently succeeding. When the header is missing, `400 Missing X-Vinyl-Ban header`. Unauthorised source → `403 Forbidden`.

Use `obj.http.x-url` / `obj.http.x-host` instead of `req.*` so the ban lurker can compact bans (`req.*` bans accumulate O(n*m)). The operator emits `beresp.http.x-url` / `beresp.http.x-host` on every cached object when `ban.enabled: true` is set (or xkey is enabled).

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

## ACLs

There are two layers of access control, enforced at different points:

- **Proxy-side** (`spec.invalidation.purge.allowedSources`, `spec.invalidation.ban.allowedSources`): the operator's invalidation proxy checks the source CIDR before forwarding to pods. By default all sources are allowed — scope tightly in production.
- **Varnish-side** (new in v0.4.2): the generated VCL contains `vinyl_purge_allowed` (always) and `vinyl_ban_allowed` (when `ban.enabled: true`). Both include `127.0.0.1` and the operator pod IP automatically; `allowedSources` CIDRs from the spec are appended. This is the ACL enforced in `vcl_recv` against the BAN/PURGE method coming directly to the traffic port.

In the common deployment — clients send PURGE/BAN to the proxy, the proxy forwards to Varnish — the operator's pod IP is the only source Varnish ever sees, so the Varnish-side ACL is effectively a safety net. When direct-to-Varnish traffic is enabled, both layers apply.
