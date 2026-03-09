# VCL Bugs, Production Gotchas, and Generator Requirements

Research for cloud-vinyl's structured VCL generator.
Date: 2026-03-08

---

## 1. Common VCL Bugs in Production

### 1.1 The Built-in VCL Trap

The most common class of bugs arises from misunderstanding how custom VCL interacts with the built-in VCL. Varnish **appends** its built-in VCL to every loaded VCL. If your subroutine does not explicitly `return()`, control falls through to the built-in version.

**Generator implication**: Every generated subroutine MUST end with an explicit `return()` action. Never rely on fall-through to built-in behavior.

The built-in `vcl_recv` does:
```vcl
if (req.http.Authorization || req.http.Cookie) {
    return (pass);
}
```
This silently bypasses cache for ALL requests with cookies (including harmless analytics cookies like `_ga`, `__utma`). Most production VCL needs to override this.

The built-in `vcl_backend_response` creates hit-for-miss objects (120s TTL, uncacheable) for:
- `beresp.ttl <= 0s`
- `beresp.http.Set-Cookie` present
- `Surrogate-control ~ "no-store"`
- `Cache-Control ~ "no-cache|no-store|private"` (when no Surrogate-Control)
- `Vary == "*"`

**Generator implication**: The generator must produce explicit cookie stripping and `Set-Cookie` handling rather than relying on defaults.

### 1.2 TTL=0 Causes Request Serialization

Setting `beresp.ttl = 0s` for uncacheable content creates a deadly interaction with request coalescing:
- Request coalescing (from `return(hash)`) means "only send one backend request at a time for similar requests"
- TTL=0 means "this response cannot be reused"
- Result: all requests serialize, one at a time, through the backend

**Correct pattern** (hit-for-miss):
```vcl
sub vcl_backend_response {
    if (beresp.http.cache-control ~ "private") {
        set beresp.uncacheable = true;
        set beresp.ttl = 120s;  // NOT 0s
    }
}
```

**Generator implication**: NEVER generate `beresp.ttl = 0s`. Always use `beresp.uncacheable = true` with a positive TTL for uncacheable content.

### 1.3 Setting Cache-Control in vcl_backend_response Does Not Affect TTL

The TTL is computed from backend response headers **before** `vcl_backend_response` executes. Setting `beresp.http.Cache-Control` in that subroutine has NO effect on `beresp.ttl`.

```vcl
// DOES NOT WORK - TTL already computed
set beresp.http.Cache-Control = "max-age=3600";

// WORKS - directly sets TTL
set beresp.ttl = 3600s;
```

**Generator implication**: Never generate Cache-Control header manipulation in `vcl_backend_response` as a caching mechanism. Always set `beresp.ttl` directly.

---

## 2. Specific Categories of VCL Bugs

### 2.1 Cache Poisoning via Vary Header

**The problem**: `Vary: User-Agent` creates thousands of cache variants (one sample of 100,000 requests yielded ~8,000 unique User-Agent strings). This effectively disables caching.

**The poisoning risk**: If Vary headers are not properly handled, an attacker can inject crafted header values that get cached and served to other users.

**Generator requirements**:
- MUST normalize Vary-targeted headers before they affect the cache key
- For `Vary: User-Agent`: normalize to a small set (e.g., "mobile", "desktop", "bot")
- For `Vary: Accept-Encoding`: normalize to "gzip" or unset
- For `Vary: Accept-Language`: use proper quality-factor parsing (not regex), e.g., via `vmod_accept`
- MUST strip `Vary: Cookie` (essentially uncacheable otherwise)
- MUST reject or warn on `Vary: *` (built-in marks as uncacheable, but generator should never produce it)
- SHOULD strip dangerous Vary values like `Vary: Host` from backend responses

### 2.2 Hash Collisions / Incorrect vcl_hash

The default hash uses `req.url` + `req.http.host` (or `server.ip` if no Host header).

**Common bugs**:
- **Case sensitivity**: Varnish does NOT lowercase hostname or URL. `Example.com/Path` and `example.com/path` are different cache entries. Browsers lowercase hostnames, but CDN-to-CDN requests may not.
- **Port in Host header**: `example.com` and `example.com:443` produce different hashes for the same content.
- **Protocol not in hash**: HTTP and HTTPS responses may differ but share a cache key by default.
- **Missing query string normalization**: `?a=1&b=2` and `?b=2&a=1` are different cache keys for the same content.

