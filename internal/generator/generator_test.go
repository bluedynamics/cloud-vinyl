package generator_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	vinylv1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
	"github.com/bluedynamics/cloud-vinyl/internal/generator"
)

// makeMinimalInput returns an Input with a single backend and no optional features.
func makeMinimalInput() generator.Input {
	return generator.Input{
		Name:      "my-cache",
		Namespace: "production",
		Spec: &vinylv1alpha1.VinylCacheSpec{
			Replicas: 1,
			Image:    "varnish:7.6",
			Backends: []vinylv1alpha1.BackendSpec{
				{Name: "app_backend", ServiceRef: vinylv1alpha1.ServiceRef{Name: "my-app"}},
			},
		},
		Endpoints: map[string][]generator.Endpoint{
			"app_backend": {{IP: "10.0.1.1", Port: 8080}},
		},
	}
}

// newGenerator creates a generator loading templates from the local templates/ directory.
// Tests run from the package directory so the relative path resolves correctly.
func newGenerator(t *testing.T) generator.Generator {
	t.Helper()
	g, err := generator.NewWithTemplateDir("templates")
	require.NoError(t, err)
	return g
}

func TestGenerate_Determinism(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	r1, err := g.Generate(input)
	require.NoError(t, err)
	r2, err := g.Generate(input)
	require.NoError(t, err)
	assert.Equal(t, r1.VCL, r2.VCL, "two calls with identical input must produce identical VCL")
	assert.Equal(t, r1.Hash, r2.Hash, "two calls with identical input must produce identical hash")
}

func TestGenerate_HashStability(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	r, err := g.Generate(input)
	require.NoError(t, err)
	// The hash stored in the result must match a fresh hash of the VCL string.
	assert.Equal(t, generator.HashVCL(r.VCL), r.Hash, "result hash must equal HashVCL(result.VCL)")
}

func TestGenerate_AlwaysUnsetProxy(t *testing.T) {
	g := newGenerator(t)
	r, err := g.Generate(makeMinimalInput())
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "unset req.http.proxy",
		"vcl_recv must always strip Proxy header (httpoxy CVE-2016-5385)")
}

func TestGenerate_AlwaysQuerySort(t *testing.T) {
	g := newGenerator(t)
	r, err := g.Generate(makeMinimalInput())
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "std.querysort",
		"vcl_recv must always call std.querysort for consistent cache keys")
}

func TestGenerate_AlwaysNeverTTLZeroCheck(t *testing.T) {
	g := newGenerator(t)
	r, err := g.Generate(makeMinimalInput())
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "beresp.ttl <= 0s",
		"vcl_backend_response must always contain a zero-TTL guard")
}

func TestGenerate_BackendDefsInOutput(t *testing.T) {
	g := newGenerator(t)
	r, err := g.Generate(makeMinimalInput())
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "backend app_backend_0",
		"generated backend identifier must appear in VCL")
	assert.Contains(t, r.VCL, `"10.0.1.1"`,
		"backend IP must appear in VCL")
}

func TestGenerate_Xkey_AddsHeaders(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	enabled := true
	input.Spec.Invalidation = vinylv1alpha1.InvalidationSpec{
		Xkey: &vinylv1alpha1.XkeySpec{Enabled: enabled},
	}
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "beresp.http.x-url = bereq.url",
		"xkey: x-url must be set in vcl_backend_response")
	assert.Contains(t, r.VCL, "beresp.http.x-host = bereq.http.host",
		"xkey: x-host must be set in vcl_backend_response")
	assert.Contains(t, r.VCL, "unset resp.http.x-url",
		"xkey: x-url must be stripped in vcl_deliver")
	assert.Contains(t, r.VCL, "unset resp.http.x-host",
		"xkey: x-host must be stripped in vcl_deliver")
	assert.Contains(t, r.VCL, "import xkey",
		"xkey: xkey vmod must be imported")
}

func TestGenerate_Cluster_AddsPeerBackends(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Spec.Cluster = vinylv1alpha1.ClusterSpec{Enabled: true}
	input.Peers = []generator.PeerBackend{
		{Name: "my_cache_0", IP: "10.0.2.1", Port: 8080},
		{Name: "my_cache_1", IP: "10.0.2.2", Port: 8080},
	}
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "backend my_cache_0",
		"cluster peer 0 backend must appear in VCL")
	assert.Contains(t, r.VCL, "backend my_cache_1",
		"cluster peer 1 backend must appear in VCL")
	assert.Contains(t, r.VCL, "10.0.2.1",
		"cluster peer 0 IP must appear in VCL")
	assert.Contains(t, r.VCL, "10.0.2.2",
		"cluster peer 1 IP must appear in VCL")
	assert.Contains(t, r.VCL, "import directors",
		"cluster: directors vmod must be imported")
}

