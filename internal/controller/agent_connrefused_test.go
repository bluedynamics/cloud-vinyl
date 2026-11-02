package controller

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// pushVCL logs a refused connection as info, not as an error (#136). That
// relies on errors.Is finding ECONNREFUSED through the error chain the agent
// client builds: http.Client -> *url.Error -> *net.OpError -> syscall.Errno,
// wrapped once more with %w. Build that chain against a real closed port.
func TestConnRefusedSurvivesTheAgentClientWrapping(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+addr+"/vcl/push", nil)
	require.NoError(t, err)
	_, err = http.DefaultClient.Do(req)
	require.Error(t, err)
	wrapped := fmt.Errorf("sending push request to %s: %w", addr, err)

	require.True(t, errors.Is(wrapped, syscall.ECONNREFUSED), "got %v", wrapped)
}
