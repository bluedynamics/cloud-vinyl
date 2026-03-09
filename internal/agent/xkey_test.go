package agent_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bluedynamics/cloud-vinyl/internal/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestXkeyPurger_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "PURGE", r.Method)
		assert.Equal(t, "article-123", r.Header.Get("X-Xkey-Purge"))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	purger := agent.NewXkeyPurger(server.URL)
	count, err := purger.Purge(context.Background(), []string{"article-123"}, false)
	assert.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestXkeyPurger_SoftPurge_UsesCorrectHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "article-456", r.Header.Get("X-Xkey-Softpurge"))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	purger := agent.NewXkeyPurger(server.URL)
	_, err := purger.Purge(context.Background(), []string{"article-456"}, true)
	assert.NoError(t, err)
}

func TestXkeyPurger_MultipleKeys(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	purger := agent.NewXkeyPurger(server.URL)
	count, err := purger.Purge(context.Background(), []string{"key1", "key2", "key3"}, false)
	assert.NoError(t, err)
	assert.Equal(t, 3, count)
	assert.Equal(t, 3, requestCount)
}

func TestXkeyPurger_NonSuccessStatus_NotCounted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	purger := agent.NewXkeyPurger(server.URL)
	count, err := purger.Purge(context.Background(), []string{"missing-key"}, false)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestXkeyPurger_NoContent_Counted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	purger := agent.NewXkeyPurger(server.URL)
	count, err := purger.Purge(context.Background(), []string{"key1"}, false)
	assert.NoError(t, err)
	assert.Equal(t, 1, count)
}