func TestGenerate_NoCluster_NoPeerBackends(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	// Cluster disabled, no peers provided.
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "import directors",
		"directors vmod is always imported (needed for per-backend directors)")
	assert.NotContains(t, r.VCL, "acl vinyl_cluster_peers",
		"no cluster: cluster peer ACL must NOT be present")
}

func TestGenerate_AllSubroutinesHaveReturnStatement(t *testing.T) {
	g := newGenerator(t)
	r, err := g.Generate(makeMinimalInput())
	require.NoError(t, err)

	subroutines := []string{
		"sub vcl_recv",
		"sub vcl_hash",
		"sub vcl_hit",
		"sub vcl_miss",
		"sub vcl_pass",
		"sub vcl_backend_fetch",
		"sub vcl_backend_response",
		"sub vcl_deliver",
		"sub vcl_purge",
		"sub vcl_pipe",
		"sub vcl_synth",
		"sub vcl_fini",
	}
	for _, sub := range subroutines {
		assert.Contains(t, r.VCL, sub, "missing subroutine: %s", sub)
	}
	// Each subroutine must have at least one return() call.
	assert.True(t, strings.Count(r.VCL, "return(") >= len(subroutines),
		"each subroutine must have at least one return() statement; found %d return() calls for %d subroutines",
		strings.Count(r.VCL, "return("), len(subroutines))
}

func TestGenerate_FullOverride_ReturnsAsIs(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Spec.VCL.FullOverride = "vcl 4.1;\ndefault: return(pass);"
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "vcl 4.1;\ndefault: return(pass);",
		"full override VCL content must be present verbatim")
	assert.Contains(t, r.VCL, "full override mode",
		"full override result must contain 'full override mode' indicator")
}

func TestGenerate_CustomSnippet_Included(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Spec.VCL.Snippets.VCLRecv = `set req.http.X-Custom = "hello";`
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, `X-Custom = "hello"`,
		"custom vcl_recv snippet content must appear in generated VCL")
}

func TestGenerate_SoftPurge_AddsGraceDelivery(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Spec.Invalidation = vinylv1alpha1.InvalidationSpec{
		Purge: &vinylv1alpha1.PurgeSpec{Soft: true},
	}
	r, err := g.Generate(input)
	require.NoError(t, err)
	// vcl_hit must handle grace delivery for soft-purged objects.
	assert.Contains(t, r.VCL, "obj.grace",
		"soft-purge: vcl_hit must reference obj.grace for stale-while-revalidate")
}

func TestGenerate_ProxyProtocol_ExportsRealIP(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Spec.ProxyProtocol = vinylv1alpha1.ProxyProtocolSpec{Enabled: true}
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "X-Forwarded-For",
		"proxy protocol: X-Forwarded-For handling must appear in vcl_recv")
	assert.Contains(t, r.VCL, "X-Real-IP",
		"proxy protocol: X-Real-IP must be set from X-Forwarded-For")
}

func TestGenerate_MultipleEndpoints_AllBackendDefs(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Endpoints["app_backend"] = []generator.Endpoint{
		{IP: "10.0.1.1", Port: 8080},
		{IP: "10.0.1.2", Port: 8080},
	}
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "backend app_backend_0", "first endpoint backend must appear")
	assert.Contains(t, r.VCL, "backend app_backend_1", "second endpoint backend must appear")
	assert.Contains(t, r.VCL, "10.0.1.1", "first endpoint IP must appear")
	assert.Contains(t, r.VCL, "10.0.1.2", "second endpoint IP must appear")
}

func TestGenerate_VCLHeader_ContainsNamespaceAndName(t *testing.T) {
	g := newGenerator(t)
	r, err := g.Generate(makeMinimalInput())
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "production/my-cache",
		"VCL header must contain namespace/name")
}

func TestGenerate_VCLVersion(t *testing.T) {
	g := newGenerator(t)
	r, err := g.Generate(makeMinimalInput())
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "vcl 4.1;",
		"generated VCL must declare version 4.1")
}

