# Purge and BAN the cache

This guide shows how to send cache-invalidation requests to a running VinylCache cluster.

## Find the invalidation address

Every VinylCache gets a Service named `<cache>-invalidation` in its own namespace, on port
`8090`. The operator reports the address to use in the cache's status:

```bash
kubectl get vinylcache my-cache -n app -o jsonpath='{.status.invalidationURL}'
# http://my-cache-invalidation.app:8090
```

Send all requests in this guide to that address. The examples use a cache `my-cache` in
the namespace `app`.

```{important}
The host must include the namespace: `my-cache-invalidation.app` or the full
`my-cache-invalidation.app.svc.cluster.local`. One operator serves the caches of all
namespaces and tells them apart by the `Host` header alone, so the bare Service name
`my-cache-invalidation` is rejected with `404 unknown cache`, even from inside the
cache's own namespace. The error names the host to use, and the operator counts every
rejection in `vinyl_invalidation_unknown_host_total`.
```

## PURGE a URL

Varnish keys a cached page by its URL and its site host, e.g. `example.org`. The request
to the proxy carries the invalidation host instead, so the proxy has to be told the site
host to purge under. Name it in the `X-Vinyl-Purge-Host` header, comma-separated for
several hosts:

```bash
curl -X PURGE -H "X-Vinyl-Purge-Host: example.org" \
  http://my-cache-invalidation.app:8090/path/to/resource
```

Clients that cannot set headers, such as `plone.cachepurging`, rely on
`spec.invalidation.purge.hosts` instead. For a purge without the header, the proxy sends
one purge per listed host:

```yaml
spec:
  invalidation:
    purge:
      enabled: true
      hosts:
        - example.org
        - www.example.org
```

The header wins over the list. With neither, the proxy purges under the invalidation host
itself, which matches nothing that real traffic cached, and says so in a `warning` field
of the response and in the operator log.

Response:

```
{"status":"ok","total":3,"succeeded":3,"objectsPurged":1,"results":[...]}
```

With several hosts, every pod result carries the `host` it purged, and `total`,
`succeeded` and `objectsPurged` cover all of them.

`status` is one of:
- `ok` — all pods purged successfully.
- `partial` — some pods failed (cache is partially stale).
- `failed` — all pods failed.

`succeeded` counts pods that *answered*, not objects that were *removed* —
HTTP 200 from a pod means it ran the purge, not that anything was there to
purge. `objectsPurged` is the actual count, summed from every pod's
`X-Vinyl-Purged` response header (each pod's own count is on its entry in
`results`). Once cluster sharding routes a URL to a single owner pod, a
correct broadcast purge legitimately removes 0 objects on every other pod —
that is expected, not a failure, and `status` stays `ok`.

`objectsPurged` is **omitted** from the response — not `0` — when no pod
reported a parseable count (e.g. an older `varnishd`, or every pod's request
failed outright). Treat a missing field as "unknown", never as "confirmed
zero"; the two mean very different things.

## BAN by expression

Two routes are available, with different semantics. Pick based on where your client sits and how much control you want over the validation step.

### Route 1 — via the operator invalidation proxy (recommended for most callers)

The proxy accepts BAN as either a JSON POST or the native BAN HTTP method, validates the expression, and forwards to `varnishadm ban` on every pod via the agent:

```bash
# JSON form:
curl -X POST http://my-cache-invalidation.app:8090/ban \
  -H "Content-Type: application/json" \
  -d '{"expression": "obj.http.X-Cache-Tag ~ \"product-42\""}'

# Native BAN HTTP method (equivalent):
curl -X BAN http://my-cache-invalidation.app:8090/ \
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
curl -X POST http://my-cache-invalidation.app:8090/purge/xkey \
  -H "Content-Type: application/json" \
  -d '{"keys": ["product-42", "category-home"]}'
```

The operator issues one `PURGE` with `X-Xkey-Purge` per key to every pod.
