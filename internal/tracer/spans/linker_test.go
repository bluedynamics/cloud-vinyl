package spans

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bluedynamics/cloud-vinyl/internal/tracer/vsl"
)

// TestLinker_CoalescedHitLinksToOriginFetch is the brief's Step 1 test
// against coalesce.txt. File order (NOTES.md, corrected in 8afb10c): Request
// 2 — the initiator, containing "Link bereq 3 fetch" (line 23) — opens the
// file at line 1; Request 32770 — the waiter, containing "Hit 3 ..." (line
// 104) — follows after the blank line at 83. fixtureTxs returns
// [initiator, waiter] in that same order, meaning the fetch is cached
// before the waiter's Build call resolves its Hit — no pending-hits buffer
// is needed for this ordering.
func TestLinker_CoalescedHitLinksToOriginFetch(t *testing.T) {
	txs := fixtureTxs(t, "coalesce.txt")
	require.Len(t, txs, 2)
	l := NewLinker(1024, time.Minute, &seqIDs{})
	var all []Span
	for _, tx := range txs {
		s, _ := l.Build(tx)
		all = append(all, s...)
	}
	fetches := spansNamed(all, "varnish fetch")
	require.Len(t, fetches, 1, "coalescing: one fetch for two requests")
	reqs := spansNamed(all, "varnish request")
	require.Len(t, reqs, 2)
	var waiter *Span
	for i := range reqs {
		if attrHas(reqs[i].Attrs, "varnish.coalesced") {
			waiter = &reqs[i]
		}
	}
	require.NotNil(t, waiter, "one request waited on the in-flight fetch")
	require.Len(t, waiter.Links, 1)
	assert.Equal(t, fetches[0].SpanID, waiter.Links[0].SpanID)
	assert.Equal(t, fetches[0].TraceID, waiter.Links[0].TraceID)
	assert.Equal(t, "origin-fetch", attrString(t, waiter.Links[0].Attrs, "varnish.link"))
}

// TestLinker_PlainLaterHitLinksWithoutCoalescedFlag is the brief's second
// Step 1 test: miss_then_hit.txt's hit arrives well after the fetch that
// created the object completed, so every hit still links to its origin
// fetch but must not carry the coalesced flag.
func TestLinker_PlainLaterHitLinksWithoutCoalescedFlag(t *testing.T) {
	txs := fixtureTxs(t, "miss_then_hit.txt")
	l := NewLinker(1024, time.Minute, &seqIDs{})
	var all []Span
	for _, tx := range txs {
		s, _ := l.Build(tx)
		all = append(all, s...)
	}
	reqs := spansNamed(all, "varnish request")
	require.Len(t, reqs, 2)
	hit := reqs[1]
	require.Len(t, hit.Links, 1, "every hit links to its origin fetch")
	assert.False(t, attrHas(hit.Attrs, "varnish.coalesced"),
		"a hit after the fetch completed did not coalesce")
}

// TestLinker_GraceHitLinksToOriginFetchWithoutCoalescedFlag: grace_bgfetch.txt
// NOTES.md — the stale hit's own "Hit 3 -1.690501 3600.000000 0.000000"
// record (4-field form) still names the PRIMING fetch's vxid (3), not the
// triggered background fetch's (32771). The coalescing contract is
// unconditional ("every hit links to its origin fetch"), so the grace hit
// must link to the priming fetch span too, well after it completed — no
// coalesced flag — while its own bgfetch (32771) is independently cached as
// a fetch identity (unused by this fixture's own Hit, but proving the 4-field
// Hit payload parses and resolves same as the 6-field form).
func TestLinker_GraceHitLinksToOriginFetchWithoutCoalescedFlag(t *testing.T) {
	txs := fixtureTxs(t, "grace_bgfetch.txt")
	require.Len(t, txs, 2)
	l := NewLinker(1024, time.Minute, &seqIDs{})
	var all []Span
	for _, tx := range txs {
		s, _ := l.Build(tx)
		all = append(all, s...)
	}
	reqs := spansNamed(all, "varnish request")
	require.Len(t, reqs, 2)
	staleHit := reqs[1]
	require.Len(t, staleHit.Links, 1, "the grace hit still links to the priming fetch")
	assert.False(t, attrHas(staleHit.Attrs, "varnish.coalesced"),
		"a stale/grace hit long after the priming fetch completed did not coalesce")
	primingFetch := spansNamed(all, "varnish fetch")[0]
	assert.Equal(t, primingFetch.SpanID, staleHit.Links[0].SpanID)
}