func TestGenerate_PurgeACL_AlwaysPresent(t *testing.T) {
	g := newGenerator(t)
	r, err := g.Generate(makeMinimalInput())
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "acl vinyl_purge_allowed",
		"purge ACL must always be present")
	assert.Contains(t, r.VCL, `"127.0.0.1"`,
		"purge ACL must always include localhost")
}

func TestGenerate_Cluster_Disabled_When_NoPeers(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	// Cluster enabled in spec but no peers provided — should NOT enable cluster.
	input.Spec.Cluster = vinylv1alpha1.ClusterSpec{Enabled: true}
	input.Peers = nil
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.NotContains(t, r.VCL, "directors.shard();\n    app_backend_0",
		"cluster-peer shard director must not be emitted when no peers are provided")
	assert.NotContains(t, r.VCL, "acl vinyl_cluster_peers",
		"cluster must not be enabled when no peers are provided")
}

func TestHashVCL_Deterministic(t *testing.T) {
	vcl := "vcl 4.1;\nsub vcl_recv { return(pass); }"
	h1 := generator.HashVCL(vcl)
	h2 := generator.HashVCL(vcl)
	assert.Equal(t, h1, h2, "HashVCL must return identical results for identical input")
	assert.Len(t, h1, 64, "SHA-256 hex digest must be 64 characters")
}

func TestHashVCL_DifferentInput_DifferentHash(t *testing.T) {
	h1 := generator.HashVCL("vcl a")
	h2 := generator.HashVCL("vcl b")
	assert.NotEqual(t, h1, h2, "different VCL content must produce different hashes")
}

func TestGenerate_BackendWithProbe(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Spec.Backends[0].Probe = &vinylv1alpha1.BackendProbeSpec{URL: "/healthz"}
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, ".url = \"/healthz\"",
		"backend probe URL must appear in VCL")
	assert.Contains(t, r.VCL, ".probe = {",
		"backend probe block must appear in VCL")
}

func TestGenerate_BackendWithConnectionParameters(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Spec.Backends[0].ConnectionParameters = &vinylv1alpha1.ConnectionParameters{
		MaxConnections: 150,
	}
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, ".max_connections = 150",
		"backend max_connections must appear in VCL")
}

func TestGenerate_ESI_ImportsEsiVmod(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Spec.VarnishParams = map[string]string{
		"feature +esi": "on",
	}
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "import esi",
		"ESI flag in VarnishParams must trigger 'import esi'")
}

func TestGenerate_CustomHeaderSnippet(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Spec.VCL.Snippets.Header = "# my custom import"
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "# my custom import",
		"custom header snippet must appear in generated VCL")
}

func TestGenerate_CustomVCLInitSnippet(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Spec.VCL.Snippets.VCLInit = "# init-custom"
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "# init-custom",
		"custom vcl_init snippet must appear in generated VCL")
}

func TestGenerate_CustomVCLHashSnippet(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Spec.VCL.Snippets.VCLHash = "hash_data(req.http.X-Custom);"
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "hash_data(req.http.X-Custom);",
		"custom vcl_hash snippet must appear in generated VCL")
}

func TestGenerate_CustomVCLHitSnippet(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Spec.VCL.Snippets.VCLHit = "# custom hit"
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "# custom hit")
}

func TestGenerate_CustomVCLMissSnippet(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Spec.VCL.Snippets.VCLMiss = "# custom miss"
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "# custom miss")
}

func TestGenerate_CustomVCLPassSnippet(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Spec.VCL.Snippets.VCLPass = "# custom pass"
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "# custom pass")
}

func TestGenerate_CustomVCLBackendFetchSnippet(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Spec.VCL.Snippets.VCLBackendFetch = "# custom backend_fetch"
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "# custom backend_fetch")
}

func TestGenerate_CustomVCLBackendResponseSnippet(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Spec.VCL.Snippets.VCLBackendResponse = "# custom backend_response"
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "# custom backend_response")
}

func TestGenerate_CustomVCLDeliverSnippet(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Spec.VCL.Snippets.VCLDeliver = "# custom deliver"
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "# custom deliver")
}

func TestGenerate_CustomVCLPurgeSnippet(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Spec.VCL.Snippets.VCLPurge = "# custom purge"
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "# custom purge")
}

func TestGenerate_CustomVCLPipeSnippet(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Spec.VCL.Snippets.VCLPipe = "# custom pipe"
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "# custom pipe")
}