**Generator requirements**:
- Normalize Host header (lowercase, strip port)
- Use `std.querysort()` for URL normalization
- Include protocol in hash when HTTPS varies responses (via `X-Forwarded-Proto`)
- Strip trailing `?` from URLs with empty query strings

### 2.3 Grace Mode Misconfiguration

Grace mode serves stale content while fetching fresh content in the background. Misconfiguration leads to:
- **Serving stale forever**: Setting `beresp.grace` too high without monitoring
- **Not serving stale at all**: Not setting `beresp.grace` or setting it to 0
- **Live streaming broken**: `default_grace=10s` serves stale manifests, hiding new video chunks

**Version-specific gotcha**: In Varnish 3.x, `req.grace` existed. In 4.x+, it was replaced by `beresp.grace` and the `default_grace` parameter.

**Generator requirements**:
- Always generate explicit `beresp.grace` values
- Allow per-content-type grace configuration (shorter for dynamic, longer for static)
- Warn if grace is set but TTL is 0 (grace has no effect on uncacheable objects)

### 2.4 Backend/Director Misconfiguration

**Common bugs**:
- **Fallback director order matters**: Backends are tried in add-order. First healthy backend wins.
- **Sticky fallback gotcha**: With `sticky=true`, once a lower-priority backend is selected, it stays selected even when higher-priority ones recover.
- **Hash director without enough replicas**: Poor key distribution across backends.
- **Round-robin with `return(pipe)`**: Backend selection happens in `vcl_recv`; piped connections reuse the TCP connection for subsequent HTTP requests, which may route to the wrong backend.

**Generator requirements**:
- Generate health probes for ALL backends
- Validate director configuration (at least 2 backends for round-robin)
- Warn on pipe usage with directors

### 2.5 Return Action Bugs

Each subroutine has a specific set of valid return actions:

| Subroutine | Valid returns |
|---|---|
| `vcl_recv` | `hash`, `pass`, `pipe`, `synth(status, reason)`, `purge` |
| `vcl_hash` | `lookup` |
| `vcl_hit` | `deliver`, `miss`, `pass`, `restart`, `synth()` |
| `vcl_miss` | `fetch`, `pass`, `restart`, `synth()` |
| `vcl_pass` | `fetch`, `restart`, `synth()` |
| `vcl_deliver` | `deliver`, `restart`, `synth()` |
| `vcl_pipe` | `pipe` |
| `vcl_purge` | `synth()`, `restart` |
| `vcl_backend_fetch` | `fetch`, `abandon` |
| `vcl_backend_response` | `deliver`, `retry`, `abandon`, `pass(duration)` |
| `vcl_backend_error` | `deliver`, `retry`, `abandon` |
| `vcl_synth` | `deliver`, `restart` |

**Generator implication**: The generator MUST enforce return action validity at generation time. This is a compile-time check the VCL compiler already does, but catching it during generation gives better error messages and prevents deployment failures.

### 2.6 Header Manipulation Bugs

**The four object scopes and where they're available**:

| Object | Readable in | Writable in |
|---|---|---|
| `req` | client-side + backend | client-side only |
| `bereq` | `vcl_pipe`, backend subs | `vcl_pipe`, backend subs |
| `beresp` | `vcl_backend_response`, `vcl_backend_error` | same |
| `resp` | `vcl_deliver`, `vcl_synth` | same |
| `obj` | `vcl_hit`, `vcl_deliver` | NEVER (read-only) |

**Common bugs**:
- Trying to set `resp.http.*` in `vcl_recv` (resp not available there)
- Trying to modify `obj.*` (always read-only)
- Confusing `req` vs `bereq` (bereq is a filtered copy of req, missing per-hop headers like Connection, Range)
- Setting a header to empty string vs `unset`: empty string is **truthy**, unset is **falsy**

**Generator implication**: Enforce variable scope at generation time. The generator must know which variables are read/write in which subroutine.

### 2.7 Restart Loop Bugs

`return(restart)` increments `req.restarts`. When `req.restarts > max_restarts` (default: 4), Varnish returns `synth(503, "Too many restarts")`.

