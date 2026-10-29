package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bluedynamics/cloud-vinyl/internal/monitoring"
)

func TestServerStartStop(t *testing.T) {
	mb := &MockBroadcaster{Result: okResult()}
	router := NewStaticRouter(map[string][2]string{
		"my-cache-invalidation.production": {"production", "my-cache"},
	})
	pm := NewPodMap()
	pm.Update("production", "my-cache", []string{"10.0.0.1"})

	srv := NewServer("127.0.0.1:0", router, pm, mb, nil)

	// Use a fixed port in test range.
	srv.addr = "127.0.0.1:19876"

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start(ctx)
	}()

	// Wait for the server to start.
	require.Eventually(t, func() bool {
		resp, err := http.Get("http://127.0.0.1:19876/")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return true
	}, 2*time.Second, 50*time.Millisecond)

	// Cancel context → server shuts down.
	cancel()

	select {
	case err := <-errCh:
		// http.ErrServerClosed is expected after Shutdown.
		assert.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("server did not shut down in time")
	}
}

func TestNewServer_Defaults(t *testing.T) {
	router := NewRegisteredRouter()
	pm := NewPodMap()
	mb := &MockBroadcaster{}

	srv := NewServer(":8090", router, pm, mb, nil)

	assert.NotNil(t, srv)
	assert.Equal(t, ":8090", srv.addr)
	assert.NotNil(t, srv.acl)
	assert.NotNil(t, srv.rateLimiter)
}

// A bare Service name is rejected with the namespaced host to use, and every
// rejection is counted: clients like plone.cachepurging do not log failed
// purges (#132).
func TestServeHTTP_UnknownHost_NamesHostAndCounts(t *testing.T) {
	router := NewRegisteredRouter()
	router.Register("app", "my-cache")
	srv := NewServer(":8090", router, NewPodMap(), &MockBroadcaster{}, nil)
	m := monitoring.NewMetrics(prometheus.NewRegistry())
	srv.SetMetrics(m)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PURGE", "/page", nil)
	req.Host = "my-cache-invalidation:8090"
	srv.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), `unknown cache: my-cache-invalidation:8090`)
	assert.Contains(t, rec.Body.String(), `use \"my-cache-invalidation.app\"`)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("PURGE", "/page", nil)
	req.Host = "cloud-vinyl.cloud-vinyl-system.svc:8090"
	srv.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.NotContains(t, rec.Body.String(), "must include the namespace",
		"a host that matches no cache gets no guess")

	assert.Equal(t, 2.0, testutil.ToFloat64(m.InvalidationUnknownHostTotal))
}