func TestGenerate_CustomVCLSynthSnippet(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Spec.VCL.Snippets.VCLSynth = "# custom synth"
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "# custom synth")
}

func TestGenerate_CustomVCLFiniSnippet(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Spec.VCL.Snippets.VCLFini = "# custom fini"
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "# custom fini")
}

func TestGenerate_AllowedPurgeSources_InACL(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Spec.Invalidation = vinylv1alpha1.InvalidationSpec{
		Purge: &vinylv1alpha1.PurgeSpec{
			AllowedSources: []string{"10.1.0.0/24", "192.168.1.5"},
		},
	}
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, `"10.1.0.0/24"`, "allowed purge source CIDR must appear in ACL")
	assert.Contains(t, r.VCL, `"192.168.1.5"`, "allowed purge source IP must appear in ACL")
}

func TestGenerate_HyphenatedName_SanitizedInVCL(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Name = "my-hyphenated-cache"
	// Enable cluster so that the director name appears in vcl_init.
	input.Spec.Cluster = vinylv1alpha1.ClusterSpec{Enabled: true}
	input.Peers = []generator.PeerBackend{
		{Name: "my_hyphenated_cache_0", IP: "10.0.2.1", Port: 8080},
	}
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "director_my_hyphenated_cache",
		"hyphens in cache name must be replaced with underscores in VCL identifiers")
}

func TestGenerate_ClusterInit_HasDirectorSetup(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Spec.Cluster = vinylv1alpha1.ClusterSpec{Enabled: true}
	input.Peers = []generator.PeerBackend{
		{Name: "my_cache_0", IP: "10.0.2.1", Port: 8080},
	}
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "directors.shard()",
		"cluster: vcl_init must initialize shard director")
	assert.Contains(t, r.VCL, ".reconfigure()",
		"cluster: vcl_init must call reconfigure() on director")
}

func TestNewWithTemplateDir_InvalidDir_ReturnsError(t *testing.T) {
	_, err := generator.NewWithTemplateDir("/nonexistent/path")
	assert.Error(t, err, "loading templates from nonexistent dir must return an error")
}

func TestGenerate_GraceAlwaysSet(t *testing.T) {
	g := newGenerator(t)
	r, err := g.Generate(makeMinimalInput())
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "beresp.grace",
		"vcl_backend_response must always set a grace period")
}

func TestGenerate_ImportStd_AlwaysPresent(t *testing.T) {
	g := newGenerator(t)
	r, err := g.Generate(makeMinimalInput())
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "import std;",
		"std vmod must always be imported")
}

func TestGenerate_BackendWithAllConnectionParameters(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Spec.Backends[0].ConnectionParameters = &vinylv1alpha1.ConnectionParameters{
		ConnectTimeout:      metav1.Duration{Duration: 2 * time.Second},
		FirstByteTimeout:    metav1.Duration{Duration: 60 * time.Second},
		BetweenBytesTimeout: metav1.Duration{Duration: 30 * time.Second},
		IdleTimeout:         metav1.Duration{Duration: 55 * time.Second},
		MaxConnections:      200,
	}
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, ".connect_timeout = 2s",
		"connect_timeout must appear in VCL")
	assert.Contains(t, r.VCL, ".first_byte_timeout = 1m",
		"first_byte_timeout must appear in VCL (60s formats as 1m)")
	assert.Contains(t, r.VCL, ".between_bytes_timeout = 30s",
		"between_bytes_timeout must appear in VCL")
	assert.Contains(t, r.VCL, ".max_connections = 200",
		"max_connections must appear in VCL")
}

func TestGenerate_FmtDuration_Minutes(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Spec.Backends[0].ConnectionParameters = &vinylv1alpha1.ConnectionParameters{
		ConnectTimeout: metav1.Duration{Duration: 2 * time.Minute},
	}
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, ".connect_timeout = 2m",
		"minute-duration must be formatted as Xm in VCL")
}

func TestGenerate_FmtDuration_Hours(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Spec.Backends[0].ConnectionParameters = &vinylv1alpha1.ConnectionParameters{
		ConnectTimeout: metav1.Duration{Duration: 2 * time.Hour},
	}
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, ".connect_timeout = 2h",
		"hour-duration must be formatted as Xh in VCL")
}

