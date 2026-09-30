# Truth fixture shape catalog

Recorded 2026-09-30 by `record-fixtures-truth.sh` against `varnish:8.0.2`,
one scenario per `docker restart vslp3-varnish; sleep 2` cycle so each
fixture is a clean VSL. This file is the authoritative source later tasks
(P3 Task 2 onward) reconcile their code against — **fixture truth outranks
this plan's code sketches**; where a sketch disagrees with what's quoted
below, the code changes, not the fixture.

All group-header line numbers below are 1-based into the fixture file named
in the section heading. `varnishlog -g request -d` groups the whole
transaction tree reachable from a top-level request/session into one
printed block; `*`/`**`/`***` is nesting depth *by indentation prefix*, not
necessarily parent/child in the object-oriented sense — see "Restart's
depth is a trap" below.

## Rig deviation: ESI needs `+esi_disable_xml_check`

`esi.txt`'s backend body is `before <esi:include src="/frag"/> after` (the
brief's fixed body — first byte is `b`, not `<`). Recorded as-is against a
vanilla `varnish:8.0.2`, this STOP-gate fired exactly as the brief warned it
might: the bereq's `Filters` list included `esi` (so `beresp.do_esi = true`
was honored), but instead of a subrequest we got

```
--  ESI_xmlerror   WARN: No ESI processing, first char not '<'. (See feature esi_disable_xml_check)
```

and no `Link ... esi` record, no child `Request` group — a flat single-fetch
transaction indistinguishable from a plain miss. Per the brief's named
off-ramp, the rig now starts varnish with

```
docker run -d --name vslp3-varnish --network "$NET" \
    -v "$PWD/default.vcl:/etc/varnish/default.vcl:ro" \
    varnish:8.0.2 -p feature=+esi_disable_xml_check >/dev/null
```

