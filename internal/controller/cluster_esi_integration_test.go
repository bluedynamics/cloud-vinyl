//go:build integration

package controller

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	vinylv1 "github.com/bluedynamics/cloud-vinyl/api/v1"
	"github.com/bluedynamics/cloud-vinyl/internal/generator"
)

const esiFragments = 16

// esiOriginVCL serves /page, which includes esiFragments fragments, and every
// other URL as a small cacheable fragment.
func esiOriginVCL() string {
	var includes strings.Builder
	for i := 0; i < esiFragments; i++ {
		fmt.Fprintf(&includes, `<esi:include src="/frag%d"/>`, i)
	}
	return fmt.Sprintf(`vcl 4.1;
backend default none;
sub vcl_recv { return (synth(200)); }
sub vcl_synth {
    set resp.http.Cache-Control = "max-age=300";
    if (req.url == "/page") {
        set resp.http.Surrogate-Control = {"content="ESI/1.0""};
        set resp.body = {"<html>%s</html>"};
    } else {
        set resp.body = "fragment " + req.url;
    }
    return (deliver);
}
`, includes.String())
}

// TestClusterESIFragmentsRouteToOwnShard guards #156 with two real varnishd
// pods that are each other's peers. A page is requested from one pod. Its
// owner expands the ESI includes, and every fragment must be routed to the
// fragment's own shard owner and cached there, instead of being cached on the
// page's owner because the subrequest inherited X-Vinyl-Shard from its parent.
//
// Without the fix about half of the fragments live on the page's owner. They
// are fetched from the origin a second time when a client asks the pod for the
// fragment directly, which routes it to its real owner.
func TestClusterESIFragmentsRouteToOwnShard(t *testing.T) {
	image := testVarnishImage(t)
	originID := startOriginVCL(t, image, esiOriginVCL())
	originIP := containerIP(t, originID)

	podA := startOperatorVarnishd(t, image, &vinylv1.VinylCache{})
	podB := startOperatorVarnishd(t, image, &vinylv1.VinylCache{})
	peers := []generator.PeerBackend{
		{Name: "esi_0", IP: containerIP(t, podA), Port: varnishPort},
		{Name: "esi_1", IP: containerIP(t, podB), Port: varnishPort},
	}
	spec := &vinylv1.VinylCacheSpec{
		Backends: []vinylv1.BackendSpec{{Name: "origin", ServiceRef: vinylv1.ServiceRef{Name: "origin"}}},
		Cluster:  vinylv1.ClusterSpec{Enabled: true},
	}
	spec.ESI.Enabled = true
	res, err := generator.New().Generate(generator.Input{
		Name:      "esi",
		Namespace: "test",
		Spec:      spec,
		Endpoints: map[string][]generator.Endpoint{"origin": {{IP: originIP, Port: 80}}},
		Peers:     peers,
	})
	require.NoError(t, err)
	loadVCL(t, podA, res.VCL)
	loadVCL(t, podB, res.VCL)

	body, err := docker("exec", podA, "curl", "-s", "http://127.0.0.1:8080/page").Output()
	require.NoError(t, err)
	for i := 0; i < esiFragments; i++ {
		require.Contains(t, string(body), fmt.Sprintf("fragment /frag%d", i),
			"the page must come back with every fragment expanded")
	}

	// A client asking for a fragment is routed to the fragment's owner. If
	// the owner already holds it, the origin sees no second request.
	for i := 0; i < esiFragments; i++ {
		_, err := docker("exec", podA, "curl", "-s", "-o", "/dev/null",
			fmt.Sprintf("http://127.0.0.1:8080/frag%d", i)).Output()
		require.NoError(t, err)
	}
	time.Sleep(time.Second)

	for i := 0; i < esiFragments; i++ {
		url := fmt.Sprintf("/frag%d", i)
		assert.Equal(t, 1, originRequests(t, originID, url),
			"%s must be fetched from the origin once, by its own shard owner", url)
	}
	assert.Equal(t, 1, originRequests(t, originID, "/page"))
}

// originRequests counts the requests the origin varnishd has logged for url.
func originRequests(t *testing.T, id, url string) int {
	t.Helper()
	out, err := docker("exec", id, "varnishlog", "-d", "-g", "request",
		"-i", "ReqURL", "-q", fmt.Sprintf(`ReqURL eq "%s"`, url)).Output()
	require.NoError(t, err, "varnishlog: %s", stderrText(err))
	return strings.Count(string(out), "ReqURL")
}