func TestGenerate_BackendGroups_PerBackendDirector(t *testing.T) {
	g := newGenerator(t)
	input := generator.Input{
		Namespace: "ns",
		Name:      "cache",
		Spec: &vinylv1alpha1.VinylCacheSpec{
			Replicas: 1,
			Image:    "vinyl:test",
			Backends: []vinylv1alpha1.BackendSpec{
				{Name: "plone", ServiceRef: vinylv1alpha1.ServiceRef{Name: "plone-svc"}},
			},
		},
		Endpoints: map[string][]generator.Endpoint{
			"plone": {
				{IP: "10.0.0.1", Port: 8080},
				{IP: "10.0.0.2", Port: 8080},
				{IP: "10.0.0.3", Port: 8080},
			},
		},
	}
	r, err := g.Generate(input)
	require.NoError(t, err)

	// Per-pod backend blocks.
	assert.Contains(t, r.VCL, `backend plone_0 {`)
	assert.Contains(t, r.VCL, `backend plone_1 {`)
	assert.Contains(t, r.VCL, `backend plone_2 {`)

	// Director init for this backend group.
	assert.Contains(t, r.VCL, "new plone = directors.shard();",
		"default director must be shard")
	assert.Contains(t, r.VCL, "plone.add_backend(plone_0);")
	assert.Contains(t, r.VCL, "plone.add_backend(plone_1);")
	assert.Contains(t, r.VCL, "plone.add_backend(plone_2);")
	assert.Contains(t, r.VCL, "plone.reconfigure();")
}

func TestGenerate_BackendGroups_RoundRobin(t *testing.T) {
	g := newGenerator(t)
	input := generator.Input{
		Namespace: "ns", Name: "cache",
		Spec: &vinylv1alpha1.VinylCacheSpec{
			Replicas: 1, Image: "vinyl:test",
			Backends: []vinylv1alpha1.BackendSpec{{
				Name:       "api",
				ServiceRef: vinylv1alpha1.ServiceRef{Name: "api-svc"},
				Director:   &vinylv1alpha1.DirectorSpec{Type: "round_robin"},
			}},
		},
		Endpoints: map[string][]generator.Endpoint{
			"api": {{IP: "10.0.0.1", Port: 80}, {IP: "10.0.0.2", Port: 80}},
		},
	}
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "new api = directors.round_robin();")
	assert.Contains(t, r.VCL, "api.add_backend(api_0);")
	assert.Contains(t, r.VCL, "api.add_backend(api_1);")
	assert.NotContains(t, r.VCL, "api.reconfigure();",
		"round_robin: no reconfigure() call")
}

func TestGenerate_Cluster_WithShardWarmupRampup(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	warmup := 0.1
	input.Spec.Cluster = vinylv1alpha1.ClusterSpec{Enabled: true}
	input.Spec.Director = vinylv1alpha1.DirectorSpec{
		Shard: &vinylv1alpha1.ShardSpec{
			Warmup: &warmup,
			Rampup: metav1.Duration{Duration: 30 * time.Second},
		},
	}
	input.Peers = []generator.PeerBackend{
		{Name: "my_cache_0", IP: "10.0.2.1", Port: 8080},
	}
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "set_warmup(",
		"cluster: vcl_init must call set_warmup() when warmup is configured")
	assert.Contains(t, r.VCL, "set_rampup(",
		"cluster: vcl_init must call set_rampup() when rampup is configured")
}

func TestGenerate_BackendGroups_Hash(t *testing.T) {
	g := newGenerator(t)
	input := generator.Input{
		Namespace: "ns", Name: "cache",
		Spec: &vinylv1alpha1.VinylCacheSpec{
			Replicas: 1, Image: "vinyl:test",
			Backends: []vinylv1alpha1.BackendSpec{{
				Name:       "api",
				ServiceRef: vinylv1alpha1.ServiceRef{Name: "api-svc"},
				Director:   &vinylv1alpha1.DirectorSpec{Type: "hash"},
			}},
		},
		Endpoints: map[string][]generator.Endpoint{
			"api": {{IP: "10.0.0.1", Port: 80}, {IP: "10.0.0.2", Port: 80}},
		},
	}
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "new api = directors.hash();")
	assert.Contains(t, r.VCL, "api.add_backend(api_0, 1.0);")
	assert.Contains(t, r.VCL, "api.add_backend(api_1, 1.0);")
	assert.NotContains(t, r.VCL, "api.reconfigure();",
		"hash: no reconfigure() call")
}