// TestLinker_UnresolvedHitFlagsDegradationAndCountsMiss: build only the
// waiter's group from coalesce.txt, never having seen the initiator's fetch
// — the fetch-identity cache is empty, so the Hit vxid cannot resolve. The
// brief's degradation contract: varnish.coalesced_unlinked=true plus the
// link-cache-misses counter, instead of a silent no-op.
func TestLinker_UnresolvedHitFlagsDegradationAndCountsMiss(t *testing.T) {
	txs := fixtureTxs(t, "coalesce.txt")
	require.Len(t, txs, 2)
	l := NewLinker(1024, time.Minute, &seqIDs{})
	misses := 0
	l.OnCacheMiss = func() { misses++ }

	// txs[1] is the waiter (Hit 3 ...); build it in isolation.
	got, _ := l.Build(txs[1])
	require.Len(t, got, 1)
	req := got[0]
	assert.Empty(t, req.Links, "nothing to link to")
	assert.True(t, attrHas(req.Attrs, "varnish.coalesced_unlinked"))
	assert.False(t, attrHas(req.Attrs, "varnish.coalesced"))
	assert.Equal(t, 1, misses, "an unresolved Hit vxid must count as a link-cache miss")
}

// TestLinker_RestartContinuationParentsUnderOriginalAndSharesTraceID proves
// the controller-ruled restart extension against restart.txt's two groups:
// the original (txs[0], carrying "Link req 4 restart") and the restarted
// continuation (txs[1], top-level Tx vxid 4, "Begin req 2 restart"). Built
// in file order — original first — the continuation's request span must
// parent under the original's request span and share its trace id, per
// NOTES.md's "Restarts: Child spans under the original request span" (a
// logical truth the raw grouping does not hand you: Request 4 prints at the
// SAME "*" depth as Request 2, not nested under it — see NOTES.md's
// "Restart's depth is a trap").
func TestLinker_RestartContinuationParentsUnderOriginalAndSharesTraceID(t *testing.T) {
	txs := fixtureTxs(t, "restart.txt")
	require.Len(t, txs, 2, "original and restarted request are two top-level Tx's")
	l := NewLinker(1024, time.Minute, &seqIDs{})

	origSpans, _ := l.Build(txs[0])
	require.NotEmpty(t, origSpans)
	orig := origSpans[0]

	contSpans, _ := l.Build(txs[1])
	require.NotEmpty(t, contSpans)
	cont := contSpans[0]

	assert.Equal(t, orig.SpanID, cont.ParentID,
		"the restarted request must parent under the original request span")
	assert.Equal(t, orig.TraceID, cont.TraceID,
		"the restarted request must land in the original's trace, not self-root")
	assert.Equal(t, "true", attrString(t, cont.Attrs, "varnish.restart_continuation"))

	// The continuation's own Hit (line 96: "Hit 3 60.000772 3600.000000
	// 0.000000") resolves against the SAME priming fetch (vxid 3) the
	// original request cached — the hit-linking contract composes with
	// restart stitching rather than being suppressed by it.
	require.Len(t, cont.Links, 1)
	fetches := spansNamed(origSpans, "varnish fetch")
	require.Len(t, fetches, 1)
	assert.Equal(t, fetches[0].SpanID, cont.Links[0].SpanID)
}

