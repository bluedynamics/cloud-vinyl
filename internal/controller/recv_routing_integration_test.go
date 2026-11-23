//go:build integration

package controller

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	vinylv1 "github.com/bluedynamics/cloud-vinyl/api/v1"
	"github.com/bluedynamics/cloud-vinyl/internal/generator"
)

// echoOriginVCL makes varnishd an origin that names itself and echoes the
// method, the URL and the x-routing-runs mark of the request it received.
func echoOriginVCL(name string) string {
	return fmt.Sprintf(`vcl 4.1;
backend default none;
sub vcl_recv { return (synth(200)); }
sub vcl_synth {
    set resp.http.x-origin = "%s";
    set resp.http.x-method = req.method;
    set resp.http.x-url = req.url;
    set resp.http.x-routing-runs = req.http.x-routing-runs;
    set resp.body = "ok";
    return (deliver);
}
`, name)
}

// apiRouting is the Plone/Volto split of #195: the Plone REST API goes to the
// plone backend with a VirtualHostMonster rewrite, everything else stays on
// the first backend, volto, which the default backend hint picks.
const apiRouting = `if (req.url ~ "^/\+\+api\+\+") {
        set req.backend_hint = plone.backend();
        set req.url = "/VirtualHostBase/http/" + req.http.host + "/Plone/VirtualHostRoot" + req.url;
    }`

// countRuns marks the request each time the routing snippet runs. The relay
// hop forwards request headers to the owner hop, so a snippet that ran on
// both hops would reach the origin marked "xx".
const countRuns = `
    if (req.http.x-routing-runs) {
        set req.http.x-routing-runs = req.http.x-routing-runs + "x";
    } else {
        set req.http.x-routing-runs = "x";
    }`

// rewrittenLogin is what the plone origin must see for /++api++/@login. The
// generated vcl_recv strips the port from the Host before any snippet runs.
const rewrittenLogin = "/VirtualHostBase/http/127.0.0.1/Plone/VirtualHostRoot/++api++/@login"

// routingInput builds the two-backend cache of the Plone/Volto case. With
// cluster set, the cache is its own only peer, so every request takes the
// relay hop and the owner hop through the same varnishd (see #150's test).
func routingInput(t *testing.T, snippets vinylv1.VCLSnippets, voltoIP, ploneIP, cacheIP string, cluster bool) generator.Input {
	t.Helper()
	spec := &vinylv1.VinylCacheSpec{Backends: []vinylv1.BackendSpec{
		{Name: "volto", ServiceRef: vinylv1.ServiceRef{Name: "volto"}},
		{Name: "plone", ServiceRef: vinylv1.ServiceRef{Name: "plone"}},
	}}
	spec.VCL.Snippets = snippets
	input := generator.Input{
		Name: "routing", Namespace: "test", Spec: spec,
		Endpoints: map[string][]generator.Endpoint{
			"volto": {{IP: voltoIP, Port: 80}},
			"plone": {{IP: ploneIP, Port: 80}},
		},
	}
	if cluster {
		spec.Cluster = vinylv1.ClusterSpec{Enabled: true}
		input.Peers = []generator.PeerBackend{{Name: "routing_0", IP: cacheIP, Port: varnishPort}}
	}
	return input
}

// TestRecvRoutingSnippetRoutesEveryMethod guards #195 with a real varnishd:
// a vclRecvRouting snippet picks the backend and rewrites the URL for writes
// as well as reads. Clustered, it must run exactly once, on the owner hop:
// on the relay hop its backend choice would be lost to the cluster block.
func TestRecvRoutingSnippetRoutesEveryMethod(t *testing.T) {
	image := testVarnishImage(t)
	voltoIP := containerIP(t, startOriginVCL(t, image, echoOriginVCL("volto")))
	ploneIP := containerIP(t, startOriginVCL(t, image, echoOriginVCL("plone")))

	for _, cluster := range []bool{false, true} {
		t.Run(fmt.Sprintf("cluster=%t", cluster), func(t *testing.T) {
			id := startOperatorVarnishd(t, image, &vinylv1.VinylCache{})
			input := routingInput(t, vinylv1.VCLSnippets{VCLRecvRouting: apiRouting + countRuns},
				voltoIP, ploneIP, containerIP(t, id), cluster)
			res, err := generator.New().Generate(input)
			require.NoError(t, err)
			loadVCL(t, id, res.VCL)

			for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodGet} {
				h := curlRequest(t, id, method, "http://127.0.0.1:8080/++api++/@login")
				assert.Equal(t, "plone", h.Get("x-origin"), "%s must reach the routed backend", method)
				assert.Equal(t, method, h.Get("x-method"))
				assert.Equal(t, rewrittenLogin, h.Get("x-url"), "%s must be rewritten", method)
				assert.Equal(t, "x", h.Get("x-routing-runs"), "%s: the snippet must run exactly once", method)
			}

			other := curlRequest(t, id, http.MethodPost, "http://127.0.0.1:8080/contact-form")
			assert.Equal(t, "volto", other.Get("x-origin"), "unrouted requests keep the default backend")
			assert.Equal(t, "/contact-form", other.Get("x-url"))
		})
	}
}

// TestRecvSnippetSeesOnlyGetAndHead reproduces what #195 reported and pins
// why vclRecv was left as it is: the same routing in vclRecv still applies to
// GET only, while a POST is passed to the first backend before the snippet
// runs. Existing vclRecv snippets may rely on seeing GET and HEAD only.
func TestRecvSnippetSeesOnlyGetAndHead(t *testing.T) {
	image := testVarnishImage(t)
	voltoIP := containerIP(t, startOriginVCL(t, image, echoOriginVCL("volto")))
	ploneIP := containerIP(t, startOriginVCL(t, image, echoOriginVCL("plone")))
	id := startOperatorVarnishd(t, image, &vinylv1.VinylCache{})
	input := routingInput(t, vinylv1.VCLSnippets{VCLRecv: apiRouting}, voltoIP, ploneIP, "", false)
	res, err := generator.New().Generate(input)
	require.NoError(t, err)
	loadVCL(t, id, res.VCL)

	get := curlRequest(t, id, http.MethodGet, "http://127.0.0.1:8080/++api++/@login")
	assert.Equal(t, "plone", get.Get("x-origin"))
	assert.Equal(t, rewrittenLogin, get.Get("x-url"))

	post := curlRequest(t, id, http.MethodPost, "http://127.0.0.1:8080/++api++/@login")
	assert.Equal(t, "volto", post.Get("x-origin"), "vclRecv never sees a POST")
	assert.Equal(t, "/++api++/@login", post.Get("x-url"))
}