func TestGenerate_BackendGroups_Fallback(t *testing.T) {
	g := newGenerator(t)
	input := generator.Input{
		Namespace: "ns", Name: "cache",
		Spec: &vinylv1alpha1.VinylCacheSpec{
			Replicas: 1, Image: "vinyl:test",
			Backends: []vinylv1alpha1.BackendSpec{{
				Name:       "primary",
				ServiceRef: vinylv1alpha1.ServiceRef{Name: "primary-svc"},
				Director:   &vinylv1alpha1.DirectorSpec{Type: "fallback"},
			}},
		},
		Endpoints: map[string][]generator.Endpoint{
			"primary": {{IP: "10.0.0.1", Port: 80}, {IP: "10.0.0.2", Port: 80}},
		},
	}
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "new primary = directors.fallback();")
	assert.Contains(t, r.VCL, "primary.add_backend(primary_0);")
	assert.Contains(t, r.VCL, "primary.add_backend(primary_1);")
}

func TestGenerate_EmptyBackendEndpoints_ReturnsError(t *testing.T) {
	g := newGenerator(t)
	input := generator.Input{
		Namespace: "ns", Name: "cache",
		Spec: &vinylv1alpha1.VinylCacheSpec{
			Replicas: 1, Image: "vinyl:test",
			Backends: []vinylv1alpha1.BackendSpec{{
				Name:       "empty",
				ServiceRef: vinylv1alpha1.ServiceRef{Name: "some-svc"},
			}},
		},
		Endpoints: map[string][]generator.Endpoint{
			"empty": {},
		},
	}
	_, err := g.Generate(input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no resolved endpoints")
}

func TestGenerate_ProbeFields_PropagatedToPerPodBackends(t *testing.T) {
	g := newGenerator(t)
	expected := int32(204)
	input := generator.Input{
		Namespace: "ns", Name: "cache",
		Spec: &vinylv1alpha1.VinylCacheSpec{
			Replicas: 1, Image: "vinyl:test",
			Backends: []vinylv1alpha1.BackendSpec{{
				Name:       "plone",
				ServiceRef: vinylv1alpha1.ServiceRef{Name: "plone-svc"},
				Probe: &vinylv1alpha1.BackendProbeSpec{
					URL:              "/ok",
					Interval:         metav1.Duration{Duration: 10 * time.Second},
					Timeout:          metav1.Duration{Duration: 5 * time.Second},
					Window:           10,
					Threshold:        8,
					ExpectedResponse: &expected,
				},
			}},
		},
		Endpoints: map[string][]generator.Endpoint{
			"plone": {{IP: "10.0.0.1", Port: 8080}, {IP: "10.0.0.2", Port: 8080}},
		},
	}
	r, err := g.Generate(input)
	require.NoError(t, err)
	// Both per-pod backends must carry the spec probe config, not hardcoded defaults.
	assert.Contains(t, r.VCL, `.url = "/ok"`)
	assert.Contains(t, r.VCL, ".interval = 10s")
	assert.Contains(t, r.VCL, ".timeout = 5s")
	assert.Contains(t, r.VCL, ".window = 10")
	assert.Contains(t, r.VCL, ".threshold = 8")
	assert.Contains(t, r.VCL, ".expected_response = 204")
	assert.NotContains(t, r.VCL, ".timeout = 2s",
		"hardcoded timeout must not leak through when spec sets a value")
}

func TestGenerate_ProbeDefaults_AppliedWhenFieldsUnset(t *testing.T) {
	g := newGenerator(t)
	input := generator.Input{
		Namespace: "ns", Name: "cache",
		Spec: &vinylv1alpha1.VinylCacheSpec{
			Replicas: 1, Image: "vinyl:test",
			Backends: []vinylv1alpha1.BackendSpec{{
				Name:       "plone",
				ServiceRef: vinylv1alpha1.ServiceRef{Name: "plone-svc"},
				Probe:      &vinylv1alpha1.BackendProbeSpec{URL: "/ok"},
			}},
		},
		Endpoints: map[string][]generator.Endpoint{"plone": {{IP: "10.0.0.1", Port: 8080}}},
	}
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, ".interval = 5s", "default interval")
	assert.Contains(t, r.VCL, ".timeout = 2s", "default timeout")
	assert.Contains(t, r.VCL, ".window = 5", "default window")
	assert.Contains(t, r.VCL, ".threshold = 3", "default threshold")
	assert.NotContains(t, r.VCL, ".expected_response", "no expected_response when unset")
}