**Common bugs**:
- Unconditional restart without checking `req.restarts`
- Restart that doesn't fix the condition that triggered it (restarts with same state)
- Restart from `vcl_synth` creating a loop (synth -> restart -> error -> synth -> restart...)

**Generator requirements**:
- MUST always guard restart with `if (req.restarts < N)` check
- SHOULD generate restart reason tracking via `req.http.X-Restart-Reason`
- MUST prevent restart in `vcl_synth` unless explicitly intended with guard

### 2.8 Streaming vs Buffering Issues

`beresp.do_stream = true` enables streaming (sending response to client as it arrives from backend).

**Gotchas**:
- **ESI breaks streaming**: With `beresp.do_esi = true`, streaming may need to be disabled
- **Gzip breaks first-response streaming**: Varnish gzipping prevents streaming of the first response for an object
- **Memory allocation**: Without Content-Length, Varnish allocates storage in `fetch_chunksize` lumps and cannot reclaim surplus storage
- **Cannot trim surplus**: Streaming responses with no Content-Length waste memory per object

**Generator requirements**:
- Disable streaming when ESI is enabled
- Warn about streaming + gzip combination
- Generate appropriate `fetch_chunksize` recommendations

### 2.9 Workspace Overflow

Workspaces are fixed-size memory areas for request/response processing:
- `workspace_client`: default 96kB (was 64kB pre-7.0)
- `workspace_backend`: default 96kB (was 64kB pre-7.0)
- `workspace_session`: default 16kB (was 8kB pre-7.0)

**Overflow causes**:
- Too many headers (`http_max_hdr` default: 64)
- Very long header values
- Complex VCL string manipulation consuming workspace
- Large synthetic response bodies in `vcl_synth`

**Overflow behavior**:
- Client workspace overflow: 500 response (previously could panic)
- Backend workspace overflow: silently drops headers (appears as `LostHeader` in VSL)

**Generator requirements**:
- Warn when generating large synthetic bodies (recommend `std.fileread()` for templates)
- Recommend workspace sizes based on generated VCL complexity
- Generate header count limits where appropriate

### 2.10 Thread Pool Exhaustion

**Key parameters**:
- `thread_pool_min`: minimum threads per pool (default: 100)
- `thread_pool_max`: maximum threads per pool (default: 5000)
- `thread_pools`: number of pools (default: 2, recommended to stay at 2)

**Critical migration bug**: In Varnish 3.x, `thread_pool_add_delay` was in milliseconds. In 4.x+, it switched to **seconds**. Migrating `-p thread_pool_add_delay=1` from 3.x to 4.x+ means "create one thread per second" instead of "one per millisecond".

**Generator implication**: When generating varnishd startup parameters, validate unit semantics for the target Varnish version.

### 2.11 Ban Lurker Efficiency

The ban lurker is a background thread that proactively tests cached objects against ban expressions.

**Critical rule**: The ban lurker can ONLY process expressions using `obj.http.*` or `obj.status`. Expressions using `req.*` fields CANNOT be tested by the lurker.

**Non-lurker-friendly (bad)**:
```vcl
ban("req.url ~ /news/");
```

**Lurker-friendly (good)**:
```vcl
# In vcl_backend_response, copy URL to object header:
set beresp.http.x-url = bereq.url;

# In ban expression, use obj.http:
ban("obj.http.x-url ~ /news/");

# In vcl_deliver, strip the internal header:
unset resp.http.x-url;
```

**Performance**: Ban complexity is O(n*m) where n=objects, m=ban expressions. Non-lurker-friendly bans accumulate and degrade performance.

**Generator requirements**:
- Always generate lurker-friendly ban patterns
- Automatically copy `bereq.url` and `bereq.http.host` to `beresp.http.*` for ban support
- Strip internal ban-support headers in `vcl_deliver`

### 2.12 PIPE Mode Gotchas

**When pipe is appropriate**: WebSocket upgrades, non-HTTP protocols. Almost never for regular HTTP.

**Bugs**:
- **Backend selection frozen**: Backend chosen in `vcl_recv` applies to ALL subsequent requests on the piped connection, not just the first
- **No visibility**: No logging of backend responses, no header manipulation, no status code checking
- **Connection header conflict**: Varnish auto-adds `Connection: close`, but doesn't remove existing `Connection` headers, resulting in duplicate conflicting headers
- **No timeout control after pipe**: Only `pipe_timeout` applies; other timeouts are irrelevant

