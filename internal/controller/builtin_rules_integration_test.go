//go:build integration

package controller

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	vinylv1 "github.com/bluedynamics/cloud-vinyl/api/v1"
	"github.com/bluedynamics/cloud-vinyl/internal/generator"
)

// cacheRulesOriginVCL answers every request with Cache-Control: max-age=300,
// plus the header that the path names. Without the built-in rules every one
// of these responses would be cached for five minutes.
const cacheRulesOriginVCL = `vcl 4.1;
backend default none;
sub vcl_recv { return (synth(200)); }
sub vcl_synth {
    set resp.http.Cache-Control = "max-age=300";
    if (req.url ~ "^/set-cookie") { set resp.http.Set-Cookie = "session=1"; }
    if (req.url ~ "^/private") { set resp.http.Cache-Control = "private, max-age=300"; }
    if (req.url ~ "^/no-store") { set resp.http.Cache-Control = "no-store, max-age=300"; }
    if (req.url ~ "^/no-cache") { set resp.http.Cache-Control = "no-cache, max-age=300"; }
    if (req.url ~ "^/vary-star") { set resp.http.Vary = "*"; }
    if (req.url ~ "^/surrogate-no-store") { set resp.http.Surrogate-Control = "no-store"; }
    set resp.body = "ok";
    return (deliver);
}
`

// curlWithHeader sends a GET with one extra request header from inside the
// container and returns the response headers.
func curlWithHeader(t *testing.T, id, url, header string) headers {
	t.Helper()
	args := []string{"exec", id, "curl", "-s", "-o", "/dev/null", "-D", "-"}
	if header != "" {
		args = append(args, "-H", header)
	}
	out, err := docker(append(args, url)...).Output()
	require.NoError(t, err, "curl %s: %s", url, stderrText(err))
	h := headers{}
	for _, line := range strings.Split(string(out), "\n") {
		name, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if ok {
			h[strings.ToLower(name)] = strings.TrimSpace(value)
		}
	}
	require.NotEmpty(t, h, "no response headers from %s", url)
	return h
}

// TestBuiltinCacheRules guards #184 with a real varnishd: the generated VCL
// keeps the cache-safety rules of Varnish's built-in VCL that it bypasses by
// returning explicitly, and applies them after the snippets, so a snippet can
// still decide first.
func TestBuiltinCacheRules(t *testing.T) {
	image := testVarnishImage(t)
	originIP := containerIP(t, startOriginVCL(t, image, cacheRulesOriginVCL))
	id := startOperatorVarnishd(t, image, &vinylv1.VinylCache{})

	spec := &vinylv1.VinylCacheSpec{Backends: []vinylv1.BackendSpec{
		{Name: "origin", ServiceRef: vinylv1.ServiceRef{Name: "origin"}},
	}}
	spec.VCL.Snippets.VCLDeliver = `set resp.http.x-hits = obj.hits;`
	// Snippets decide first: one strips the cookie, one caches a request with
	// credentials for a resource it knows to be public.
	spec.VCL.Snippets.VCLBackendResponse = `if (bereq.url == "/set-cookie-stripped") {
        unset beresp.http.Set-Cookie;
    }`
	spec.VCL.Snippets.VCLRecv = `if (req.url == "/authorization-public") {
        return(hash);
    }`
	res, err := generator.New().Generate(generator.Input{
		Name: "rules", Namespace: "test", Spec: spec,
		Endpoints: map[string][]generator.Endpoint{"origin": {{IP: originIP, Port: 80}}},
	})
	require.NoError(t, err)
	loadVCL(t, id, res.VCL)

	cases := []struct {
		path   string
		header string
		cached bool
	}{
		{"/plain", "", true},
		{"/cookie-request", "Cookie: session=1", true},
		{"/set-cookie", "", false},
		{"/private", "", false},
		{"/no-store", "", false},
		{"/no-cache", "", false},
		{"/vary-star", "", false},
		{"/surrogate-no-store", "", false},
		{"/authorization", "Authorization: Basic dXNlcjpwYXNz", false},
		{"/set-cookie-stripped", "", true},
		{"/authorization-public", "Authorization: Basic dXNlcjpwYXNz", true},
	}
	for _, c := range cases {
		t.Run(strings.TrimPrefix(c.path, "/"), func(t *testing.T) {
			url := "http://127.0.0.1:8080" + c.path
			first := curlWithHeader(t, id, url, c.header)
			assert.Equal(t, "0", first.Get("x-hits"), "the first request is never a hit")
			second := curlWithHeader(t, id, url, c.header)
			if c.cached {
				assert.Equal(t, "1", second.Get("x-hits"), "%s must be served from cache", c.path)
			} else {
				assert.Equal(t, "0", second.Get("x-hits"), "%s must not be served from cache", c.path)
			}
		})
	}
}