func TestGenerate_FullOverride_BypassesEmptyBackendCheck(t *testing.T) {
	g := newGenerator(t)
	input := generator.Input{
		Namespace: "ns", Name: "cache",
		Spec: &vinylv1alpha1.VinylCacheSpec{
			Replicas: 1, Image: "vinyl:test",
			Backends: []vinylv1alpha1.BackendSpec{{
				Name:       "empty",
				ServiceRef: vinylv1alpha1.ServiceRef{Name: "some-svc"},
			}},
			VCL: vinylv1alpha1.VCLSpec{FullOverride: "vcl 4.1; # user override"},
		},
		Endpoints: map[string][]generator.Endpoint{"empty": {}},
	}
	r, err := g.Generate(input)
	require.NoError(t, err, "full override must bypass the empty-backend guard")
	assert.Contains(t, r.VCL, "user override")
}

func TestGenerate_OperatorIP_PresentInPurgeACL(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.OperatorIP = "10.244.1.7"
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "acl vinyl_purge_allowed {")
	assert.Contains(t, r.VCL, `"10.244.1.7";`,
		"operator IP must appear in vinyl_purge_allowed")
	assert.Contains(t, r.VCL, `"127.0.0.1";`,
		"localhost entry must remain")
}

func TestGenerate_OperatorIP_OmittedWhenEmpty(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	// OperatorIP left at zero value.
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "acl vinyl_purge_allowed {")
	// Only localhost must be in the ACL (no user allowedSources in the minimal input).
	purgeACL := strings.SplitAfter(r.VCL, "acl vinyl_purge_allowed {")[1]
	purgeACL = strings.SplitN(purgeACL, "}", 2)[0]
	assert.Equal(t, 1, strings.Count(purgeACL, `"127.0.0.1";`),
		"minimal input must emit exactly one ACL entry (localhost)")
	assert.NotContains(t, purgeACL, "10.244.",
		"no operator IP entry when OperatorIP is empty")
}

func TestGenerate_OperatorIP_CoexistsWithAllowedSources(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.OperatorIP = "10.244.1.7"
	input.Spec.Invalidation.Purge = &vinylv1alpha1.PurgeSpec{
		Enabled:        true,
		AllowedSources: []string{"10.0.0.0/8", "192.168.1.0/24"},
	}
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, `"10.244.1.7";`)
	assert.Contains(t, r.VCL, `"10.0.0.0/8";`)
	assert.Contains(t, r.VCL, `"192.168.1.0/24";`)
	assert.Contains(t, r.VCL, `"127.0.0.1";`)
}

func TestGenerate_BAN_DisabledByDefault(t *testing.T) {
	g := newGenerator(t)
	r, err := g.Generate(makeMinimalInput())
	require.NoError(t, err)
	assert.NotContains(t, r.VCL, "acl vinyl_ban_allowed",
		"BAN ACL must NOT be emitted when spec.invalidation.ban is unset")
	assert.NotContains(t, r.VCL, `req.method == "BAN"`,
		"BAN handler must NOT be emitted when ban is disabled")
}

func TestGenerate_BAN_EnabledEmitsACLAndHandler(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Spec.Invalidation.BAN = &vinylv1alpha1.BANSpec{Enabled: true}
	r, err := g.Generate(input)
	require.NoError(t, err)

	// Extract just the BAN block so assertions don't accidentally match
	// substrings emitted elsewhere in the VCL.
	banBlock := strings.SplitAfter(r.VCL, `if (req.method == "BAN")`)[1]
	banBlock = strings.SplitN(banBlock, "# Only handle GET", 2)[0]

	assert.Contains(t, r.VCL, "acl vinyl_ban_allowed {",
		"BAN ACL must be emitted when ban.enabled=true")
	assert.Contains(t, banBlock, `client.ip ~ vinyl_ban_allowed`,
		"BAN handler must check the BAN ACL")
	assert.Contains(t, banBlock, `synth(403, "Forbidden")`,
		"BAN handler must 403 outside the ACL")
	assert.Contains(t, banBlock, `synth(400, "Missing X-Vinyl-Ban header")`,
		"BAN handler must 400 when X-Vinyl-Ban header is absent")
	assert.Contains(t, banBlock, `std.ban(req.http.X-Vinyl-Ban)`,
		"BAN handler must use std.ban(), not deprecated ban()")
	assert.Contains(t, banBlock, `std.ban_error()`,
		"BAN handler must surface std.ban_error() on malformed expressions")
	assert.Contains(t, banBlock, `return(synth(200, "Banned"))`,
		"BAN handler must return synth 200 on success")

	// Hard regression guard: the bare ban() call form must never be emitted.
	// (Matches the original architecture checklist: always use std.ban().)
	// std.ban(...) is fine; only the bare form (preceded by whitespace, not
	// by `.`) is forbidden.
	assert.NotContains(t, r.VCL, " ban(req.http.X-Vinyl-Ban)",
		"deprecated bare ban() call must not appear; use std.ban()")
}