**Generator requirements**:
- SHOULD prefer `return(pass)` over `return(pipe)` for HTTP requests
- MUST only generate `return(pipe)` for WebSocket upgrades or explicit non-HTTP protocols
- MUST set `pipe_timeout` when generating pipe configurations

### 2.13 WebSocket Handling

WebSockets require pipe mode because Varnish doesn't natively handle the bidirectional framing.

**Correct pattern**:
```vcl
sub vcl_recv {
    if (req.http.Upgrade ~ "(?i)websocket") {
        return (pipe);
    }
}

sub vcl_pipe {
    if (req.http.Upgrade) {
        set bereq.http.Upgrade = req.http.Upgrade;
        set bereq.http.Connection = req.http.Connection;
    }
}
```

**Gotcha**: `pipe_timeout` must be set high enough for long-lived WebSocket connections.

---

## 3. Security-Relevant VCL Bugs

### 3.1 Cache Poisoning via Request Headers

**The bug**: Using custom headers for authorization decisions without verifying origin:
```vcl
// UNSAFE - client can forge this header
if (req.http.Paywall-State == "allow") {
    return (hash);
}
```

**Generator requirement**: Never trust client-supplied headers for authorization. Generate header stripping for all internal headers at the top of `vcl_recv`.

### 3.2 Httpoxy Vulnerability

The `Proxy` request header can be used to hijack backend HTTP connections.

**Generator requirement**: Always generate `unset req.http.Proxy;` in `vcl_recv`.

### 3.3 X-Forwarded-For Spoofing

Clients can forge `X-Forwarded-For`. The first trusted proxy in the chain should overwrite (not append to) XFF.

**Generator requirements**:
- Generate XFF handling that overwrites with `client.ip` rather than blindly appending
- When behind a trusted load balancer using PROXY protocol, use `client.ip` which reflects the real client

### 3.4 Set-Cookie Caching

Caching a response with `Set-Cookie` serves that cookie to ALL subsequent clients, creating session transfer vulnerabilities.

**Generator requirement**: Always strip `Set-Cookie` from cached responses, or use `return(pass)` for responses with Set-Cookie.

### 3.5 PURGE Authorization

PURGE requests must be restricted by ACL. Without restriction, anyone can evict cache content.

**Generator requirement**: Always generate PURGE ACL enforcement.

### 3.6 ACL DNS Resolution Trap

If an ACL entry references a hostname that cannot be resolved, it **matches any address**. With negation (`!`), it **rejects any address**.

**Generator requirement**: Prefer IP addresses over hostnames in ACLs. Warn when hostnames are used.

### 3.7 HTTP Request Smuggling

Multiple Varnish CVEs relate to request smuggling (VSV00007, VSV00008, VSV00010, VSV00015, VSV00016):
- Malformed chunked encoding
- Duplicate Host or Content-Length headers
- HTTP/2 smuggling bypassing VCL authorization

**Generator requirement**: Generate header sanitization at top of `vcl_recv` (strip duplicate Host, validate Content-Length).

---

## 4. VCL Generation Best Practices

### 4.1 What a VCL Generator MUST Enforce

1. **Explicit returns**: Every subroutine must end with a valid `return()` action
2. **Variable scope correctness**: Only use variables where they're readable/writable
3. **Cookie stripping**: Remove tracking cookies, preserve session cookies by whitelist
4. **Httpoxy mitigation**: `unset req.http.Proxy`
5. **PURGE ACL**: Restrict PURGE to authorized IPs
6. **Set-Cookie handling**: Never cache responses with Set-Cookie
7. **Lurker-friendly bans**: Copy request data to object headers for ban support
8. **No TTL=0**: Use `beresp.uncacheable = true` with positive TTL
9. **Restart guards**: Always check `req.restarts` before `return(restart)`
10. **Explicit `beresp.ttl`**: Set TTL directly, not via Cache-Control manipulation
11. **Internal header stripping**: Remove internal headers (x-url, x-host, etc.) in `vcl_deliver`

### 4.2 What a VCL Generator MUST Prevent

