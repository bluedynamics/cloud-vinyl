package proxy

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServerStartStop(t *testing.T) {
	mb := &MockBroadcaster{Result: okResult()}
	router := NewStaticRouter(map[string][2]string{
		"my-cache-invalidation.production": {"production", "my-cache"},
	})
	pm := NewPodMap()
	pm.Update("production", "my-cache", []string{"10.0.0.1"})

	srv := NewServer("127.0.0.1:0", router, pm, mb)

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

	srv := NewServer(":8090", router, pm, mb)

	assert.NotNil(t, srv)
	assert.Equal(t, ":8090", srv.addr)
	assert.NotNil(t, srv.acl)
	assert.NotNil(t, srv.rateLimiter)
}
