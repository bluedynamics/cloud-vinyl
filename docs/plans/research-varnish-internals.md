# Varnish Cache Internals Research for cloud-vinyl

Research date: 2026-03-08

---

## 1. vmod_directors.shard() vs Always-Proxy-Pattern

### 1.1 How the Shard Director Works

The shard director implements **ring-based consistent hashing** (similar to Ketama). It builds a deterministic hashing ring by computing SHA256 hashes of `<ident>_<n>` for each backend, where `n` ranges from 1 to the `replicas` parameter (default 67). This creates virtual nodes on the ring. When a request arrives, the shard key is hashed and the nearest backend clockwise on the ring is selected.

**Key properties:**
- When backends are added or removed, only the keys that were mapped to the changed backend are reassigned -- all other mappings remain stable.
- Backend weights scale the number of replicas (virtual nodes), so a backend with weight 2.0 gets ~134 replicas vs. the default 67.
- Weights are capped; values >10 "probably do not make much sense."
- If no explicit `reconfigure()` is called, reconfiguration happens automatically at end of the current task (after `vcl_init{}` or when the current client/backend task finishes).

**Backend selection parameters:**
- `by` -- determines sharding key: HASH (uses varnish hash), URL (hashes req.url), KEY (explicit integer), BLOB (first 4 bytes of blob)
- `alt` -- selects fallback backends by index on the ring (useful for retries: `alt=req.restarts`)
- `healthy` -- CHOSEN (default, returns first healthy), IGNORE (bypasses health), ALL (checks health for alternatives too)
- `resolve` -- LAZY (returns director reference for layering) vs NOW (immediate resolution)

### 1.2 The "Always Proxy" / Self-Routing Pattern

The self-routing pattern eliminates the need for a dedicated load balancer. Each Varnish node in the cluster:

1. Receives a request (via DNS round-robin or any L4 balancer)
2. Computes the shard key (typically from `req.url`)
3. Checks if the selected backend is itself (`server.identity`)
4. If **yes**: sets backend to the actual origin and proceeds with normal caching
5. If **no**: `return(pass)` to forward the request to the owning Varnish node

The owning node then processes the request through its own caching logic and returns the response back through the originating node.

**VCL skeleton:**
```vcl
sub vcl_recv {
    set req.backend_hint = cluster.backend(req.url);
    set req.http.X-shard = req.backend_hint;

    if (req.http.X-shard == server.identity) {
        set req.backend_hint = content;  // actual origin
    } else {
        return(pass);  // forward to owning node
    }
}
```

**Alternative: Redirect pattern** -- instead of proxying, return HTTP 302 to redirect the client directly to the owning node. Saves intra-cluster bandwidth at the cost of an extra client round-trip. Better for large objects.

### 1.3 Trade-offs: Shard Director Clustering vs. Simple Proxy

| Aspect | Shard Director Cluster | Simple Proxy (every node caches independently) |
|--------|----------------------|-----------------------------------------------|
| **Effective cache size** | Sum of all nodes (each object stored once) | Size of a single node (objects duplicated) |
| **Cache hit ratio** | Much higher for large working sets | Lower, especially with many nodes |
| **Intra-cluster traffic** | Every non-local request traverses 2 nodes | None |
| **Complexity** | VCL routing logic, health checks, reconfigure | Minimal |
| **Latency on miss** | Extra hop for non-local requests | Direct to origin |
| **Failure resilience** | Must handle backend health; cache reshuffling on failure | Each node independent |
| **Rolling updates** | Consistent hashing minimizes reshuffling | No impact |
| **Load distribution** | Depends on key distribution; can be uneven | Naturally even |

**Important caveat from docs:** "distribution of requests depends on the number of requests per key and the uniformity of the distribution of key values. In short, while this technique may lead to much better efficiency overall, it may also lead to less good load balancing for specific cases."

**Scaling limit:** Varnish Software recommends evaluating multi-tier sharding when 7+ Varnish servers are in a single cluster, because intra-cluster traffic grows with cluster size.

