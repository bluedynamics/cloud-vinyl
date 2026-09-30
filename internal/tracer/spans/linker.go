package spans

import (
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/bluedynamics/cloud-vinyl/internal/tracer/vsl"
)

// cacheEntry pairs a cached value with the wall-clock time it was inserted,
// for TTL eviction measured in real processing time — not any VSL
// timestamp. The cache bounds how long the Linker waits, while pumping the
// live log, for a correlating group to arrive; that is unrelated to how far
// apart the correlated spans' own VSL times are (coalesce.txt's waiter and
// initiator are ~30ms apart in VSL time but could be minutes apart in
// log-processing time under load or a restart).
type cacheEntry[V any] struct {
	value      V
	insertedAt time.Time
}

// vxidCache is a bounded, TTL-expiring map keyed by VSL vxid: a map for O(1)
// lookup plus a FIFO ring of keys, oldest first, for O(1) amortized
// eviction. It is insertion-ordered, not access-ordered (a get never moves
// an entry within the ring) — simple and sufficient for this use, where
// every vxid is written once and read at most once. A re-set of an
// already-cached vxid (which would update its value/timestamp without
// moving its ring position, the one place insertion- vs access-order would
// matter) is not a realistic concern here: Varnish mints vxids
// monotonically per transaction for the lifetime of a varnishd process, so
// the same vxid recurring within one process's TTL window would require
// billions of intervening transactions or a wraparound neither this cache
// nor its callers are sized for. Single-goroutine use only (the
// supervisor's handle closure calls Linker.Build serially) — no mutex
// guards it.
type vxidCache[V any] struct {
	capacity int
	ttl      time.Duration
	items    map[uint64]cacheEntry[V]
	order    []uint64
}

func newVxidCache[V any](capacity int, ttl time.Duration) *vxidCache[V] {
	return &vxidCache[V]{capacity: capacity, ttl: ttl, items: make(map[uint64]cacheEntry[V])}
}

// set records value under vxid at wall-clock time now. It first drops
// entries older than ttl from the front of the ring (insertion order, so
// the oldest entries are checked first and the scan can stop at the first
// survivor), then evicts the single oldest remaining entry if capacity is
// now exceeded.
func (c *vxidCache[V]) set(vxid uint64, value V, now time.Time) {
	for len(c.order) > 0 {
		oldest := c.order[0]
		if e, ok := c.items[oldest]; ok && now.Sub(e.insertedAt) <= c.ttl {
			break
		}
		delete(c.items, oldest)
		c.order = c.order[1:]
	}
	if _, exists := c.items[vxid]; !exists {
		c.order = append(c.order, vxid)
	}
	c.items[vxid] = cacheEntry[V]{value: value, insertedAt: now}
	if len(c.order) > c.capacity {
		evict := c.order[0]
		c.order = c.order[1:]
		delete(c.items, evict)
	}
}

// get returns the live (not expired as of now) value stored under vxid, if
// any.
func (c *vxidCache[V]) get(vxid uint64, now time.Time) (V, bool) {
	e, ok := c.items[vxid]
	if !ok || now.Sub(e.insertedAt) > c.ttl {
		var zero V
		return zero, false
	}
	return e.value, true
}

// fetchInfo is what the Linker remembers about a fetch span, keyed by the
// BeReq's own VXID, so a later HIT record naming that vxid can resolve it.
type fetchInfo struct {
	traceID trace.TraceID
	spanID  trace.SpanID
	end     time.Time // the fetch span's own End, for coalescing comparison
}

// restartInfo is what the Linker remembers about an original request span
// that restarted, keyed by the restarted continuation's own VXID (the child
// vxid named in that request's own "Link req <vxid> restart" record) — see
// Build's doc comment for why that key equals the continuation top-level
// Tx's VXID.
type restartInfo struct {
	traceID trace.TraceID
	spanID  trace.SpanID
}

