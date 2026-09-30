package vsl

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func parseAll(t *testing.T, fixture string) []*Tx {
	t.Helper()
	f, err := os.Open("testdata/" + fixture)
	require.NoError(t, err)
	defer f.Close()
	return parseReader(t, f)
}

func parseReader(t *testing.T, r io.Reader) []*Tx {
	t.Helper()
	var out []*Tx
	p := NewParser(r)
	for {
		tx, err := p.Next()
		if err != nil {
			break
		}
		out = append(out, tx)
	}
	return out
}

// txSummary is a comparable projection of *Tx (pointers make Tx itself
// unsuitable for assert.Equal) used to compare a parse of clean input
// against a parse of the same input with garbage lines injected.
type txSummary struct {
	Type     string
	VXID     uint64
	NRecords int
	Children []txSummary
}

func summarize(txs []*Tx) []txSummary {
	out := make([]txSummary, len(txs))
	for i, tx := range txs {
		out[i] = summarizeTx(tx)
	}
	return out
}

func summarizeTx(tx *Tx) txSummary {
	return txSummary{
		Type:     tx.Type,
		VXID:     tx.VXID,
		NRecords: len(tx.Records),
		Children: summarize(tx.Children),
	}
}

func TestParser_MissThenHit_GroupsAndNesting(t *testing.T) {
	txs := parseAll(t, "miss_then_hit.txt")
	require.Len(t, txs, 2)
	require.Equal(t, "Request", txs[0].Type)
	require.Len(t, txs[0].Children, 1, "miss must nest one BeReq")
	assert.Equal(t, "BeReq", txs[0].Children[0].Type)
	assert.Empty(t, txs[1].Children, "hit fetches nothing")
	assert.True(t, txs[1].Has("Hit"))
	assert.NotZero(t, txs[0].VXID)
}

func TestParser_TimestampsParse(t *testing.T) {
	txs := parseAll(t, "miss_then_hit.txt")
	start, ok := txs[0].Timestamp("Start")
	require.True(t, ok)
	resp, ok := txs[0].Timestamp("Resp")
	require.True(t, ok)
	assert.True(t, resp.After(start))
	_, ok = txs[0].Children[0].Timestamp("Bereq")
	assert.True(t, ok)
}

func TestParser_HeaderLookupCaseInsensitive(t *testing.T) {
	txs := parseAll(t, "miss_then_hit.txt")
	v, ok := txs[0].Header("ReqHeader", "TraceParent")
	require.True(t, ok)
	assert.Equal(t, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", v)
}

// TestHeader_LastMatchWinsOnRewrittenHeader proves Header's unset/re-add
// contract directly against the real fixture, not just indirectly through
// the spans package. rewrite_miss.txt's BeReq child genuinely carries three
// records for this header: the client's forwarded "Traceparent" (span id
// 00f067aa0ba902b7), a "BereqUnset" for it, and the VCL-minted lowercase
// "traceparent" re-add. If Header ever regresses to first-match, this test
// (not just spans_test.go's adoption tests) goes red.
func TestHeader_LastMatchWinsOnRewrittenHeader(t *testing.T) {
	txs := parseAll(t, "rewrite_miss.txt")
	require.NotEmpty(t, txs)
	require.NotEmpty(t, txs[0].Children, "fixture must nest a BeReq")
	bereq := txs[0].Children[0]
	require.Equal(t, "BeReq", bereq.Type)

	v, ok := bereq.Header("BereqHeader", "traceparent")
	require.True(t, ok)
	assert.Regexp(t, `^00-4bf92f3577b34da6a3ce929d0e0e4736-[0-9a-f]{16}-01$`, v,
		"still W3C-shaped, same trace id as the forwarded header")
	assert.False(t, strings.HasSuffix(v, "-00f067aa0ba902b7-01"),
		"must not be the forwarded (pre-rewrite) value's span id")

	// Corroboration: prove the multi-record situation is real in the
	// fixture, and that last-match actually discriminated between two
	// distinct records rather than there being only one candidate.
	var sawForwarded, sawReturned bool
	for _, r := range bereq.Records {
		if r.Tag != "BereqHeader" {
			continue
		}
		if strings.Contains(r.Payload, "00f067aa0ba902b7") {
			sawForwarded = true
		}
		if strings.Contains(r.Payload, v) {
			sawReturned = true
		}
	}
	assert.True(t, sawForwarded, "fixture must still carry the forwarded record")
	assert.True(t, sawReturned, "returned value must appear verbatim in a raw record")
}

// TestParser_GarbageLinesAreSkippedNotFatal injects synthetic garbage lines
// (never recorded in a real fixture) into a real fixture's content and
// asserts the parse is byte-for-byte identical, structurally, to parsing the
// pristine content. That is the only way to show garbage is *skipped*: a
// bare "parsing didn't error" assertion would pass identically even if the
// skip path were deleted, since none of the recorded fixtures contain a
// line outside the group/record/blank shapes.
func TestParser_GarbageLinesAreSkippedNotFatal(t *testing.T) {
	raw, err := os.ReadFile("testdata/miss_then_hit.txt")
	require.NoError(t, err)
	pristine := string(raw)

	// Inject one plain garbage line into the top-level Request group and one
	// truncation-style fragment into the nested BeReq group, so both nesting
	// depths are exercised. Neither line matches groupRe (no leading "*") or
	// recordRe (no leading "-"), so both must fall through to the "skipped,
	// never fatal" branch at vsl.go's end of Next's loop body.
	mutated := strings.Replace(pristine,
		"-   ReqMethod      GET\n",
		"-   ReqMethod      GET\nthis is not a VSL line\n",
		1)
	mutated = strings.Replace(mutated,
		"--  BereqMethod    GET\n",
		"--  BereqMethod    GET\n[...12 lines suppressed...]\n",
		1)
	require.NotEqual(t, pristine, mutated, "test bug: garbage injection did not change the input")

	want := summarize(parseReader(t, strings.NewReader(pristine)))
	got := summarize(parseReader(t, strings.NewReader(mutated)))
	assert.Equal(t, want, got, "garbage lines must be skipped without disturbing parsed groups or records")
}