(`-p param=value` appended after the image name is passed straight to
`varnishd` by the official image's entrypoint.) With that flag, `esi.txt`
shows the expected subrequest (quoted below). **Task 7 must mirror this
exact incantation** — the generator's tracing-enabled VCL path needs the
same `feature=+esi_disable_xml_check` param wired into the varnishd
invocation (or an equivalent `vcl_init { std.set_ttl... }`-style workaround
was not tried since the feature flag alone resolved it), or ESI fragments
in production will silently degrade to flat fetches exactly like this STOP
did.

No other rig deviations from the brief were needed. `truthbackend.py` is
verbatim from the brief. The VCL blocks (`vcl_recv` traceparent-validation,
`vcl_backend_fetch` minting) are the P2 script's blocks verbatim, confirmed
by diff against `record-fixtures-tracing.sh`. The scenario rules
(pipe/synth/restart/retry/do_esi) match the brief's Step 2 code block
exactly.

---

## coalesce.txt — two concurrent `/slow` requests

**MUST-hold check: two Request groups, exactly ONE fetch between them.**
Confirmed. Group headers:

```
1:*   << Request  >> 32770        (the waiter — printed FIRST in the file)
43:**  << BeReq    >> 3            (nested under Request 2, not under 32770)
84:*   << Request  >> 2            (the initiator — printed SECOND, separated by a blank line)
```

Correction to the naive reading: `varnishlog -g request` prints one block
per **top-level transaction root**. The waiter (req vxid 32770) and the
initiator (req vxid 2) are two independent top-level roots — hence the
blank line between them (line 83) — even though they share one fetch
(bereq vxid 3, printed nested under the *initiator's* block, at `**`).
There is exactly one `BeReq` group in the whole file.

**Link record** (in the initiator's Request group, line 23):

```
-   Link           bereq 3 fetch
```

Reason is `fetch`, matching the coalescing MISS path.

**Hit payload in the waiter** (line 104, in the *waiter's* Request group):

```
-   Hit            3 121.970332 3600.000000 0.000000 5 5
```

Per `varnishd -x vsl` for `Hit`: `%u %f %f %f [%u [%u]]` = VXID of the
looked-up object (`3` — the same bereq vxid as the fetch `Link`, this is
how a Linker resolves coalescing: match `Hit <vxid>` against a fetch
vxid→span cache), remaining TTL (`121.970332`), grace (`3600.0`, our
`beresp.grace = 1h`), keep (`0.0`), and **two optional trailing fields
present here**: fetched-so-far and content-length, both `5` (the 5-byte
`hello` body). The waiter's group also carries a `Timestamp Waitinglist:`
record (absent from every other fixture) immediately after the `Hit` line
— the waiter blocked on the busy object before delivering.

The waiter has **no** `Link` record of its own and **no** nested `BeReq`
group — it never fetches; it only cites vxid 3 via `Hit`.

Every bereq's minted `BereqHeader traceparent` (one bereq total):

```
--  BereqHeader    traceparent: 00-84880075c2414ee4b2e02eb6cdf2f9fa-151a9e1aea9f4c00-01
```

Self-rooted (no incoming client traceparent in this scenario) — full new
trace id minted by the `else` branch of the P2 minting block.

---

## grace_bgfetch.txt — prime, lapse, stale hit triggers bgfetch

Group headers:

```
1:*   << Request  >> 2            (the priming request — plain miss+fetch)
44:**  << BeReq    >> 3
86:*   << Request  >> 32770       (the stale hit, new top-level root, blank-line separated)
130:**  << BeReq    >> 32771      (the background fetch, nested under 32770)
```

**Link records:**

```
-   Link           bereq 3 fetch      (priming request → its fetch)
-   Link           bereq 32771 bgfetch (stale-hit request → its background fetch)
```

The `bgfetch` reason is the discriminator vs. a coalescing `fetch`/plain
`fetch`. Per the spec's truth-semantics table ("Grace / bgfetch: Child of
the triggering request span"), the nesting here **does** match: bereq 32771
is printed at `**`, nested directly under Request 32770 — same shape as an
ordinary miss+fetch, just triggered from a HIT instead of a MISS.

**Hit payload for the grace hit** (line 106, note: only 4 fields, no
fetched-so-far/content-length):

```
-   Hit            3 -1.690501 3600.000000 0.000000
```

Contrast with coalesce.txt's 6-field `Hit` line above. The remaining-TTL
field is **negative** (`-1.690501`) — that's the tell for "past TTL, still
in grace" as opposed to a fresh hit (positive remaining TTL, see
restart.txt's `Hit` below). The two optional fields (fetched-so-far,
content-length) only appeared in coalesce.txt's `Hit`, where the object was
still busy/streaming at lookup time; here the object is a complete, stored,
non-busy object, so Varnish omits them. **A parser must treat both 4-field
and 6-field `Hit` payloads as valid**, not just the longer form.

Response header confirms staleness: `Age: 2` on a `max-age=1` object.

Minted `BereqHeader traceparent` (two bereqs, unrelated — both self-root
independently since neither carries an inherited traceparent):

```
--  BereqHeader    traceparent: 00-88b30bd4354a408c9a465712ce907141-9059c7b9aa164cf6-01   (priming fetch, bereq 3)
--  BereqHeader    traceparent: 00-efc208b9187c467f8352be4ee6b98c9a-7f6b8f3b1ef2497f-01   (background fetch, bereq 32771)
```

Different trace ids entirely — the bgfetch is not correlated to the priming
fetch's trace by any VCL mechanism; only the `Link ... bgfetch` on the
*triggering* (stale-hit) request ties the bgfetch's bereq vxid back to a
client request at all.

---

## esi.txt — `/esi` with `+esi_disable_xml_check`

**MUST-hold check: shows a subrequest for `/frag`.** Confirmed only after
the feature-flag fix above. Group headers:

```
1:*   << Request  >> 2      (the /esi request)
47:**  << BeReq    >> 3      (its fetch)
88:**  << Request  >> 4      (the ESI subrequest for /frag — SAME depth as BeReq 3)
127:*** << BeReq    >> 5     (the subrequest's own fetch)
```

**ESI subrequests nest as child Request groups within the same printed
block as their parent** (no blank line anywhere in this file — one
combined block, confirmed via `awk` scan for blank lines), answering the
brief's open question directly: they do **not** surface as separate
top-level groups under `-g request`. Depth-wise, the ESI child `Request`
group (`**`) sits at the same indentation level as its parent's `BeReq`
group, and its own `BeReq` goes one level deeper (`***`).

**Link records** (both in Request 2's block):

```
-   Link           bereq 3 fetch
-   Link           req 4 esi 1
```

`Link req 4 esi 1` — child type `req`, child vxid `4`, reason `esi`,
trailing field `1` (per `varnishd -x vsl` Link's optional 4th field,
"child task sub-level" — this is the ESI recursion depth). The nested
subrequest itself then carries its own `Link bereq 5 fetch` (line 108) for
its fetch, same shape as a top-level request.

**Minted `BereqHeader traceparent`** — the surprising part:

```
--  BereqHeader    traceparent: 00-62f872aa18c246ad85a8548c91647026-d663ba2b16ec4114-01   (bereq 3, the /esi fetch)
--- BereqHeader    traceparent: 00-0f219f3312d3484e98fa26b4827938dc-f73f82a9bd1b4e19-01   (bereq 5, the /frag fetch)
```

**Different trace ids.** The ESI subrequest (Request 4) carries no
`ReqHeader traceparent` at all — Varnish does not propagate
`req.http.traceparent` from the parent client request into a synthesized
ESI subrequest automatically, and neither the P2 `vcl_recv` block nor this
rig's scenario rules do it explicitly. So bereq 5 hits the minting block's
`else` (self-root) branch same as if it were an unrelated top-level
request, producing a **completely unrelated trace id** from its parent.
**Task 7 / the generator's ESI story must decide**: either add explicit VCL
to copy `req.http.traceparent` into the ESI subrequest before `vcl_recv`
runs there (not proven here — out of this task's scope), or the Linker
must stitch ESI children onto the parent purely via the structural
`Link req <vxid> esi <n>` record, ignoring trace id correlation entirely
for this case. NOTES.md records the observed truth; it does not resolve
this fork.

---

## restart.txt — `/teapot` (418) triggers one restart

**Restart's depth is a trap.** Group headers:

```
1:*   << Request  >> 2      (original request)
39:**  << BeReq    >> 3      (its fetch — the 418)
80:*   << Request  >> 4      (the restarted request — marked '*', SAME depth as Request 2)
```

No blank line anywhere before line 116 (EOF) — confirmed via `awk` blank
scan — so Request 4 is part of the *same* printed block as Request 2, but
its indentation prefix is `*`, identical to a fresh top-level request, NOT
`**` as a naive "restarts are children" reading would predict. The spec's
truth-semantics table says "Restarts: Child spans under the original
request span" — that is a *logical* truth this task's Linker must
construct; it is **not** what the raw grouping/indentation hands you. The
only structural evidence tying Request 4 to Request 2 is the `Link`
record — depth alone is not sufficient to reconstruct the parent/child
relationship for restarts. (Contrast with ESI, where depth *does* encode
the parent/child relationship faithfully.)

**Link record** (line 37, in Request 2's block):

```
-   Link           req 4 restart
```

Child type `req`, child vxid `4`, reason `restart`, no trailing sub-level
field (that field is ESI-specific).

**What actually happens on restart — an unrequested but real finding:**
because our scenario VCL only bumps `beresp.ttl` to `60s` when it was
`<= 0s` and unconditionally sets `beresp.grace = 1h`, the 418 response
becomes **cacheable** (RFC line shows `TTL RFC -1 ...`, i.e. naturally
uncacheable, but the VCL override wins: `TTL VCL 60 10 0 ...` then
`TTL VCL 60 3600 0 ...`, one `TTL` record per `set beresp.ttl`/
`set beresp.grace` mutation per `varnishd -x vsl`'s TTL tag docs). So the
restarted request (Request 4) does not re-fetch — it **HITs the
newly-cached 418** it just stored:

```
-   Hit            3 60.000772 3600.000000 0.000000
```

(4-field form again, positive remaining TTL — a fresh, non-grace hit,
confirming the earlier reading that the two optional trailing fields are
about busy-object state, not scenario type.) `vcl_deliver`'s
`req.restarts == 0` guard correctly prevents a second restart, so the
final response delivered to the client is the same 418 both times. This is
real, recorded behavior, not a rig bug — restart tests/parsers must not
assume the retried path always re-fetches from the backend.

**Minted `BereqHeader traceparent`** (one bereq — the restarted request
never fetches, it hits):

```
--  BereqHeader    traceparent: 00-e999f079fc2d47cd863aef9e34bd03df-2abf26de16de4e08-01
```

---

## retry.txt — `/flaky` (500 then 200)

**MUST-hold check: two backend attempts.** Confirmed. Group headers:

```
1:*   << Request  >> 2       (the client request)
43:**  << BeReq    >> 3       (attempt 1 — 500)
77:*** << BeReq    >> 32769   (attempt 2 — 200, nested UNDER attempt 1, not sibling)
```

**The retried bereq sits nested under the first bereq** (`***`, one level
deeper than `**`), answering the brief's open question directly — it is
not a sibling `BeReq` group at the same `**` depth as attempt 1.

**Link record** (line 75, inside BeReq 3's block, not the client
Request's):

```
--  Link           bereq 32769 retry
```

Note this `Link` lives in the **BeReq** group (prefix `--`), not the
client Request group — the only scenario in this set where a `Link` record
originates from a bereq rather than a request.

**Minted `BereqHeader traceparent`** — three lines across the two
attempts, showing the retry keeps the trace id and re-mints only the span
id:

```
--  BereqHeader    traceparent: 00-677cf25cdf1e4e64ba6ed681d14ebe45-94a1637fb97747db-01   (attempt 1, minted at BACKEND_FETCH)
--- BereqHeader    traceparent: 00-677cf25cdf1e4e64ba6ed681d14ebe45-94a1637fb97747db-01   (attempt 2, inherited header, pre-VCL_call)
--- BereqUnset     traceparent: 00-677cf25cdf1e4e64ba6ed681d14ebe45-94a1637fb97747db-01   (attempt 2, unset at BACKEND_FETCH)
--- BereqHeader    traceparent: 00-677cf25cdf1e4e64ba6ed681d14ebe45-1e96313246cb4e81-01   (attempt 2, freshly minted)
```

Because the retried bereq inherits attempt 1's headers verbatim (including
its already-minted `traceparent`), the minting block's `if` branch fires
(header present → regsub only the span-id segment) rather than the `else`
(self-root) branch, so the **trace id stays `677cf25c...` across both
attempts** while the span id changes per attempt
(`94a1637f...` → `1e963132...`). This is the one scenario where two
distinct minted span ids share one trace id — a Linker building one fetch
span per retry attempt should key spans by (trace id, span id) pair, not
assume one attempt per trace id.

---

## pipe.txt — `/pipe`

**MUST-hold check: client group lacks a plain `Resp` timestamp.** Confirmed
— the full `Timestamp` label set present in this fixture, in order:

```
Timestamp Start:
Timestamp Req:
Timestamp Process:
Timestamp Pipe:
Timestamp PipeSess:
```

No `Resp`. No `Fetch`, `Beresp`, or `BerespBody` either (piping bypasses
the ordinary response-processing path). `PipeSess` is present (it marks
when the piped byte-stream session itself ended) — answering the brief's
explicit question: `Resp` absent, `PipeSess` present.

Group headers:

```
1:*   << Request  >> 2
28:**  << BeReq    >> 3
```

**Link record:**

```
-   Link           bereq 3 pipe
```

Reason `pipe` — the sixth and last distinct `Link` reason observed across
this fixture set (the full list: `fetch`, `bgfetch`, `esi`, `restart`,
`retry`, `pipe`).

The `BeReq` group has **no minted `BereqHeader traceparent`** — the P2
minting block lives in `vcl_backend_fetch`, but a piped bereq never calls
`vcl_backend_fetch` (there's no `VCL_call BACKEND_FETCH` line anywhere in
this fixture); the raw bytes are piped straight through instead. **Pipe
requests never get a minted fetch span id** — a fetch-span-per-request
assumption breaks here by design, matching the spec table's "Pipe / synth:
... no fetch span pretensions."

`BereqAcct 0 0 0 0 0 0` and the `PipeAcct` line (`121 201 0 117`, on the
client group) are pipe-specific accounting records not seen elsewhere in
this set.

---

## synth.txt — `/synth` (410 synthetic)

No backend interaction at all — confirmed by the complete absence of a
`BeReq` group and of any `Link` record. The entire fixture is one
`Request` group:

```
1:*   << Request  >> 2
```

`Timestamp` labels present: `Start`, `Req`, `Process`, `Resp` — the
smallest label set of any fixture in this set (no `Fetch`/`Bereq`/
`Beresp`/`BerespBody`, unlike even pipe.txt which at least reaches
`Connected`).

```
-   VCL_call       RECV
-   VCL_return     synth
...
-   RespProtocol   HTTP/1.1
-   RespStatus     410
-   RespReason     gone
-   RespHeader     Date: Wed, 30 Sep 2026 12:36:36 GMT
-   RespHeader     Server: Varnish
-   RespHeader     X-Varnish: 2
-   VCL_call       SYNTH
```

`VCL_call SYNTH` is the discriminator vs. every other scenario's
`VCL_call DELIVER`-only path. Matches the spec table's "Pipe / synth:
Request span ...; no fetch span pretensions" — synth is the same "no
backend at all" shape as pipe minus even the `BeReq` group and the
`Connected`/`Bereq` timestamps.

---

## Cross-cutting observations for later tasks

- **`Link` reasons observed, all six**: `fetch`, `bgfetch`, `esi`,
  `restart`, `retry`, `pipe`. No fixture produced a `Link ... synth` (synth
  never links to anything — there's nothing to link to).
- **`Hit` payload has two shapes**: 4-field (`vxid ttl grace keep`) for a
  hit on a complete, non-busy object (grace_bgfetch.txt, restart.txt), and
  6-field (adds fetched-so-far + content-length) when the looked-up object
  was still busy/streaming at lookup time (coalesce.txt's waiter). A
  parser must accept both.
  A negative remaining-TTL field distinguishes a grace hit from a fresh
  hit; both use the same 4-field shape otherwise.
- **Blank-line separation in `-g request -d` output marks independent
  top-level transaction roots**, not narrative order. Coalescing's waiter
  and initiator are two blank-line-separated blocks (different roots,
  correlated only via `Hit <vxid>` against the shared fetch's vxid);
  restart's original and restarted requests are *not* separated by a
  blank line (same top-level session, still two `*`-depth groups) even
  though they are two distinct `Begin req ... reason` transactions.
- **Depth prefix (`*`/`**`/`***`) is not a reliable parent/child proxy on
  its own.** It's faithful for ESI (child request one level deeper than
  its trigger's bereq) and for retry (retried bereq one level deeper than
  the failed attempt), but restart resets to `*` — the shallowest depth —
  for its child request. Always resolve parent/child via the `Link`
  record's child vxid, never via prefix depth alone.
- Every minted `BereqHeader traceparent` observed in this set is
  self-rooted (`00-<32 hex>-<16 hex>-01`) because no scenario in the
  brief's recording sequence sends an incoming `traceparent` header; P2's
  fixtures (`rewrite_miss.txt`, `rewrite_selfroot.txt`) already cover the
  incoming-header rewrite path and are untouched by this task.
