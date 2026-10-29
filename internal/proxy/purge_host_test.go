package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingBroadcaster records every broadcast; one purge can fan out to
// several hosts. Each pod reports one purged object.
type recordingBroadcaster struct {
	reqs []BroadcastRequest
}

func (b *recordingBroadcaster) Broadcast(_ context.Context, pods []string, req BroadcastRequest) BroadcastResult {
	b.reqs = append(b.reqs, req)
	results := make([]PodResult, len(pods))
	for i, p := range pods {
		results[i] = PodResult{Pod: p, Status: 200, ObjectsPurged: new(1)}
	}
	return BroadcastResult{
		Status: "ok", Total: len(pods), Succeeded: len(pods),
		ObjectsPurged: aggregateObjectsPurged(results), Results: results,
	}
}

// purgeServer serves cache app/my-cache on two pods, with optional
// spec.invalidation.purge.hosts.
func purgeServer(t *testing.T, specHosts ...string) (*Server, *recordingBroadcaster) {
	t.Helper()
	router := NewRegisteredRouter()
	router.Register("app", "my-cache")
	router.SetPurgeHosts("app", "my-cache", specHosts)
	pm := NewPodMap()
	pm.Update("app", "my-cache", []string{"10.0.0.1", "10.0.0.2"})
	b := &recordingBroadcaster{}
	return NewServer(":8090", router, pm, b, nil), b
}

func purge(t *testing.T, srv *Server, purgeHost string) (*httptest.ResponseRecorder, BroadcastResult) {
	t.Helper()
	req := httptest.NewRequest("PURGE", "/some/page", nil)
	req.Host = "my-cache-invalidation.app:8090"
	if purgeHost != "" {
		req.Header.Set(purgeHostHeader, purgeHost)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	var res BroadcastResult
	if rec.Code < 400 {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	}
	return rec, res
}

func hostsOf(reqs []BroadcastRequest) []string {
	out := make([]string, len(reqs))
	for i, r := range reqs {
		out[i] = r.Host
	}
	return out
}

// Varnish keys objects by URL and site host. The proxy is reached through the
// invalidation host, so forwarding that host purged nothing (#134).
func TestPurge_HeaderNamesTheSiteHost(t *testing.T) {
	srv, b := purgeServer(t, "spec.example")
	rec, res := purge(t, srv, "example.org")

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []string{"example.org"}, hostsOf(b.reqs), "the header wins over purge.hosts")
	_, forwarded := b.reqs[0].Headers[purgeHostHeader]
	assert.False(t, forwarded, "the routing hint is not forwarded to varnishd")
	assert.Empty(t, res.Warning)
	assert.Equal(t, 2, *res.ObjectsPurged)
}

func TestPurge_HeaderListFansOutAndMerges(t *testing.T) {
	srv, b := purgeServer(t)
	_, res := purge(t, srv, "example.org, www.example.org,example.org")

	assert.Equal(t, []string{"example.org", "www.example.org"}, hostsOf(b.reqs), "trimmed and de-duplicated")
	assert.Equal(t, "ok", res.Status)
	assert.Equal(t, 4, res.Total, "two hosts on two pods")
	assert.Equal(t, 4, res.Succeeded)
	assert.Equal(t, 4, *res.ObjectsPurged)
	require.Len(t, res.Results, 4)
	assert.Equal(t, "example.org", res.Results[0].Host)
	assert.Equal(t, "www.example.org", res.Results[3].Host)
}

// plone.cachepurging cannot set headers; spec.invalidation.purge.hosts covers it.
func TestPurge_SpecHostsWithoutHeader(t *testing.T) {
	srv, b := purgeServer(t, "example.org", "www.example.org")
	_, res := purge(t, srv, "")

	assert.Equal(t, []string{"example.org", "www.example.org"}, hostsOf(b.reqs))
	assert.Empty(t, res.Warning)
}

func TestPurge_NoSiteHostFallsBackWithWarning(t *testing.T) {
	srv, b := purgeServer(t)
	rec, res := purge(t, srv, "")

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []string{"my-cache-invalidation.app:8090"}, hostsOf(b.reqs),
		"without a site host the old forwarding stays, so nothing that worked breaks")
	assert.Contains(t, res.Warning, "no site host given")
}

func TestPurge_RejectsBadHostHeader(t *testing.T) {
	srv, b := purgeServer(t)

	rec, _ := purge(t, srv, "example.org, bad host")
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	many := make([]string, maxPurgeHosts+1)
	for i := range many {
		many[i] = "h" + strings.Repeat("x", i) + ".example"
	}
	rec, _ = purge(t, srv, strings.Join(many, ","))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "at most 16")

	assert.Empty(t, b.reqs, "a rejected request broadcasts nothing")
}
