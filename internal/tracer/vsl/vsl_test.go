package vsl

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func parseAll(t *testing.T, fixture string) []*Tx {
	t.Helper()
	f, err := os.Open("testdata/" + fixture)
	require.NoError(t, err)
	defer f.Close()
	var out []*Tx
	p := NewParser(f)
	for {
		tx, err := p.Next()
		if err != nil {
			break
		}
		out = append(out, tx)
	}
	return out
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

func TestParser_GarbageLinesAreSkippedNotFatal(t *testing.T) {
	txs := parseAll(t, "invalid_traceparent.txt")
	require.NotEmpty(t, txs)
}