// Linker wraps BuildWithOutcome with cross-transaction correlation that a
// single top-level Tx cannot see on its own:
//   - hit-to-fetch links, including coalescing detection (NOTES.md's
//     coalesce.txt and grace_bgfetch.txt sections);
//   - restart continuation stitching, parenting a restarted request's span
//     under the original request's span and into its trace (NOTES.md's
//     restart.txt section — "Restarts: Child spans under the original
//     request span" is a logical truth the raw grouping/depth does not hand
//     you; only the Link record ties the two Tx's together).
//
// Both are driven by small bounded, TTL-expiring caches of vxid -> span
// identity. Single-goroutine use only (the supervisor's handle closure
// calls Build serially, exactly like it called BuildWithOutcome directly
// before this task) — no mutex guards the caches.
type Linker struct {
	ids      IDSource
	fetches  *vxidCache[fetchInfo]
	restarts *vxidCache[restartInfo]

	// OnCacheMiss, if set, is called synchronously whenever a correlation
	// genuinely cannot be resolved against the cache — a HIT record whose
	// fetch vxid was never seen or has aged out, or a restart continuation
	// whose original was never seen or has aged out. cmd/tracer wires this
	// to the vinyl_tracer_link_cache_misses_total counter; the spans
	// package itself stays free of a metrics dependency, matching how
	// BuildWithOutcome's Outcome return already lets the caller own its own
	// counters instead of this package reaching for one directly.
	OnCacheMiss func()
}

// NewLinker returns a Linker whose fetch-identity and restart-identity
// caches each hold up to capacity entries for up to ttl.
func NewLinker(capacity int, ttl time.Duration, ids IDSource) *Linker {
	return &Linker{
		ids:      ids,
		fetches:  newVxidCache[fetchInfo](capacity, ttl),
		restarts: newVxidCache[restartInfo](capacity, ttl),
	}
}

// Build wraps BuildWithOutcome, then layers cross-transaction correlation
// onto the result:
//
//  1. Restart continuation: if tx is a top-level Request whose own "Begin"
//     record marks it a restart (payload contains "restart" — the only
//     reasons a top-level Request's Begin carries are "rxreq" and
//     "restart", NOTES.md's restart.txt section), look up tx.VXID in the
//     restart-identity cache. A hit means an earlier Build call saw the
//     ORIGINAL request's own "Link req <vxid> restart" record naming this
//     same vxid (restart.txt: original's Link names child vxid 4; the
//     continuation's own top-level Tx.VXID is that same 4 — the two Begin/
//     Link records reference each other symmetrically) — reparent the
//     whole built subtree under that original span and into its trace,
//     tagging varnish.restart_continuation=true. A miss self-roots exactly
//     as BuildWithOutcome already does, plus OnCacheMiss — counted only
//     when BuildWithOutcome's own Outcome is OutcomeSpans (a real,
//     fully-built Request group); an OutcomeUnusable/OutcomeUnsampled
//     empty result is a different, already-counted kind of loss and must
//     not also inflate this counter.
//  2. Record every fetch span just built (Name == "varnish fetch") into the
//     fetch-identity cache, keyed by its originating BeReq's own VXID.
//  3. Record this tx's own "Link req <vxid> restart" record(s), if any,
//     into the restart-identity cache, keyed by the named child vxid, so a
//     later top-level Tx with that VXID can resolve step 1.
//  4. Resolve this tx's own "Hit <vxid> ..." record, if any, against the
//     fetch-identity cache: a hit adds a Link to its origin fetch
//     (varnish.link="origin-fetch"), plus varnish.coalesced=true when the
//     hit's own Start precedes the cached fetch's End (the request
//     overlapped the still-in-flight fetch). An unresolvable vxid — the
//     fetch was never seen or aged out — adds
//     varnish.coalesced_unlinked=true instead, plus OnCacheMiss.
func (l *Linker) Build(tx *vsl.Tx) ([]Span, Outcome) {
	now := time.Now()
	built, outcome := BuildWithOutcome(tx, l.ids)
	if len(built) == 0 {
		// Purity: only OutcomeSpans means "this was a real Request group
		// that Build fully processed" (BuildWithOutcome's own doc: a
		// tx.Type != Request group also reports OutcomeSpans, since
		// neither is trace loss). OutcomeUnusable/OutcomeUnsampled are
		// intentional silence or already-counted data loss on their own
		// metric — not a failed restart-link correlation — so a Begin
		// marking this tx a restart continuation must not also inflate the
		// link-cache-misses counter here. (In practice this branch cannot
		// both have outcome == OutcomeSpans and isRestartContinuation(tx)
		// true: a real Request group with valid timestamps always builds
		// at least one span. The guard documents that invariant instead of
		// relying on it silently.)
		if outcome == OutcomeSpans && l.isRestartContinuation(tx) {
			if _, ok := l.restarts.get(tx.VXID, now); !ok {
				l.miss()
			}
		}
		return built, outcome
	}

	if l.isRestartContinuation(tx) {
		if orig, ok := l.restarts.get(tx.VXID, now); ok {
			reparentRestart(built, orig)
		} else {
			l.miss()
		}
	}

	for _, sp := range built {
		if sp.Name == "varnish fetch" {
			l.fetches.set(sp.vxid, fetchInfo{traceID: sp.TraceID, spanID: sp.SpanID, end: sp.End}, now)
		}
	}
	l.recordRestartLinks(tx, built[0], now)
	l.resolveHit(tx, &built[0], now)

	return built, outcome
}

