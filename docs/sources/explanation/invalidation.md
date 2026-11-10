# Cache invalidation

cloud-vinyl offers three ways to remove cached content: {term}`PURGE`, {term}`BAN`, and {term}`xkey`.
All three reach every pod of a cache.
This page explains how they differ and where access control applies.
For the requests and settings, see {doc}`/how-to/purge-ban`.

(invalidation-purge)=

## PURGE (object-level)

A PURGE removes the cached object of one URL and site host.
The generated {term}`VCL` handles the PURGE method in `vcl_recv` with `return(purge)`.

By default a purge is soft (`spec.invalidation.purge.soft: true`).
A {term}`soft purge` marks the object as stale instead of removing it.
The next request still gets the stale object while the cache revalidates it in the background.
The origin then sees one refresh instead of a burst of misses.
This protects busy endpoints from a cache stampede right after a purge.

A purge sent to the operator's invalidation proxy fans out to every pod.
With clustering, only the pod that owns the URL holds the object.
Expect the other pods to purge nothing.

(invalidation-ban)=

## BAN (expression-based)

A ban invalidates every object that matches a VCL expression, such as all objects with a given cache tag.
Two routes lead to a ban.
They differ in the place that checks the expression and in the access control that applies.

The **invalidation proxy** route is always available.
The operator validates the expression before it reaches any pod.
It accepts only `obj.http.*` on the left-hand side and rejects wildcard-only expressions.
The agent on each pod then runs `varnishadm ban`.
A client never talks to the cache directly on this route.

The **direct** route, added in v0.4.2, sends the BAN method to the cache's traffic port.
The direct route is off by default and needs `spec.invalidation.ban.enabled`.
The cache validates the expression itself with `std.ban()` and answers `400` for a malformed one.
This route suits clients that already sit next to the cache and do not want the extra hop through the operator.
The cost is that the cache-side ACL becomes the only gate.

Both routes work best with expressions on `obj.http.*`.
The {term}`ban lurker` can test those against stored objects in the background and drop the ban afterwards.
The cache can test a ban on `req.*` only when a request arrives.
Such bans pile up, and every lookup checks every active ban against the object, in `O(n*m)`.
The operator stores `x-url` and `x-host` on every cached object when you enable bans or xkey.

(invalidation-xkey)=

## Xkey (surrogate keys)

Xkey groups objects under arbitrary string keys.
Other names for these keys are surrogate keys and cache tags.
The origin names the keys in an `Xkey` response header, such as `Xkey: product-42 category-home`.
One request can later invalidate every object that carries a key.
This matches how content management systems think: one change affects all pages that show the changed item.

Xkey needs the `vmod_xkey` module in the cache and `spec.invalidation.xkey.enabled`.
The operator turns one xkey request into one PURGE per key on every pod.

(invalidation-acls)=

## Access control

Two layers of access control apply, at different points.

The **proxy-side** ACL is `spec.invalidation.purge.allowedSources` and `spec.invalidation.ban.allowedSources`.
The invalidation proxy checks the source CIDR before it forwards anything to the pods.
By default it allows all sources.
Limit it to your known callers in production.

The **cache-side** ACL, new in v0.4.2, lives in the generated VCL.
`vinyl_purge_allowed` always exists, and `vinyl_ban_allowed` exists when you set `ban.enabled`.
Both contain `127.0.0.1` and the operator pod IP, followed by the `allowedSources` CIDRs from the spec.
`vcl_recv` checks them for PURGE and BAN requests that arrive on the traffic port.

In the common deployment, clients send PURGE and BAN to the proxy, and the proxy forwards them to the cache.
The operator pod IP is then the only source the cache ever sees.
The cache-side ACL acts as a safety net.
Once you enable the direct BAN route, both layers matter.
