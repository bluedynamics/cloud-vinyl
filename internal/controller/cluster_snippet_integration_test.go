//go:build integration

package controller

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	vinylv1 "github.com/bluedynamics/cloud-vinyl/api/v1"
	"github.com/bluedynamics/cloud-vinyl/internal/generator"
)

// originVCL makes varnishd a cacheable origin that echoes x-fetch-mark, so
// the test can count how often vcl_backend_fetch touched the request.
const originVCL = `vcl 4.1;
backend default none;
sub vcl_recv { return (synth(200)); }
sub vcl_synth {
    set resp.http.Cache-Control = "max-age=300";
    set resp.http.x-seen-mark = req.http.x-fetch-mark;
    set resp.body = "ok";
    return (deliver);
}
`

// TestClusterSnippetsRunOnOwnerHopOnly guards #150 with a real varnishd. The
// cache is its own only peer, so every request takes both hops through the
// same varnishd: the relay hop passes it to the peer address, the owner hop
// does the lookup. Before #150 the vcl_deliver snippet ran again on the relay
// hop, where obj.hits is 0, and turned the owner's HIT into MISS; the
// vcl_backend_fetch snippet ran on both hops and marked the request twice.
func TestClusterSnippetsRunOnOwnerHopOnly(t *testing.T) {
	image := testVarnishImage(t)
	originIP := startOrigin(t, image)

	id := startOperatorVarnishd(t, image, &vinylv1.VinylCache{})
	cacheIP := containerIP(t, id)

	spec := &vinylv1.VinylCacheSpec{
		Backends: []vinylv1.BackendSpec{{Name: "origin", ServiceRef: vinylv1.ServiceRef{Name: "origin"}}},
		Cluster:  vinylv1.ClusterSpec{Enabled: true},
	}
	spec.VCL.Snippets.VCLDeliver = `if (obj.hits > 0) {
        set resp.http.x-cache = "HIT";
    } else {
        set resp.http.x-cache = "MISS";
    }
    return (deliver);`
	spec.VCL.Snippets.VCLBackendFetch = `if (bereq.http.x-fetch-mark) {
        set bereq.http.x-fetch-mark = bereq.http.x-fetch-mark + "x";
    } else {
        set bereq.http.x-fetch-mark = "x";
    }`
	res, err := generator.New().Generate(generator.Input{
		Name:      "snippets",
		Namespace: "test",
		Spec:      spec,
		Endpoints: map[string][]generator.Endpoint{"origin": {{IP: originIP, Port: 80}}},
		Peers:     []generator.PeerBackend{{Name: "snippets_0", IP: cacheIP, Port: varnishPort}},
	})
	require.NoError(t, err)
	loadVCL(t, id, res.VCL)

	first := curlHeaders(t, id, "http://127.0.0.1:8080/page")
	assert.Equal(t, "MISS", first.Get("x-cache"))
	assert.Equal(t, "x", first.Get("x-seen-mark"),
		"vcl_backend_fetch must touch the request once, on the owner hop")

	second := curlHeaders(t, id, "http://127.0.0.1:8080/page")
	assert.Equal(t, "HIT", second.Get("x-cache"),
		"the relay hop must not overwrite the owner's x-cache")
}

// startOrigin runs a varnishd that answers every request itself and returns
// its IP on the docker bridge.
func startOrigin(t *testing.T, image string) string {
	t.Helper()
	id := startOriginVCL(t, image, originVCL)
	return containerIP(t, id)
}

// startOriginVCL runs a varnishd with the given VCL as an origin and returns
// its container id.
func startOriginVCL(t *testing.T, image, vcl string) string {
	t.Helper()
	dir := t.TempDir()
	vclPath := filepath.Join(dir, "default.vcl")
	require.NoError(t, os.WriteFile(vclPath, []byte(vcl), 0o644))
	require.NoError(t, os.Chmod(dir, 0o755))
	out, err := docker("run", "-d", "--rm", "-v", vclPath+":/etc/varnish/default.vcl:ro", image).Output()
	require.NoError(t, err, "docker run origin: %s", stderrText(err))
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = docker("rm", "-f", id).Run() })

	deadline := time.Now().Add(15 * time.Second)
	for docker("exec", id, "curl", "-sf", "-o", "/dev/null", "http://127.0.0.1:80/").Run() != nil {
		if time.Now().After(deadline) {
			logs, _ := docker("logs", id).CombinedOutput()
			t.Fatalf("origin did not come up:\n%s", logs)
		}
		time.Sleep(200 * time.Millisecond)
	}
	return id
}

func containerIP(t *testing.T, id string) string {
	t.Helper()
	out, err := docker("inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", id).Output()
	require.NoError(t, err, "docker inspect: %s", stderrText(err))
	ip := strings.TrimSpace(string(out))
	require.NotEmpty(t, ip)
	return ip
}

// loadVCL writes vcl into the container's /tmp and makes it the active VCL,
// as the agent does with a push.
func loadVCL(t *testing.T, id, vcl string) {
	t.Helper()
	cmd := docker("exec", "-i", id, "sh", "-c", "cat > /tmp/test.vcl")
	cmd.Stdin = strings.NewReader(vcl)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "write vcl: %s", out)
	varnishadm(t, id, "vcl.load", "test", "/tmp/test.vcl")
	varnishadm(t, id, "vcl.use", "test")
}

type headers map[string]string