// isRestartContinuation reports whether tx is a top-level Request whose own
// Begin record marks it as a restarted continuation. Begin's payload is
// "<type> <parent-vxid> <reason>" (NOTES.md's restart.txt section: "req 2
// restart"); checking the reason field positionally, rather than a bare
// substring match, avoids a false positive on some future reason string
// that merely contains "restart" as a substring.
func (l *Linker) isRestartContinuation(tx *vsl.Tx) bool {
	if tx.Type != txTypeRequest {
		return false
	}
	b, ok := tx.First("Begin")
	if !ok {
		return false
	}
	f := strings.Fields(b)
	return len(f) >= 3 && f[2] == restartReason
}

// reparentRestart rewrites built (a just-built restart continuation's
// spans) in place to parent under orig and join its trace: the top request
// span's ParentID becomes orig's span id, and every span in built that
// shared the top span's (self-rooted or inherited) trace id is moved onto
// orig's trace id instead.
func reparentRestart(built []Span, orig restartInfo) {
	oldTrace := built[0].TraceID
	built[0].ParentID = orig.spanID
	built[0].TraceID = orig.traceID
	built[0].Attrs = append(built[0].Attrs, attribute.Bool("varnish.restart_continuation", true))
	for i := 1; i < len(built); i++ {
		if built[i].TraceID == oldTrace {
			built[i].TraceID = orig.traceID
		}
	}
}

// recordRestartLinks scans tx's own records (never a descendant's — see
// spans.go's countRestarts, which this mirrors) for "Link req <vxid>
// restart" and remembers top's identity under each named child vxid.
func (l *Linker) recordRestartLinks(tx *vsl.Tx, top Span, now time.Time) {
	for _, r := range tx.Records {
		if r.Tag != "Link" {
			continue
		}
		f := strings.Fields(r.Payload)
		if len(f) < 3 || f[0] != "req" || f[2] != restartReason {
			continue
		}
		if vxid, err := strconv.ParseUint(f[1], 10, 64); err == nil {
			l.restarts.set(vxid, restartInfo{traceID: top.TraceID, spanID: top.SpanID}, now)
		}
	}
}

// resolveHit resolves tx's own "Hit <vxid> ..." record, if any, against the
// fetch-identity cache and annotates req (built's top request span)
// in place. Per NOTES.md: the Hit payload's first field is always the
// looked-up object's originating fetch vxid, whether the rest of the
// payload has 4 fields (a complete, non-busy object) or 6 (still
// busy/streaming at lookup time) — only the first field is needed here.
func (l *Linker) resolveHit(tx *vsl.Tx, req *Span, now time.Time) {
	raw, ok := tx.First("Hit")
	if !ok {
		return
	}
	f := strings.Fields(raw)
	if len(f) == 0 {
		return
	}
	vxid, err := strconv.ParseUint(f[0], 10, 64)
	if err != nil {
		return
	}
	fetch, ok := l.fetches.get(vxid, now)
	if !ok {
		req.Attrs = append(req.Attrs, attribute.Bool("varnish.coalesced_unlinked", true))
		l.miss()
		return
	}
	req.Links = append(req.Links, Link{
		TraceID: fetch.traceID,
		SpanID:  fetch.spanID,
		Attrs:   []attribute.KeyValue{attribute.String("varnish.link", "origin-fetch")},
	})
	if req.Start.Before(fetch.end) {
		req.Attrs = append(req.Attrs, attribute.Bool("varnish.coalesced", true))
	}
}

func (l *Linker) miss() {
	if l.OnCacheMiss != nil {
		l.OnCacheMiss()
	}
}
