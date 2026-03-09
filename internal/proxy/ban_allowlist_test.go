package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateBanExpression(t *testing.T) {
	t.Run("valid obj.http.* expression", func(t *testing.T) {
		err := ValidateBanExpression("obj.http.X-Url ~ ^/product/")
		require.NoError(t, err)
	})

	t.Run("valid obj.http.* equality expression", func(t *testing.T) {
		err := ValidateBanExpression("obj.http.X-Tag == article-123")
		require.NoError(t, err)
	})

	t.Run("req.url LHS rejected", func(t *testing.T) {
		err := ValidateBanExpression("req.url ~ ^/product/")
		assert.Error(t, err)
	})

	t.Run("obj.url LHS rejected", func(t *testing.T) {
		err := ValidateBanExpression("obj.url ~ ^/product/")
		assert.Error(t, err)
	})

	t.Run("wildcard RHS '.' rejected", func(t *testing.T) {
		err := ValidateBanExpression("obj.http.Content-Type ~ .")
		assert.Error(t, err)
	})

	t.Run("wildcard RHS '.*' rejected", func(t *testing.T) {
		err := ValidateBanExpression("obj.http.Content-Type ~ .*")
		assert.Error(t, err)
	})

	t.Run("empty expression rejected", func(t *testing.T) {
		err := ValidateBanExpression("")
		assert.Error(t, err)
	})

	t.Run("whitespace-only expression rejected", func(t *testing.T) {
		err := ValidateBanExpression("   ")
		assert.Error(t, err)
	})

	t.Run("malformed expression rejected", func(t *testing.T) {
		err := ValidateBanExpression("obj.http.X-Url")
		assert.Error(t, err)
	})

	t.Run("bereq.url LHS rejected", func(t *testing.T) {
		err := ValidateBanExpression("bereq.url ~ ^/api/")
		assert.Error(t, err)
	})
}