// TestLinker_RestartContinuationCacheMissSelfRoots: build the restarted
// continuation in isolation — the linker never saw the original's "Link req
// 4 restart" record, so the restart-identity cache is empty. Degradation
// must match Build's existing self-rooting (a fresh root span, zero
// ParentID) plus the cache-miss counter, not a crash or silent drop. The
// continuation's own "Hit 3 ..." record is stripped in memory first so this
// test isolates the restart-identity miss alone; left in place it would
// ALSO miss on the (equally unseeded) fetch-identity cache and count twice
// — a real, correct second degradation, just not what this test is about.
func TestLinker_RestartContinuationCacheMissSelfRoots(t *testing.T) {
	txs := fixtureTxs(t, "restart.txt")
	require.Len(t, txs, 2)
	var stripped []vsl.Record
	for _, r := range txs[1].Records {
		if r.Tag == "Hit" {
			continue
		}
		stripped = append(stripped, r)
	}
	txs[1].Records = stripped

	l := NewLinker(1024, time.Minute, &seqIDs{})
	misses := 0
	l.OnCacheMiss = func() { misses++ }

	got, _ := l.Build(txs[1])
	require.NotEmpty(t, got)
	cont := got[0]
	assert.Zero(t, cont.ParentID, "a cache miss must self-root exactly as Build does today")
	assert.False(t, attrHas(cont.Attrs, "varnish.restart_continuation"))
	assert.Equal(t, 1, misses)
}

// TestLinker_RestartMissNotCountedWhenGroupUnusable: a synthetic top-level
// Request tx whose Begin record marks it a restart continuation (so
// isRestartContinuation(tx) is true) but which is missing the Start
// timestamp Build needs — BuildWithOutcome must classify it OutcomeUnusable
// and return zero spans, never reaching the point where a restart-identity
// lookup would mean anything. Purity contract: the link-cache-misses
// counter tracks failed CORRELATIONS against a group that actually built,
// not every empty result — an unusable (or unsampled) group is a different,
// already-counted kind of loss (BuildWithOutcome's own Outcome), and must
// not also inflate the restart-linking miss counter. Before the fix, the
// early len(built)==0 branch in Build ran the restart-cache check
// unconditionally whenever isRestartContinuation(tx) was true, regardless
// of why built came back empty, over-counting this case.
func TestLinker_RestartMissNotCountedWhenGroupUnusable(t *testing.T) {
	tx := &vsl.Tx{
		Type: "Request",
		VXID: 4,
		Records: []vsl.Record{
			{Tag: "Begin", Payload: "req 2 restart"},
			// Deliberately no "Timestamp Start:" record, so respEnd/Start
			// resolution fails and BuildWithOutcome returns OutcomeUnusable.
		},
	}
	l := NewLinker(1024, time.Minute, &seqIDs{})
	misses := 0
	l.OnCacheMiss = func() { misses++ }

	got, outcome := l.Build(tx)
	require.Empty(t, got)
	assert.Equal(t, OutcomeUnusable, outcome)
	assert.Equal(t, 0, misses,
		"an unusable group must not also count as a restart-link-cache miss")
}

// TestVxidCache_EvictsOldestBeyondCapacity: the bounded-LRU contract — once
// capacity is exceeded, the OLDEST inserted entry (not the most recently
// used one; this cache is insertion-ordered, not access-ordered, per the
// brief's "map + ring of vxids, evict oldest beyond capacity") is dropped
// first.
func TestVxidCache_EvictsOldestBeyondCapacity(t *testing.T) {
	c := newVxidCache[int](2, time.Hour)
	now := time.Now()
	c.set(1, 100, now)
	c.set(2, 200, now)
	c.set(3, 300, now) // exceeds capacity 2: vxid 1 must be evicted

	_, ok := c.get(1, now)
	assert.False(t, ok, "oldest entry must be evicted")
	v2, ok := c.get(2, now)
	assert.True(t, ok)
	assert.Equal(t, 200, v2)
	v3, ok := c.get(3, now)
	assert.True(t, ok)
	assert.Equal(t, 300, v3)
}

// TestVxidCache_DropsExpiredEntriesOnInsert: an entry older than ttl is
// treated as absent once enough wall-clock time has passed, even without
// exceeding capacity — the brief's "drop entries older than ttl on insert".
func TestVxidCache_DropsExpiredEntriesOnInsert(t *testing.T) {
	c := newVxidCache[int](1024, time.Minute)
	base := time.Now()
	c.set(1, 100, base)

	_, ok := c.get(1, base.Add(2*time.Minute))
	assert.False(t, ok, "a get past ttl must report absent")

	// A later set purges the now-expired entry from the ring too (not just
	// masking it in get), so the ring cannot grow unbounded under a steady
	// stream of distinct vxids.
	c.set(2, 200, base.Add(2*time.Minute))
	assert.Len(t, c.order, 1, "the expired vxid-1 entry must be purged from the ring on insert")
}