func (h headers) Get(name string) string { return h[strings.ToLower(name)] }

func curlHeaders(t *testing.T, id, url string) headers {
	t.Helper()
	out, err := docker("exec", id, "curl", "-s", "-o", "/dev/null", "-D", "-", url).Output()
	require.NoError(t, err, "curl %s: %s", url, stderrText(err))
	h := headers{}
	for _, line := range strings.Split(string(out), "\n") {
		name, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if ok {
			h[strings.ToLower(name)] = strings.TrimSpace(value)
		}
	}
	require.NotEmpty(t, h, fmt.Sprintf("no response headers from %s", url))
	return h
}

// TestClusterSnippetsWithReturnCompile compiles cluster VCL whose guarded
// snippets end in return statements: the guard puts them inside an if block,
// which VCC must accept for every one of the five subroutines.
func TestClusterSnippetsWithReturnCompile(t *testing.T) {
	image := testVarnishImage(t)
	spec := &vinylv1.VinylCacheSpec{
		Backends: []vinylv1.BackendSpec{{Name: "origin", ServiceRef: vinylv1.ServiceRef{Name: "origin"}}},
		Cluster:  vinylv1.ClusterSpec{Enabled: true},
	}
	spec.VCL.Snippets.VCLHash = "return (lookup);"
	spec.VCL.Snippets.VCLPass = "return (fetch);"
	spec.VCL.Snippets.VCLDeliver = "return (deliver);"
	spec.VCL.Snippets.VCLBackendFetch = "return (fetch);"
	spec.VCL.Snippets.VCLBackendResponse = "return (deliver);"
	res, err := generator.New().Generate(generator.Input{
		Name:      "compile",
		Namespace: "test",
		Spec:      spec,
		Endpoints: map[string][]generator.Endpoint{"origin": {{IP: "10.0.0.1", Port: 80}}},
		Peers:     []generator.PeerBackend{{Name: "compile_0", IP: "10.0.0.2", Port: varnishPort}},
	})
	require.NoError(t, err)
	require.Contains(t, res.VCL, "if (!bereq.http.X-Vinyl-Shard) {")

	dir := t.TempDir()
	vclPath := filepath.Join(dir, "cluster.vcl")
	require.NoError(t, os.WriteFile(vclPath, []byte(res.VCL), 0o644))
	require.NoError(t, os.Chmod(dir, 0o755))
	out, err := docker("run", "--rm", "--entrypoint", "varnishd",
		"-v", vclPath+":/cluster.vcl:ro", image,
		"-C", "-f", "/cluster.vcl", "-n", "/tmp/varnishd").CombinedOutput()
	require.NoError(t, err, "cluster VCL does not compile:\n%s", out)
}

// TestBackendWithoutEndpointsLoads guards #171 with a real varnishd, on every
// image the operator supports: generated VCL with an empty backend group, and
// with every group empty and no cluster (no real static backend at all), must
// load in vcl_init and answer 503 for the empty route while others work.
func TestBackendWithoutEndpointsLoads(t *testing.T) {
	images := []string{"varnish:8.0", "varnish:9.1.0"}
	if image := os.Getenv("VINYL_TEST_VARNISH_IMAGE"); image != "" {
		images = []string{image}
	}
	for _, image := range images {
		t.Run(image, func(t *testing.T) {
			testVarnishImage(t)
			originIP := startOrigin(t, image)

			for _, tc := range []struct {
				name      string
				endpoints map[string][]generator.Endpoint
				wantPlone int
			}{
				{"one empty group", map[string][]generator.Endpoint{
					"plone": {{IP: originIP, Port: 80}}, "thumbor": {},
				}, 200},
				{"all groups empty", map[string][]generator.Endpoint{
					"plone": {}, "thumbor": {},
				}, 503},
			} {
				t.Run(tc.name, func(t *testing.T) {
					spec := &vinylv1.VinylCacheSpec{Backends: []vinylv1.BackendSpec{
						{Name: "plone", ServiceRef: vinylv1.ServiceRef{Name: "plone"}},
						{Name: "thumbor", ServiceRef: vinylv1.ServiceRef{Name: "thumbor"}},
					}}
					spec.VCL.Snippets.VCLRecv = `if (req.url ~ "^/plone") { set req.backend_hint = plone.backend(); }
    if (req.url ~ "^/thumbor") { set req.backend_hint = thumbor.backend(); }`
					res, err := generator.New().Generate(generator.Input{
						Name: "empty", Namespace: "test", Spec: spec, Endpoints: tc.endpoints,
					})
					require.NoError(t, err)

					id := startOperatorVarnishd(t, image, &vinylv1.VinylCache{})
					loadVCL(t, id, res.VCL)

					assert.Equal(t, fmt.Sprint(tc.wantPlone), status(t, id, "http://127.0.0.1:8080/plone"))
					assert.Equal(t, "503", status(t, id, "http://127.0.0.1:8080/thumbor"),
						"the route of the backend without endpoints answers 503")
					assert.Contains(t, varnishadm(t, id, "backend.list"), "noendpoints_thumbor")
				})
			}
		})
	}
}

func status(t *testing.T, id, url string) string {
	t.Helper()
	out, err := docker("exec", id, "curl", "-s", "-o", "/dev/null", "-w", "%{http_code}", url).Output()
	require.NoError(t, err, "curl %s: %s", url, stderrText(err))
	return string(out)
}
