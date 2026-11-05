//go:build integration

package controller

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	vinylv1 "github.com/bluedynamics/cloud-vinyl/api/v1"
	"github.com/bluedynamics/cloud-vinyl/internal/generator"
)

// namedOriginVCL is an origin that names itself in x-origin.
func namedOriginVCL(name string) string {
	return strings.Replace(originVCL, `set resp.body = "ok";`,
		`set resp.http.x-origin = "`+name+`";
    set resp.body = "ok";`, 1)
}

func testImages() []string {
	if image := os.Getenv("VINYL_TEST_VARNISH_IMAGE"); image != "" {
		return []string{image}
	}
	return []string{"varnish:8.0", "varnish:9.1.0"}
}

// TestUnsnippetedTrafficSpreadsAcrossBackendPods guards #173 with a real
// varnishd: without any vcl_recv snippet, requests go through the first
// backend's director and reach all of its pods, not just <first>_0.
func TestUnsnippetedTrafficSpreadsAcrossBackendPods(t *testing.T) {
	for _, image := range testImages() {
		t.Run(image, func(t *testing.T) {
			testVarnishImage(t)
			a := containerIP(t, startOriginVCL(t, image, namedOriginVCL("A")))
			b := containerIP(t, startOriginVCL(t, image, namedOriginVCL("B")))

			for _, director := range []string{"shard", "round_robin"} {
				t.Run(director, func(t *testing.T) {
					spec := &vinylv1.VinylCacheSpec{Backends: []vinylv1.BackendSpec{{
						Name:       "origin",
						ServiceRef: vinylv1.ServiceRef{Name: "origin"},
						Director:   &vinylv1.DirectorSpec{Type: director},
					}}}
					res, err := generator.New().Generate(generator.Input{
						Name: "spread", Namespace: "test", Spec: spec,
						Endpoints: map[string][]generator.Endpoint{"origin": {{IP: a, Port: 80}, {IP: b, Port: 80}}},
					})
					require.NoError(t, err)
					id := startOperatorVarnishd(t, image, &vinylv1.VinylCache{})
					loadVCL(t, id, res.VCL)

					seen := map[string]int{}
					for i := 0; i < 12; i++ {
						h := curlHeaders(t, id, fmt.Sprintf("http://127.0.0.1:8080/page-%d", i))
						seen[h.Get("x-origin")]++
					}
					assert.Greater(t, seen["A"], 0, "origin A got traffic: %v", seen)
					assert.Greater(t, seen["B"], 0, "origin B got traffic: %v", seen)
				})
			}
		})
	}
}

// TestUnsnippetedTrafficToEmptyFirstBackendIs503 guards #173: with the first
// backend without endpoints and no snippet, requests answer 503 instead of
// silently reaching a pod of another backend.
func TestUnsnippetedTrafficToEmptyFirstBackendIs503(t *testing.T) {
	for _, image := range testImages() {
		t.Run(image, func(t *testing.T) {
			testVarnishImage(t)
			other := containerIP(t, startOriginVCL(t, image, namedOriginVCL("other")))
			spec := &vinylv1.VinylCacheSpec{Backends: []vinylv1.BackendSpec{
				{Name: "first", ServiceRef: vinylv1.ServiceRef{Name: "first"}},
				{Name: "second", ServiceRef: vinylv1.ServiceRef{Name: "second"}},
			}}
			res, err := generator.New().Generate(generator.Input{
				Name: "emptyfirst", Namespace: "test", Spec: spec,
				Endpoints: map[string][]generator.Endpoint{"first": {}, "second": {{IP: other, Port: 80}}},
			})
			require.NoError(t, err)
			id := startOperatorVarnishd(t, image, &vinylv1.VinylCache{})
			loadVCL(t, id, res.VCL)

			for i := 0; i < 6; i++ {
				assert.Equal(t, "503", status(t, id, fmt.Sprintf("http://127.0.0.1:8080/page-%d", i)))
			}
		})
	}
}
