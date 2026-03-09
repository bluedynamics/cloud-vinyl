package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestACL(t *testing.T) {
	t.Run("valid CIDR allows matching IP", func(t *testing.T) {
		acl, err := NewACL([]string{"10.0.0.0/24"})
		require.NoError(t, err)
		assert.True(t, acl.Allows("10.0.0.5"))
	})

	t.Run("valid CIDR allows matching IP with port", func(t *testing.T) {
		acl, err := NewACL([]string{"10.0.0.0/24"})
		require.NoError(t, err)
		assert.True(t, acl.Allows("10.0.0.5:54321"))
	})

	t.Run("CIDR denies non-matching IP", func(t *testing.T) {
		acl, err := NewACL([]string{"10.0.0.0/24"})
		require.NoError(t, err)
		assert.False(t, acl.Allows("192.168.1.1"))
	})

	t.Run("CIDR denies non-matching IP with port", func(t *testing.T) {
		acl, err := NewACL([]string{"10.0.0.0/24"})
		require.NoError(t, err)
		assert.False(t, acl.Allows("192.168.1.1:80"))
	})

	t.Run("empty ACL allows all", func(t *testing.T) {
		acl, err := NewACL([]string{})
		require.NoError(t, err)
		assert.True(t, acl.Allows("1.2.3.4"))
		assert.True(t, acl.Allows("192.168.0.1"))
		assert.True(t, acl.Allows("10.255.255.255"))
	})

	t.Run("nil slice ACL allows all", func(t *testing.T) {
		acl, err := NewACL(nil)
		require.NoError(t, err)
		assert.True(t, acl.Allows("1.2.3.4"))
	})

	t.Run("invalid CIDR returns error", func(t *testing.T) {
		_, err := NewACL([]string{"not-a-cidr"})
		require.Error(t, err)
	})

	t.Run("multiple CIDRs — first match allows", func(t *testing.T) {
		acl, err := NewACL([]string{"10.0.0.0/24", "192.168.0.0/16"})
		require.NoError(t, err)
		assert.True(t, acl.Allows("192.168.100.1"))
		assert.False(t, acl.Allows("172.16.0.1"))
	})
}