func TestGenerate_BAN_BanLurkerHeadersEmitted(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Spec.Invalidation.BAN = &vinylv1alpha1.BANSpec{Enabled: true}
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.Contains(t, r.VCL, "beresp.http.x-url = bereq.url",
		"BAN requires x-url ban-lurker-friendly header (otherwise bans accumulate O(n*m))")
	assert.Contains(t, r.VCL, "beresp.http.x-host = bereq.http.host",
		"BAN requires x-host ban-lurker-friendly header")
	assert.Contains(t, r.VCL, "unset resp.http.x-url",
		"BAN-support internal headers must be stripped before delivery")
	assert.Contains(t, r.VCL, "unset resp.http.x-host",
		"BAN-support internal headers must be stripped before delivery")
}

func TestGenerate_BAN_RateLimit_CurrentlyInert(t *testing.T) {
	// Regression guard: rate-limit field is accepted on the CR but not
	// plumbed into VCL (tracked as a BAN follow-up). This test locks in
	// the deferred state so nobody half-implements it.
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Spec.Invalidation.BAN = &vinylv1alpha1.BANSpec{
		Enabled:            true,
		RateLimitPerMinute: 60,
	}
	r, err := g.Generate(input)
	require.NoError(t, err)
	assert.NotContains(t, r.VCL, "vsthrottle",
		"vsthrottle VMOD must not be imported until rate-limiting is wired")
	assert.NotContains(t, r.VCL, "Rate limited",
		"no rate-limit synth message until the feature is implemented")
}

func TestGenerate_BAN_OperatorIPInACL(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.OperatorIP = "10.244.1.7"
	input.Spec.Invalidation.BAN = &vinylv1alpha1.BANSpec{Enabled: true}
	r, err := g.Generate(input)
	require.NoError(t, err)
	banACL := strings.SplitAfter(r.VCL, "acl vinyl_ban_allowed {")[1]
	banACL = strings.SplitN(banACL, "}", 2)[0]
	assert.Contains(t, banACL, `"10.244.1.7";`,
		"operator IP must appear in vinyl_ban_allowed so operator-proxied BAN requests are accepted")
	assert.Contains(t, banACL, `"127.0.0.1";`,
		"localhost entry must remain")
}

func TestGenerate_BAN_AllowedSourcesHonored(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Spec.Invalidation.BAN = &vinylv1alpha1.BANSpec{
		Enabled:        true,
		AllowedSources: []string{"10.0.0.0/8", "192.168.1.0/24"},
	}
	r, err := g.Generate(input)
	require.NoError(t, err)
	banACL := strings.SplitAfter(r.VCL, "acl vinyl_ban_allowed {")[1]
	banACL = strings.SplitN(banACL, "}", 2)[0]
	assert.Contains(t, banACL, `"10.0.0.0/8";`)
	assert.Contains(t, banACL, `"192.168.1.0/24";`)
}

func TestGenerate_BAN_EnabledButNotConfigured_OnlyLocalhost(t *testing.T) {
	g := newGenerator(t)
	input := makeMinimalInput()
	input.Spec.Invalidation.BAN = &vinylv1alpha1.BANSpec{Enabled: true}
	r, err := g.Generate(input)
	require.NoError(t, err)
	banACL := strings.SplitAfter(r.VCL, "acl vinyl_ban_allowed {")[1]
	banACL = strings.SplitN(banACL, "}", 2)[0]
	assert.Equal(t, 1, strings.Count(banACL, `"127.0.0.1";`),
		"BAN ACL with no AllowedSources and no OperatorIP must contain exactly one entry (localhost)")
}
