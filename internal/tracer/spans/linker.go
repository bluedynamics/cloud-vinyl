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

// parkedSet holds one top-level Request group's already-built span set that
// Build withheld from its own caller because its "Hit <vxid> ..." record did
// not resolve against the fetch-identity cache at that instant.
//
// Why parking exists: varnishlog -g request emits a coalescing initiator's
// group (carrying the fetch) and the waiter's group (carrying the Hit) in
// completion order, which is a genuine ~50/50 race (see
// coalesce-investigation.md) — NOT ordered by which one started first. At
// the moment the waiter's own Build call runs, an unresolved Hit vxid is
// indistinguishable between "genuinely unresolvable" (the fetch identity
// was never recorded, or aged out of the fetches cache) and "the other half
// of that race: the initiator's own Build call, which is what records the
// fetch identity this vxid needs, simply has not happened yet." Parking
// gives the latter case a bounded window (the Linker's ageLimit) to resolve
// before conceding permanent degradation, instead of degrading immediately
// and irreversibly on the very call that lost the race.
type parkedSet struct {
	vxid      uint64
	spans     []Span
	arrivedAt time.Time
}

// defaultMaxParked and defaultParkAgeLimit bound the Linker's parking lot:
// at most this many unresolved hit-groups held back at once, each for at
// most this long, before Build gives up and releases it degraded. Both are
// deliberately small — this is a short-lived holding pen for a race that
// normally resolves within one or two Build calls (milliseconds of
// log-processing time), not a general-purpose store. A parked set overstays
// only when the thing it is waiting for (the initiator's fetch identity)
// genuinely never arrives — a crashed/restarted varnishd, a truncated log,
// or a coalescing hit whose initiator fell outside the fetch-identity
// cache's own TTL — at which point today's degradation (coalesced_unlinked
// + a link-cache-miss) is still exactly the right, honest answer.
const (
	defaultMaxParked    = 256
	defaultParkAgeLimit = 10 * time.Second
)

// Linker wraps BuildWithOutcome with cross-transaction correlation that a
// single top-level Tx cannot see on its own:
//   - hit-to-fetch links, including coalescing detection (NOTES.md's
//     coalesce.txt and grace_bgfetch.txt sections), now resolved for BOTH
//     VSL group-emission orderings via a small bounded parking lot (see
//     parkedSet's doc) rather than only the initiator-first half;
//   - restart continuation stitching, parenting a restarted request's span
//     under the original request's span and into its trace (NOTES.md's
//     restart.txt section — "Restarts: Child spans under the original
//     request span" is a logical truth the raw grouping/depth does not hand
//     you; only the Link record ties the two Tx's together).
//
// Both are driven by small bounded, TTL-expiring caches of vxid -> span
// identity, plus the parking lot for hit resolution. Single-goroutine use
// only (the supervisor's handle closure calls Build serially, exactly like
// it called BuildWithOutcome directly before this task) — no mutex guards
// any of this state, the parking lot included.
type Linker struct {
	ids      IDSource
	fetches  *vxidCache[fetchInfo]
	restarts *vxidCache[restartInfo]

	// OnCacheMiss, if set, is called synchronously whenever a correlation
	// genuinely cannot be resolved — a restart continuation whose original
	// was never seen or has aged out, or a hit's parked group released
	// WITHOUT ever resolving (age-out, parking-lot overflow eviction, or
	// Flush at shutdown). It is never called for the mere act of parking a
	// hit (see OnHitParked) — only for a release that ends in degradation.
	// cmd/tracer wires this to the vinyl_tracer_link_cache_misses_total
	// counter; the spans package itself stays free of a metrics dependency,
	// matching how BuildWithOutcome's Outcome return already lets the
	// caller own its own counters instead of this package reaching for one
	// directly.
	OnCacheMiss func()

	// OnHitParked, if set, is called synchronously whenever a hit's group is
	// newly parked (its vxid did not resolve on its own Build call). This is
	// a distinct event from OnCacheMiss: most parked sets go on to resolve
	// normally a Build call or two later, so parking itself is not a miss —
	// only a release that never resolved counts there. cmd/tracer wires this
	// to vinyl_tracer_hits_parked_total, purely for observability into how
	// often the race the parking lot exists for actually happens.
	OnHitParked func()

	maxParked int
	ageLimit  time.Duration
	// nowFunc is the wall clock the parking lot's age bookkeeping reads.
	// Set-once (to time.Now) at construction in production; tests override
	// it directly before driving the Linker, following
	// internal/controller/debounce.go's existing clock-seam pattern.
	nowFunc func() time.Time

	// parked holds every span set currently withheld from its own Build
	// call's return, oldest-arrived first (parking always appends; overflow
	// and age-out both remove from the front first) — see parkedSet's doc.
	parked []*parkedSet
}