### 1.4 Kubernetes-specific Considerations

**IBM Varnish Operator approach:**
- Uses Go templates to dynamically inject backend definitions from pod IPs
- Supports `onlyReady` backends (uses K8s readiness) or all scheduled pods (uses Varnish health probes)
- VCL reloads on backend changes preserve cache (no restart needed)
- Shard director requires `.reconfigure()` after adding backends

**Key K8s challenge:** You cannot easily build a sharded Varnish cluster because pods come and go dynamically. All Varnish pods need to know about each other (peer discovery) to build identical VCL. Solutions:
- Operator-managed VCL templating (IBM Varnish Operator)
- Headless Service + DNS for peer discovery
- Sidecar signaller approach (kube-httpcache, but has the chicken-and-egg problem)

**Istio approach** (documented by Kai Burjack): Using Istio's DestinationRule for consistent hash-based load balancing at the mesh level, achieving visible reduction in p80/p95 latencies.

### 1.5 Rampup and Warmup

**Rampup (slow-start for recovered backends):**
- When a backend has just become healthy and is within its rampup period, the director probabilistically returns the next alternative backend instead
- Probability is proportional to `elapsed_time / rampup_duration`
- Only applies when `alt==0`
- Default: disabled (duration=0). IBM Varnish Operator example uses `set_rampup(30s)`

**Warmup (probabilistic load spreading):**
- A fraction of requests for a key go to the next alternate backend, even when the primary is healthy
- `warmup=0.5` spreads load over two backends per key
- IBM Varnish Operator example uses `set_warmup(0.1)`
- Default: disabled (probability=0.0)

