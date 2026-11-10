# Cache invalidation

cloud-vinyl supports three invalidation mechanisms, all broadcast to every pod in the cluster.

(invalidation-purge)=

## PURGE (object-level)

A standard HTTP `PURGE` request removes a single cached object by URL.
The cache must have `return(purge)` in `vcl_recv` for the PURGE method (generated automatically).

**Soft purge** (default, `spec.invalidation.purge.soft: true`) marks objects as stale instead of removing them.
Subsequent requests revalidate in the background (stale-while-revalidate).
This prevents cache stampedes on busy endpoints.

Send a purge via the operator proxy:

```text
PURGE http://<operator-svc>:8090/path/to/resource
```

(invalidation-ban)=

## BAN (expression-based)

Bans invalidate sets of objects matching a VCL expression.
Two routes are available.
They differ in where the expression is validated and in how the ACL is enforced.

### Via the operator invalidation proxy (default, always available)

The proxy listens on port `8090` of the operator Service.
It validates the expression (only `obj.http.*` on the left-hand side, no wildcard-only expressions).
It then forwards the expression to every pod via `vinyl-agent POST /ban`, which runs `varnishadm ban <expr>`:

```text
POST http://<operator-svc>:8090/ban
Content-Type: application/json

{"expression": "obj.http.X-Cache-Tag ~ \"product-42\""}
```

The native BAN HTTP method with the `X-Ban-Expression` header is equivalent:

```text
BAN http://<operator-svc>:8090/
X-Ban-Expression: obj.http.X-Cache-Tag ~ "product-42"
```

No opt-in is needed.
Proxy-side ACL enforcement is currently advisory (see "ACLs" below).

### Directly to the cache (BAN method on the traffic port)

From v0.4.2, VinylCache can expose a BAN handler directly in VCL, bypassing the operator proxy:

```yaml
spec:
  invalidation:
    ban:
      enabled: true
      allowedSources:
        - "10.0.0.0/8"
```

The generator then emits a `vinyl_ban_allowed` ACL (localhost + operator pod IP + `allowedSources`).
It also emits a `vcl_recv` BAN handler that calls `std.ban(req.http.X-Vinyl-Ban)`:

```text
BAN http://<cache-traffic-svc>:8080/
X-Vinyl-Ban: obj.http.x-url ~ "^/news/"
```

On malformed expressions the handler returns `400 Invalid ban expression: <std.ban_error()>` and never silently succeeds.
When the header is missing, it returns `400 Missing X-Vinyl-Ban header`.
An unauthorised source gets `403 Forbidden`.

Use `obj.http.x-url` and `obj.http.x-host` instead of `req.*` so the ban lurker can compact bans.
Bans on `req.*` accumulate in `O(n*m)`.
The operator emits `beresp.http.x-url` and `beresp.http.x-host` on every cached object when `ban.enabled: true` is set (or xkey is enabled).

(invalidation-xkey)=

## Xkey (surrogate-key)

Xkey (also called surrogate keys or cache tags) allows grouping objects under arbitrary string keys.
When the cached response includes an `Xkey: key1 key2` header, a single request can purge all objects with that key.

```text
POST http://<operator-svc>:8090/purge/xkey
Content-Type: application/json

{"keys": ["product-42", "category-home"]}
```

The operator issues one `PURGE` with `X-Xkey-Purge: <key>` per key to every pod.

Xkey requires the `vmod_xkey` module.
Enable it in the spec:

```yaml
spec:
  invalidation:
    xkey:
      enabled: true
```

(invalidation-acls)=

## ACLs

Two layers of access control apply, enforced at different points:

- **Proxy-side** (`spec.invalidation.purge.allowedSources`, `spec.invalidation.ban.allowedSources`): the operator's invalidation proxy checks the source CIDR before forwarding to pods.
  By default all sources are allowed.
  Scope them tightly in production.
- **Cache-side** (new in v0.4.2): the generated VCL contains `vinyl_purge_allowed` (always) and `vinyl_ban_allowed` (when `ban.enabled: true`).
  Both include `127.0.0.1` and the operator pod IP automatically.
  The `allowedSources` CIDRs from the spec are appended.
  This is the ACL enforced in `vcl_recv` against the BAN/PURGE method coming directly to the traffic port.

In the common deployment, clients send PURGE/BAN to the proxy and the proxy forwards to the cache.
The operator's pod IP is then the only source the cache ever sees.
The cache-side ACL is effectively a safety net.
When direct-to-cache traffic is enabled, both layers apply.