1. Generating `beresp.ttl = 0s` (causes serialization)
2. Using `return(pipe)` for regular HTTP (use `return(pass)` instead)
3. Unconditional `return(restart)` without restart counter check
4. Setting `beresp.http.Cache-Control` as a TTL mechanism in `vcl_backend_response`
5. Using `req.*` variables in ban expressions (ban lurker can't process them)
6. Trusting client-supplied headers for authorization
7. `Vary: *` (generator should never produce this)
8. `Vary: User-Agent` without normalization
9. `Vary: Cookie` (effectively uncacheable)
10. Hostname-based ACL entries without warning
11. Generating `ban()` (deprecated since 6.6, use `std.ban()`)

### 4.3 What a VCL Generator SHOULD Do

1. **Normalize query strings**: `std.querysort(req.url)` + strip marketing parameters
2. **Normalize Accept-Encoding**: Collapse to "gzip" or unset
3. **Normalize User-Agent** (if Vary'd on): Reduce to categories
4. **Lowercase Host header**: Prevent case-sensitivity cache splits
5. **Strip port from Host**: Prevent `example.com` vs `example.com:443` cache split
6. **Generate health probes**: For all backends
7. **Generate grace configuration**: With sane defaults (10s for healthy, 6h for sick backends)
8. **Generate custom error pages**: Replace "Guru Meditation" with branded pages
9. **Cache short error responses**: Even 0.1s TTL on 5xx prevents backend hammering
10. **Make VCL idempotent**: Guard operations that may execute multiple times (restarts, shielding)
11. **Use VMODs over regex**: Prefer `vmod_cookie`, `vmod_urlplus`, `vmod_accept` over complex regex

### 4.4 How Commercial Products Handle VCL Generation

**Fastly**:
- Translates UI/API configuration into generated VCL automatically
- Generated VCL is viewable via "Show VCL"
- Key insight: VCL code may run more than once per request (restarts, shielding) -- all generated code must be idempotent
- Uses `fastly.ff.visits_this_service` (validated internally) instead of forgeable `Fastly-FF` header
- `regsub()` returns the full input on no match (data leak risk) -- Fastly recommends `if()` with `re.group.1` instead

**Varnish Enterprise**:
- Varnish Controller deploys VCL files across fleets
- VCL validation before deployment via `varnishd -Cf`
- Uses MSE (Massive Storage Engine) for persistent cache
- Enterprise VMODs (aclplus, rewrite, xkey) for common patterns

**IBM Varnish Operator (Kubernetes)**:
- Template files using Go templates for dynamic backend injection
- Backends are injected at runtime based on Kubernetes service discovery

---

## 5. Varnish 7.x Specific Changes

### 5.1 Breaking Changes from 6.x to 7.x

| Change | Impact | Generator Action |
|---|---|---|
| PCRE -> PCRE2 | Parameters renamed: `pcre_match_limit` -> `pcre2_match_limit` | Use PCRE2 parameter names |
| Number format RFC8941 | Max 15 digits, max 3 decimal places, no scientific notation | Validate generated numbers |
| `ban()` deprecated | Use `std.ban()` + `std.ban_error()` instead | Always generate `std.ban()` |
| ACL pedantic default | Pedantic mode ON by default, use `-pedantic` flag to disable | Generate ACLs with proper masking |
| VCL_acl logging off | Must add `+log` flag to ACL declarations | Include `+log` when logging needed |
| Workspace defaults increased | `workspace_client` 64kB->96kB, `workspace_backend` 64kB->96kB | Adjust recommendations |
| `std.rollback()` from `vcl_pipe` | Now causes VCL failure | Never generate rollback in pipe |
| Accept-Ranges on pass | No longer auto-generated for passed objects (7.6) | Generate explicitly if needed |

### 5.2 New Features to Leverage

| Feature | Version | Description |
|---|---|---|
| `req.hash_ignore_vary` | 7.0+ | Skip Vary check during lookup (freshness only) |
| `req.filters` | 7.x | Control response body filtering in `vcl_recv` |
| H2 VMOD | 7.5+ | Per-session HTTP/2 tuning (`h2.is()`, rapid reset params) |
| `pipe_task_deadline` | 7.5+ | Max duration for pipe transactions |
| `vcl_req_reset` feature flag | 7.5+ | Interrupt tasks when H2 stream closes |
| ACL `+fold` flag | 7.5+ | Merge adjacent subnets for optimization |
| `vcl_backend_refresh` | 7.x | New subroutine for conditional requests |

### 5.3 Version Detection for Generator

The generator should produce VCL compatible with the target Varnish version:
- `vcl 4.0;` for Varnish 4.x-5.x
- `vcl 4.1;` for Varnish 6.x+ (introduces `beresp.filters`, `resp.body` in synth)

---

## 6. Regex-Specific VCL Bugs

### 6.1 regsub() Returns Full Input on No Match

```vcl
// BUG: If cookie:auth doesn't match, returns the ENTIRE cookie value
set var.result = regsub(req.http.cookie:auth, "pattern", "\1");
```

**Fix**: Use `if()` with explicit match check:
```vcl
if (req.http.cookie:auth ~ "pattern") {
    set var.result = regsub(req.http.cookie:auth, "pattern", "\1");
} else {
    set var.result = "";
}
```

### 6.2 Catastrophic Backtracking

PCRE2 is a backtracking engine. Poorly crafted patterns can cause:
- High CPU usage
- Stack overflow (segfault/panic)
- Thread starvation

Controlled by `pcre2_match_limit` and `pcre2_depth_limit` (global settings).

### 6.3 Empty Strings Are Truthy

```vcl
// BUG: req.url.qs is "" (empty string), which is TRUTHY
if (req.url.qs) {
    // This executes even with no query string!
}

// FIX:
if (std.strlen(req.url.qs) > 0) {
    // Only executes with actual query string
}
```

**Generator requirement**: Use explicit length/existence checks, never rely on string truthiness for potentially empty values.

---

## 7. Connection and Backend Bugs

### 7.1 Backend Idle Timeout Race Condition

Varnish default `backend_idle_timeout` is 60s. Many backends (Apache, Node.js) default to 5s keep-alive timeout. This creates a race:
1. Backend closes connection after 5s idle
2. Varnish tries to reuse the connection (still within its 60s window)
3. Connection reset, 503 error

**Generator requirement**: Generate `backend_idle_timeout` that is LESS than the backend's keep-alive timeout (e.g., backend 5s -> Varnish 4s).

### 7.2 Health Probe False Positives

**Bug**: Some backends return `HTTP/1.1 200` (no reason phrase). Varnish health probes may reject this as invalid.

**Generator requirement**: Generate probes with `.expected_response = 200` and test tolerance.

### 7.3 Connection Pooling Across VCLs

Backend connections are pooled by `.host`/`.port`. Two backends with the same address but different names share a connection pool. This can cause unexpected behavior when VCLs are reloaded.

---

## 8. ESI-Specific Bugs

1. **HTTPS in ESI includes**: Ignored by default. Requires `-p feature=+esi_ignore_https`
2. **Non-XML content**: ESI parser expects `<` as first byte. Non-XML files silently skip ESI processing
3. **ESI fragment failure**: Silent by default (acts as `onerror="continue"`)
4. **ESI + streaming**: Streaming may need to be disabled for ESI
5. **vcl_backend_error caching**: Synthetic responses from `vcl_backend_error` CAN end up in cache (unlike `vcl_synth` which never caches)

---

## 9. Checklist for VCL Generator Validation

### Compile-Time Checks (at generation time)
- [ ] All subroutines end with valid `return()` actions
- [ ] Variable reads/writes are in correct subroutine scope
- [ ] No `beresp.ttl = 0s` generated
- [ ] No `ban()` generated (use `std.ban()`)
- [ ] No `return(pipe)` for regular HTTP traffic
- [ ] All restarts guarded by `req.restarts` check
- [ ] PURGE method guarded by ACL
- [ ] `req.http.Proxy` stripped
- [ ] Internal headers stripped in `vcl_deliver`
- [ ] Cookie whitelist applied (not blacklist)
- [ ] Set-Cookie stripped from cacheable responses
- [ ] Numbers conform to RFC8941 (max 15 digits, 3 decimal places)
- [ ] Ban expressions are lurker-friendly (use `obj.http.*`, not `req.*`)

### Structural Checks
- [ ] All backends have health probes
- [ ] Grace mode configured with explicit values
- [ ] Vary headers are normalized before affecting cache key
- [ ] Host header normalized (lowercase, no port)
- [ ] Query strings sorted (`std.querysort`)
- [ ] Marketing parameters stripped
- [ ] Custom error pages defined
- [ ] X-Forwarded-For handled securely

### Version-Specific Checks (Varnish 7.x)
- [ ] Uses `vcl 4.1;` declaration
- [ ] Uses `std.ban()` not `ban()`
- [ ] PCRE2 parameter names used
- [ ] No scientific notation in numbers
- [ ] ACL entries use proper masking
- [ ] ACL `+log` flag added when logging needed

---

## Sources

### Varnish Software Official
- [Painful Varnish Mistakes](https://info.varnish-software.com/blog/painful-varnish-mistakes)
- [10 Varnish Cache Mistakes and How to Avoid Them](https://info.varnish-software.com/blog/10-varnish-cache-mistakes-and-how-avoid-them)
- [Hit-for-Miss and Why a NULL TTL is Bad](https://info.varnish-software.com/blog/hit-for-miss-and-why-a-null-ttl-is-bad-for-you)
- [PSA: You Don't Need That Many Regexes](https://info.varnish-software.com/blog/psa-you-dont-need-that-many-regexes)
- [Using Pipe in Varnish](https://info.varnish-software.com/blog/using-pipe-varnish)
- [When to Use Pipe](https://info.varnish-software.com/blog/when-to-use-pipe)
- [Ban Lurker](https://info.varnish-software.com/blog/ban-lurker)
- [Cache Invalidation in Varnish](https://info.varnish-software.com/blog/cache-invalidation-in-varnish)
- [Handling the X-Forwarded-For Header](https://info.varnish-software.com/blog/handling-the-x-forwarded-for-header)
- [Example VCL Template](https://www.varnish-software.com/developers/tutorials/example-vcl-template/)
- [Built-in VCL Tutorial](https://www.varnish-software.com/developers/tutorials/varnish-builtin-vcl/)
- [Troubleshooting Varnish](https://www.varnish-software.com/developers/tutorials/troubleshooting-varnish/)
- [Banning Content](https://www.varnish-software.com/developers/tutorials/ban/)
- [Removing Cookies](https://www.varnish-software.com/developers/tutorials/removing-cookies-varnish/)
- [Tuning Varnish](https://docs.varnish-software.com/book/operations/tuning-varnish/)

### Varnish Cache Documentation
- [Changes in Varnish 7.0](https://vinyl-cache.org/docs/7.6/whats-new/changes-7.0.html)
- [Changes in Varnish 7.5](https://varnish-cache.org/docs/trunk/whats-new/changes-7.5.html)
- [Built-in Subroutines](https://varnish-cache.org/docs/6.0/users-guide/vcl-built-in-subs.html)
- [VCL Variables Reference](https://vinyl-cache.org/docs/trunk/reference/vcl-var.html)
- [Grace Mode](https://vinyl-cache.org/docs/trunk/users-guide/vcl-grace.html)
- [Hashing](https://varnish-cache.org/docs/trunk/users-guide/vcl-hashing.html)
- [Security Advisories](https://varnish-cache.org/security/)

### Fastly
- [Best Practices for Using the Vary Header](https://www.fastly.com/blog/best-practices-using-vary-header)
- [Better VCL for More Maintainable Configurations](https://www.fastly.com/blog/maintainable-vcl)

### GitHub Issues
- [Backend Workspace Overflow #4232](https://github.com/varnishcache/varnish-cache/issues/4232)
- [Workspace Overflow Handling](https://github.com/varnishcache/varnish-cache/issues/2012)
- [Connection Header on Pipe #2337](https://github.com/varnishcache/varnish-cache/issues/2337)
- [Backend Idle Timeout #3633](https://github.com/varnishcache/varnish-cache/issues/3633)
- [First Byte Timeout on Keep-Alive #1772](https://github.com/varnishcache/varnish-cache/issues/1772)
- [Health Probes Without Reason Phrase #2069](https://github.com/varnishcache/varnish-cache/issues/2069)

### Other
- [Varnish Panic from Low Workspace - Claudio Kuenzler](https://www.claudiokuenzler.com/blog/737/varnish-panic-crash-low-sess-workspace-backend-client-sizing)
- [Cache Invalidation Strategies - Smashing Magazine](https://www.smashingmagazine.com/2014/04/cache-invalidation-strategies-with-varnish-cache/)
- [Web Cache Poisoning - PortSwigger](https://portswigger.net/web-security/web-cache-poisoning/exploiting-design-flaws)