**Bug (fixed in 2018, issue #2823):** Warmup/rampup could return an unhealthy alternative backend even when the preferred backend was healthy. Fixed by ensuring only healthy backends are considered during warmup/rampup.

### 1.6 Shard Director vs. UDO (Enterprise)

The open-source shard director uses **ring-based consistent hashing** (Ketama-style), while Varnish Enterprise's UDO uses **Rendezvous Hashing** (Highest Random Weight). Both minimize redistribution on backend changes, but:

- Ring-based: O(log N) lookup, but O(N * replicas) memory for the ring
- Rendezvous: O(N) lookup per request (iterate all backends), but simpler and no ring structure

UDO additionally offers: dynamic backends via DNS, a 60-second cooloff before backend teardown, built-in self-routing without manual VCL, and `.self_is_next()` for primary node detection.

**libvmod-cluster** (open-source by UPLEX, the shard director authors) extends the shard director with self-routing cluster functionality, including `.set_real()` for dynamic backend switching and `deny`/`allow` for node exclusion.

---

## 2. ESI (Edge Side Includes) in Varnish

### 2.1 How ESI Processing Works

ESI is activated per-object in `vcl_backend_response`:
```vcl
set beresp.do_esi = true;
```

Varnish implements a **limited ESI subset**: only `<esi:include>`, `<esi:remove>`, and `<!--esi -->`. No `<esi:try>`, `<esi:choose>`, `<esi:when>`, etc.

**Processing flow:**
1. Backend delivers HTML with ESI tags
2. Varnish parses the response and identifies ESI includes
3. During delivery, Varnish fetches each included fragment as a sub-request
4. Fragments go through the full VCL pipeline (`vcl_recv` -> `vcl_hash` -> cache lookup -> etc.)
5. Results are stitched together on-the-fly during delivery

**Important:** `set beresp.do_esi = true` should be set only on the **parent** document. Do NOT set it on included fragments unless they also contain `<esi:include>` tags.

### 2.2 ESI and Caching Interaction

**Parent and fragments have independent cache policies.** Each fragment is a separate cache object with its own TTL, grace, and keep values.

```vcl
sub vcl_backend_response {
    if (bereq.url == "/page.html") {
        set beresp.do_esi = true;
        set beresp.ttl = 24h;      // parent cached for 24h
    } elseif (bereq.url == "/sidebar") {
        set beresp.ttl = 1m;       // fragment cached for 1 minute
    }
}
```

**Grace interaction with ESI:**
- Each fragment independently participates in grace mode
- A stale parent can be served from grace while a fresh child fragment is fetched, or vice versa
- There is **no staleness inheritance** -- parent and child expiry are completely independent
- If a parent is served from grace, its ESI include sub-requests still go through normal cache lookup (may hit fresh fragments or trigger their own grace logic)

**Uncacheable fragments:**
- Fragments can be made uncacheable (e.g., personalized content) by returning `pass` in `vcl_recv` for those URLs
- The parent remains cacheable; only the fragment is fetched on every request
- **Production pitfall:** Non-cacheable ESI fragments still consume a backend request per parent delivery, which can overwhelm backends under load

### 2.3 ESI Thread/Request Limits

**max_esi_depth** (default: 5) -- limits nesting depth (fragments including other fragments). Prevents infinite recursion.

**Thread consumption:**
- Standard (open-source) Varnish processes ESI includes **sequentially** within a single thread
- Each ESI include triggers a sub-request that must complete before the next one starts
- Deep nesting multiplies stack usage -- `thread_pool_stack` may need increasing (default 48KB, issue #2129 showed crashes at 4 levels of nesting with stack protector enabled; fix: increase to 64KB+ with 150% safety margin)

**Varnish Enterprise Parallel ESI:**
- Fetches all includes concurrently instead of sequentially
- `esi_limit` parameter (default: 10) limits concurrent includes per ESI level per delivery
- With default `max_esi_depth=5`, theoretical max is 50 simultaneous subrequests per delivery

**Thread pool exhaustion:**
- ESI subrequests consume worker threads
- Under H/2, session threads can monopolize the pool, causing deadlock (issue #2418)
- Varnish 6.2+ added a thread pool watchdog that panics the worker process if deadlock is detected
- **Mitigation:** size `thread_pool_max` generously; monitor `threads_limited` counter

### 2.4 Common ESI Production Pitfalls

1. **Non-cacheable ESI fragments can kill your backend.** If every page includes a personalized fragment that can't be cached, each page view generates a backend request for that fragment. With 200 concurrent users, that's 200 backend requests for the fragment alone. (Documented real-world incident: request queue accumulation caused Varnish to become unresponsive for 11+ minutes.)

2. **Compression ratio degradation.** Varnish compresses each ESI fragment separately and stitches compressed parts together. This prevents gzip back-references from spanning fragment boundaries, reducing compression ratio compared to compressing the full assembled page.

3. **Stack overflow on deep nesting.** With `-fstack-protector-strong` (common on modern distros), ESI depth > 4 can cause SIGSEGV if `thread_pool_stack` is at default 48KB.

4. **First-byte check.** Varnish checks if the first byte is `<`; if not, ESI processing is skipped silently. Override with `+esi_disable_xml_check`.

5. **VCL switching.** ESI sub-requests start in the **original** VCL, not the one switched to via `return(vcl(...))` in `vcl_recv`. This catches people who expect ESI fragments to use a different VCL.

6. **Fragment failure handling.** By default, fragment failures are silent (as if `onerror="continue"`). Enable `+esi_include_onerror` for stricter handling. But once headers are sent, Varnish can only close the connection (HTTP/1) or reset the stream (H/2) -- it cannot change the status code.

7. **`beresp.do_stream` interaction.** With streaming enabled (default), if a fragment fetch fails mid-delivery, the parent body may be truncated. With streaming disabled, the complete parent is buffered first, so fragment failures can be handled more gracefully but at the cost of latency.

8. **Range requests (206) are generally incompatible with ESI.**

9. **HTTPS ESI.** Varnish does not support ESI over HTTPS; it automatically downgrades to HTTP. Use `+esi_ignore_https` to suppress warnings.

---

## 3. xkey vmod (Surrogate Keys / Secondary Hash)

### 3.1 How xkey Works

xkey adds **secondary hash keys** to cached objects, enabling fast purging of all objects tagged with a specific key.

**Tagging:** Backend applications set the `xkey` (or `X-HashTwo`) response header with space/comma-separated keys:
```
HTTP/1.1 200 OK
Xkey: category_sports id_1265778 type_article
```

Multiple keys per object are supported. Multiple objects can share the same key.

**Purging:**
```vcl
sub vcl_recv {
    if (req.method == "PURGE") {
        if (!client.ip ~ purge) { return(synth(405)); }
        set req.http.x-purges = xkey.purge(req.http.x-xkey-purge);
        return(synth(200, req.http.x-purges + " objects purged"));
    }
}
```

**Purge request:**
```
PURGE / HTTP/1.1
Host: example.com
X-Xkey-Purge: category_sports
```

### 3.2 purge() vs softpurge()

- **xkey.purge(keys):** Removes all objects matching any of the given keys immediately. Returns count of purged objects.
- **xkey.softpurge(keys):** Resets TTL to 0 but **preserves grace and keep**. Objects remain available for grace-mode delivery and conditional revalidation. Returns count of soft-purged objects.

**softpurge + grace interaction:** After softpurge, the object is immediately "stale" (TTL=0). If grace is still active, the next request will:
1. Serve the stale object immediately
2. Trigger an async background fetch for a fresh version
This is the preferred approach for tag-based invalidation in production.

### 3.3 Performance: Critical Scalability Issues

**The xkey vmod is in maintenance mode with known, unfixed scalability issues.**

**Root cause -- mutex contention on the expiry data structure:**

xkey piggybacks on Varnish's internal expiry mechanism for thread-safe access to objects. While processing keys during object insertion or purging, xkey holds the expiry mutex, blocking **all** other expiry operations (including normal TTL-based expiry). On busy sites with high object insertion rates or frequent purges, this creates a global bottleneck.

> "This can really bring Varnish to its knees on busy sites where many new objects are inserted or a lot of purges happen."

**Specific scaling problems:**
- Every new object insertion incurs xkey locking overhead
- Large purge operations (touching thousands of objects) hold the mutex for extended periods
- On MSE (Massive Storage Engine) caches, startup with xkey requires indexing every object -- "one million disk operations need to take place" for a million-object cache
- Objects cached before `import xkey;` was loaded **cannot** be purged by xkey (relevant during VCL reloads)

**Practical limits:** Not formally documented, but the architecture fundamentally limits throughput proportionally to (insertion_rate + purge_rate) due to global mutex contention.

### 3.4 xkey and Grace Objects

Soft-purged objects retain their grace and keep values. This means:
- An object with 1h grace that is soft-purged will continue to be servable for up to 1h
- During that time, grace mode operates normally (async refresh on first stale hit)
- After grace expires, the keep period enables conditional revalidation (304 Not Modified)

**Gotcha:** If grace is already expired when softpurge is called, the object is effectively hard-purged (TTL=0, no grace remaining).

### 3.5 Alternative: ykey (Varnish Enterprise)

ykey is a core-integrated successor to xkey that solves the scalability issues:
- Proper mutex design that doesn't block the expiry mechanism
- Safe handling of purge operations spanning the entire cache
- MSE integration for persistent key data on disk (survives restarts)
- Built-in softpurge support

**There is no viable open-source alternative to xkey.** For cloud-vinyl, this means we must either:
1. Accept xkey's limitations and design around them (limit purge frequency, keep object counts reasonable)
2. Implement our own tag-tracking at the controller level (external to Varnish)
3. Use BAN expressions as an alternative (different trade-offs: O(bans * objects) CPU cost)

---

## 4. Soft-Purge in Varnish

### 4.1 How Soft-Purge Works

Soft-purge uses `vmod_purge` (built into Varnish since 5.2):

```vcl
purge.soft(DURATION ttl = 0, DURATION grace = -1, DURATION keep = -1)
```

**Parameters:**
- `ttl`: Set to 0 by default (marks object as expired)
- `grace`: -1 means "leave untouched" (preserve existing grace)
- `keep`: -1 means "leave untouched" (preserve existing keep)

**Key constraint:** Soft-purge can only **decrease** lifetimes, never extend them.

Setting all three to 0 is equivalent to a hard purge.

### 4.2 Soft-Purge vs Hard-Purge

| Aspect | Hard Purge | Soft Purge |
|--------|-----------|------------|
| Object removal | Immediate | Stays in cache |
| Grace serving | Not possible (object gone) | Yes, serves stale during grace |
| Client impact | Next request waits for backend | Next request gets stale immediately |
| Backend load | Thundering herd risk | Smooth, async refresh |
| Conditional revalidation | Not possible | Possible if keep > 0 |

### 4.3 Interaction with Grace Mode

**This is the critical selling point of soft-purge:**

1. `purge.soft(0s)` sets TTL=0, preserving grace and keep
2. The object immediately becomes "stale"
3. The next client request triggers:
   - **If grace active:** Stale object served immediately + async background fetch
   - **If grace expired but keep active:** Varnish sends conditional request (If-None-Match / If-Modified-Since) to backend synchronously
   - **If keep also expired:** Normal cache miss, full backend fetch

**Best practice pattern:**
```vcl
purge.soft(0s, 30s, 120s)
```
- TTL=0: expire immediately
- Grace=30s: serve stale for up to 30s (uses minimum of current and specified)
- Keep=120s: allow conditional revalidation for 2 minutes after grace

### 4.4 Common Gotchas

1. **Must be called in BOTH `vcl_hit` and `vcl_miss`** to handle all variants of an object. Missing `vcl_miss` means vary-variants may not get purged.

2. **Grace must already be set on the object.** If objects were cached with `beresp.grace = 0s`, soft-purge provides no benefit -- the object expires immediately with no stale serving.

3. **Can only decrease, never extend.** If an object has 5s grace remaining and you soft-purge with grace=30s, the grace stays at 5s.

4. **Async refresh only on first request.** Only the first request after soft-purge triggers the backend fetch. All subsequent requests within grace serve the same stale object. If the backend fetch fails, the stale object continues to be served until grace expires.

5. **Multi-tier caching pitfall.** If Varnish sits behind another CDN/cache layer, the upstream cache may re-cache the stale content served during grace as if it were fresh. This creates a "stale loop" where invalidation doesn't propagate.

6. **No purge confirmation for soft-purge.** Unlike hard purge where the object is definitely gone, soft-purge only adjusts timers. If grace was already expired, the soft-purge effectively becomes a hard purge -- but silently.

7. **`purge.soft()` returns the number of affected objects** -- always check this for monitoring/logging.

---

## 5. Shard Director Specifics

### 5.1 Consistent Hashing Ring Implementation

**Ring construction:**
1. For each backend, compute SHA256(`<ident>_<n>`) for n = 1..replicas (default 67)
2. Take the last 32 bits of each SHA256 hash
3. Place these on a circular uint32 ring
4. Total ring entries = num_backends * replicas (with weight scaling)

**Lookup:**
1. Compute key from request (URL hash, explicit key, etc.)
2. Find smallest hash value on ring >= key (clockwise search, wrapping)
3. Return that backend

**Stability guarantee:** When a backend is added/removed, only keys between the affected backend's ring positions and the next backend's positions are remapped. All other key-to-backend mappings remain stable.

### 5.2 Reconfiguration Behavior

**Adding/removing backends:**
```vcl
shard.add_backend(backend, [ident], [rampup])
shard.remove_backend([backend], [ident])
shard.reconfigure(replicas=67)
```

**Key behaviors:**
- `reconfigure()` rebuilds the entire ring. This is an atomic operation.
- If `reconfigure()` is not called explicitly, it happens automatically at end of current task.
- Multiple shard directors can be reconfigured, but changes to each director are only supported one at a time (serialize reconfigure calls).
- Backend changes without `reconfigure()` are safe -- they accumulate and apply together.

**In Kubernetes context:** When a pod is added/removed, the VCL needs to be regenerated with the new backend list and reloaded. The shard director's `reconfigure()` is called in the new `vcl_init`, which rebuilds the ring with the updated backends. The consistent hashing ensures minimal key remapping.

### 5.3 Health Checks with Shard Director

**Backend health probes** are the standard Varnish mechanism:
```vcl
backend node1 {
    .host = "10.0.0.1";
    .probe = {
        .request = "HEAD /healthz HTTP/1.1" "Host: varnish" "Connection: close";
        .interval = 5s;
        .timeout = 1s;
        .window = 5;
        .threshold = 3;
    }
}
```

**When a backend is sick:**
1. The ring lookup finds the preferred backend
2. If unhealthy, it searches clockwise for the next healthy backend
3. This continues until a healthy backend is found or all have been checked
4. The `healthy` parameter controls this behavior (CHOSEN/IGNORE/ALL)

**Implication:** When a backend goes sick, its traffic shifts to the next backend(s) on the ring. When it recovers, traffic shifts back. This is **less stable than stateful session persistence** but doesn't require any shared state.

### 5.4 Rolling Updates / Backend Churn

**What happens during a rolling update (Kubernetes context):**

1. Old pod starts terminating, new pod starts
2. VCL is regenerated and reloaded with updated backend list
3. `reconfigure()` rebuilds the ring
4. Keys mapped to the old pod are redistributed (minimal, ~1/N of total keys)
5. Those keys will experience cache misses on their new backend
6. Rampup feature helps: the new backend gets traffic gradually

**Rampup during rolling updates:**
- `set_rampup(30s)` -- newly healthy backends receive increasing traffic over 30 seconds
- During rampup, traffic is probabilistically sent to the next alternative backend
- This gives the new backend time to warm its cache before receiving full load

**Warmup for load spreading:**
- `set_warmup(0.1)` -- 10% of requests for each key go to the next alternative backend
- This pre-populates the alternative backend's cache, reducing impact if the primary fails
- `warmup=0.5` spreads load 50/50 across two backends per key

**Backend churn risk:** Frequent backend changes (e.g., aggressive HPA scaling) cause repeated ring reconfiguration. Each reconfiguration redistributes ~1/N of keys, causing cache misses. **Debouncing backend changes** is critical -- accumulate changes over a short window before triggering VCL reload.

**Stability vs. persistence trade-off:** The shard director switches back to the preferred backend when it recovers. This means a flapping backend causes repeated cache invalidation for its key range. Health check tuning (window/threshold) is essential to prevent flapping.

### 5.5 Monitoring

Monitor shard director issues via VSL:
```
varnishlog -I Error:^vmod_directors.shard
```

This catches errors from shard director operations (failed reconfigurations, no healthy backends, etc.).

---

## Summary: Implications for cloud-vinyl Architecture

### Shard Director Decision
The open-source shard director is production-ready and well-documented. For cloud-vinyl:
- Use ring-based consistent hashing (built-in) rather than requiring Enterprise features
- Implement rampup (30s suggested) and warmup (0.1 suggested) by default
- **Critical:** implement backend change debouncing to prevent ring reconfiguration churn
- Peer discovery via headless Service + DNS or operator-managed VCL templates
- Consider libvmod-cluster for more sophisticated self-routing

### ESI Considerations
- ESI is a Varnish-native feature with no special vmod requirements
- Main operational concern: non-cacheable fragments causing backend amplification
- Thread pool sizing must account for ESI depth and concurrent connections
- Parallel ESI is Enterprise-only; standard Varnish processes sequentially

### Cache Invalidation Strategy
- **xkey has known scalability problems** -- acceptable for moderate object counts and purge frequencies
- For cloud-vinyl, implement both xkey (tag-based) and direct purge (URL-based)
- **Always use soft-purge** (xkey.softpurge + purge.soft) for grace-mode compatibility
- Design for eventual consistency: soft-purge + grace provides "good enough" freshness without thundering herd
- Consider BAN as fallback for pattern-based invalidation (different perf characteristics)
- External tag tracking at the controller level could supplement/replace xkey for large deployments

### Grace Mode Design
- Set meaningful grace on all objects (e.g., 24h for unhealthy backends, 10s effective for healthy)
- Use req.grace to dynamically restrict grace based on backend health
- Always set keep > 0 to enable conditional revalidation after grace expires
- Soft-purge relies on grace being configured -- ensure it's part of the default VCL

---

## Sources

### Shard Director & Clustering
- [Varnish Directors Module (trunk docs)](https://vinyl-cache.org/docs/trunk/reference/vmod_directors.html)
- [Creating a self-routing Varnish cluster](https://info.varnish-software.com/blog/creating-self-routing-varnish-cluster)
- [Self-routing cluster VCL example (GitHub Gist)](https://gist.github.com/rezan/1eadaef1745286a4e7262d83e1eff19c)
- [IBM Varnish Operator - VCL Configuration](https://ibm.github.io/varnish-operator/vcl-configuration.html)
- [libvmod-cluster (UPLEX)](https://code.uplex.de/uplex-varnish/libvmod-cluster)
- [Varnish Sharding with Istio in Kubernetes (Medium)](https://medium.com/hamburger-berater-team/varnish-sharding-with-istio-in-kubernetes-402f313919aa)
- [Varnish in Kubernetes (kruyt.org)](https://kruyt.org/varnish-kuberenets/)
- [Shard director may return unhealthy backend - Issue #2823](https://github.com/varnishcache/varnish-cache/issues/2823)
- [Shard director rampup discussion (mailing list)](https://www.mail-archive.com/varnish-misc@varnish-cache.org/msg08281.html)
- [UDO vmod (Varnish Enterprise)](https://docs.varnish-software.com/varnish-enterprise/vmods/udo/)

### ESI
- [ESI (trunk docs)](https://vinyl-cache.org/docs/trunk/users-guide/esi.html)
- [Troubleshooting Varnish ESI high load spikes](https://www.claudiokuenzler.com/blog/880/troubleshooting-solving-high-load-spikes-timeouts-varnish-backend-with-esi)
- [Stack overflow with >4 level ESI - Issue #2129](https://github.com/varnishcache/varnish-cache/issues/2129)
- [H/2 thread starvation deadlock - Issue #2418](https://github.com/varnishcache/varnish-cache/issues/2418)
- [Parallel ESI (Varnish Enterprise)](https://docs.varnish-software.com/varnish-enterprise/features/pesi/)
- [Controlling the Cache with ESI (Smashing Magazine)](https://www.smashingmagazine.com/2015/02/using-edge-side-includes-in-varnish/)

### xkey / Surrogate Keys
- [xkey vmod source (GitHub)](https://github.com/varnish/varnish-modules/blob/master/src/vmod_xkey.vcc)
- [Secondary keys documentation (Varnish Software)](https://docs.varnish-software.com/book/invalidation/secondary-keys/)
- [xkey Debian manpage](https://manpages.debian.org/testing/varnish-modules/vmod_xkey.3)
- [ykey (Varnish Enterprise)](https://docs.varnish-software.com/varnish-enterprise/vmods/ykey/)

### Soft-Purge & Cache Invalidation
- [vmod_purge (trunk docs)](https://vinyl-cache.org/docs/trunk/reference/vmod_purge.html)
- [Cache Invalidation in Varnish (Varnish Software blog)](https://info.varnish-software.com/blog/cache-invalidation-in-varnish)
- [Grace mode and keep (trunk docs)](https://vinyl-cache.org/docs/trunk/users-guide/vcl-grace.html)
- [Object lifetime tutorial (Varnish Developer Portal)](https://www.varnish-software.com/developers/tutorials/object-lifetime/)
- [Grace mode in Varnish 5.2 (GetPageSpeed)](https://www.getpagespeed.com/server-setup/varnish/varnish-5-2-grace-mode)
- [Purge (Varnish Enterprise)](https://docs.varnish-software.com/varnish-enterprise/vmods/purge/)