// NewLinker returns a Linker whose fetch-identity and restart-identity
// caches each hold up to capacity entries for up to ttl, and whose parking
// lot uses the package's default bounds (defaultMaxParked,
// defaultParkAgeLimit).
func NewLinker(capacity int, ttl time.Duration, ids IDSource) *Linker {
	return &Linker{
		ids:       ids,
		fetches:   newVxidCache[fetchInfo](capacity, ttl),
		restarts:  newVxidCache[restartInfo](capacity, ttl),
		maxParked: defaultMaxParked,
		ageLimit:  defaultParkAgeLimit,
		nowFunc:   time.Now,
	}
}

// Build wraps BuildWithOutcome, then layers cross-transaction correlation
// onto the result:
//
//  1. Age-out housekeeping: release, degraded, every parked span set (see
//     parkedSet's doc) that has waited longer than the Linker's ageLimit as
//     of now. This runs FIRST, on every Build call, before this call's own
//     group is even built — deliberately before step 2 below records this
//     group's own fetch identities, so a long-overstayed parked vxid can
//     never accidentally resolve against a same-numbered but unrelated
//     fetch that merely happens to arrive in the same Build call. This is a
//     real hazard, not a hypothetical one: Varnish mints vxids per-process
//     starting from a small number, so two independent varnishd
//     lifetimes/recordings routinely reuse the same vxid.
//  2. Restart continuation: if tx is a top-level Request whose own "Begin"
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
//  3. Record every fetch span just built (Name == "varnish fetch") into the
//     fetch-identity cache, keyed by its originating BeReq's own VXID.
//  4. Record this tx's own "Link req <vxid> restart" record(s), if any,
//     into the restart-identity cache, keyed by the named child vxid, so a
//     later top-level Tx with that VXID can resolve step 2.
//  5. Resolve any still-parked sets whose vxid is now cached (having just
//     been recorded in step 3, by this call or an earlier one): attach the
//     link + overlap-rule coalesced flag exactly as step 6 below would have
//     at the parked set's OWN original Build call, and release it — meaning
//     Build's returned []Span can contain spans from an EARLIER group,
//     resolved here rather than at their own Build call. Callers that
//     enqueue/export every returned span (cmd/tracer does) handle this
//     transparently; callers that assume one Build call == one group's
//     worth of spans do not exist today, but should not be added without
//     accounting for this.
//  6. Resolve this tx's own "Hit <vxid> ..." record, if any, against the
//     fetch-identity cache: a hit adds a Link to its origin fetch
//     (varnish.link="origin-fetch"), plus varnish.coalesced=true when the
//     hit's own Start precedes the cached fetch's End (the request
//     overlapped the still-in-flight fetch) — same as before. An
//     unresolvable vxid no longer degrades immediately: the whole group is
//     PARKED (withheld from this Build call's return, keyed by the
//     unresolved vxid, stamped with this call's now) for step 5 of a later
//     Build call to resolve, and OnHitParked fires. A parking-lot overflow
//     (already at maxParked) releases the single oldest parked set degraded
//     — today's varnish.coalesced_unlinked + OnCacheMiss — before parking
//     the new arrival, so the lot never grows unbounded and nothing is
//     dropped silently.
//
// Flush releases every still-parked set degraded; callers that stop pumping
// Build (shutdown) must call it so a parked set never seen again is still
// exported rather than lost.
func (l *Linker) Build(tx *vsl.Tx) ([]Span, Outcome) {
	now := l.nowFunc()

	var result []Span
	result = append(result, l.releaseAgedParked(now)...)

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
		return result, outcome
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

	result = append(result, l.resolveParked(now)...)

	overflow, parked := l.resolveHit(tx, built, now)
	result = append(result, overflow...)
	if parked {
		return result, outcome
	}
	return append(result, built...), outcome
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
// fetch-identity cache. When it resolves, built's top request span (index 0)
// is linked in place exactly as before and resolveHit reports parked=false.
// When it does not (yet) resolve, the whole group is parked instead (see
// park) and resolveHit reports parked=true, so Build withholds built from
// its own return; overflow carries any OLDER parked set a capacity eviction
// released degraded while making room, which Build must still return rather
// than drop.
func (l *Linker) resolveHit(tx *vsl.Tx, built []Span, now time.Time) (overflow []Span, parked bool) {
	raw, ok := tx.First("Hit")
	if !ok {
		return nil, false
	}
	f := strings.Fields(raw)
	if len(f) == 0 {
		return nil, false
	}
	vxid, err := strconv.ParseUint(f[0], 10, 64)
	if err != nil {
		return nil, false
	}
	fetch, ok := l.fetches.get(vxid, now)
	if !ok {
		return l.park(vxid, built, now), true
	}
	linkHit(&built[0], fetch)
	return nil, false
}

// linkHit attaches a Link to fetch on req, plus the coalesced flag when
// req's own Start precedes fetch's End (the request overlapped the
// still-in-flight fetch) — the shared overlap rule used both for a hit that
// resolves immediately and for a parked set resolved later (resolveParked).
func linkHit(req *Span, fetch fetchInfo) {
	req.Links = append(req.Links, Link{
		TraceID: fetch.traceID,
		SpanID:  fetch.spanID,
		Attrs:   []attribute.KeyValue{attribute.String("varnish.link", "origin-fetch")},
	})
	if req.Start.Before(fetch.end) {
		req.Attrs = append(req.Attrs, attribute.Bool("varnish.coalesced", true))
	}
}

// park withholds built (a just-built top-level Request group whose own Hit
// vxid did not resolve) from its Build call's return, keyed under vxid, so
// a later Build call's resolveParked gets a bounded chance to resolve it.
// Overflow — the lot already at maxParked — first releases the single
// oldest parked set degraded (today's varnish.coalesced_unlinked +
// OnCacheMiss), exactly like an age-out would, rather than either dropping
// the new arrival or letting the lot grow unbounded; that released set is
// returned so the caller (Build) can still emit it.
func (l *Linker) park(vxid uint64, built []Span, now time.Time) []Span {
	var overflow []Span
	if len(l.parked) >= l.maxParked {
		oldest := l.parked[0]
		l.parked = l.parked[1:]
		overflow = l.degradeRelease(oldest)
	}
	l.parked = append(l.parked, &parkedSet{vxid: vxid, spans: built, arrivedAt: now})
	l.hitParked()
	return overflow
}

// releaseAgedParked releases, degraded, every parked set that has waited
// longer than the Linker's ageLimit as of now. See Build's doc comment for
// why this runs before anything else in Build.
func (l *Linker) releaseAgedParked(now time.Time) []Span {
	var out []Span
	var kept []*parkedSet
	for _, p := range l.parked {
		if now.Sub(p.arrivedAt) > l.ageLimit {
			out = append(out, l.degradeRelease(p)...)
		} else {
			kept = append(kept, p)
		}
	}
	l.parked = kept
	return out
}

// resolveParked releases, linked, every remaining parked set whose vxid is
// now present in the fetch-identity cache — the other half of resolving
// both VSL group-emission orderings: a waiter-first hit is returned from
// whichever LATER Build call happens to record its initiator's fetch
// identity, carrying the same link + overlap-rule coalesced flag it would
// have gotten had the orderings been reversed.
func (l *Linker) resolveParked(now time.Time) []Span {
	var out []Span
	var kept []*parkedSet
	for _, p := range l.parked {
		if fetch, ok := l.fetches.get(p.vxid, now); ok {
			linkHit(&p.spans[0], fetch)
			out = append(out, p.spans...)
		} else {
			kept = append(kept, p)
		}
	}
	l.parked = kept
	return out
}

// degradeRelease applies today's degradation (varnish.coalesced_unlinked=
// true on the top request span) to p's span set and counts it as a
// link-cache miss — shared by age-out, parking-lot overflow eviction, and
// Flush, the three ways a parked set can be released WITHOUT ever
// resolving.
func (l *Linker) degradeRelease(p *parkedSet) []Span {
	p.spans[0].Attrs = append(p.spans[0].Attrs, attribute.Bool("varnish.coalesced_unlinked", true))
	l.miss()
	return p.spans
}

// Flush releases every still-parked span set, degraded, and empties the
// parking lot. Callers that stop driving Build (graceful shutdown) must
// call this once afterward so a parked set that never got its resolving
// Build call is still exported instead of silently lost — cmd/tracer wires
// this in after its supervisor's run loop returns, before waiting for the
// export pipeline to drain.
func (l *Linker) Flush() []Span {
	out := make([]Span, 0, len(l.parked))
	for _, p := range l.parked {
		out = append(out, l.degradeRelease(p)...)
	}
	l.parked = nil
	return out
}

func (l *Linker) miss() {
	if l.OnCacheMiss != nil {
		l.OnCacheMiss()
	}
}

func (l *Linker) hitParked() {
	if l.OnHitParked != nil {
		l.OnHitParked()
	}
}
