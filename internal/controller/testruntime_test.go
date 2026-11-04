package controller

import (
	"github.com/stretchr/testify/require"

	vinylv1 "github.com/bluedynamics/cloud-vinyl/api/v1"
	"github.com/bluedynamics/cloud-vinyl/internal/catalog"
)

const testCatalogYAML = `
varnish:
  default: "9.1.0"
  versions:
    "9.1.0":
      daemon: docker.io/library/varnish:9.1.0
      exporter: example.org/varnish-exporter:1.8.3-varnish9.1.0
      tracer: example.org/cloud-vinyl-tracer:1.0.0
vinyl:
  default: "9.1.0"
  versions:
    "9.1.0":
      daemon: example.org/vinyl-cache:9.1.0
      exporter: example.org/varnish-exporter:1.8.3-vinyl9.1.0
      tracer: example.org/cloud-vinyl-tracer:1.0.0-vinyl
`

// testingT is what the helpers need from a test. *testing.T satisfies it, and
// so does Ginkgo's GinkgoT(), which is not a testing.TB.
type testingT interface {
	require.TestingT
	Helper()
}

// testCatalog is the catalog every controller unit test resolves against.
func testCatalog(t testingT) *catalog.Catalog {
	t.Helper()
	c, err := catalog.Parse([]byte(testCatalogYAML))
	require.NoError(t, err)
	return c
}

// testRuntime resolves a runtime from testCatalog for direct builder tests.
func testRuntime(t testingT, f vinylv1.Flavor) catalog.Runtime {
	t.Helper()
	rt, err := testCatalog(t).Resolve(f, "")
	require.NoError(t, err)
	return rt
}
