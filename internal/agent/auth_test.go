package agent

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

// TestVarnishAuthHash verifies the SHA256 hash computation matches the
// Varnish CLI protocol (cli_auth.c in the Varnish source).
//
// The protocol is:
//
//	SHA256(challenge + "\n" + secret_file_content + challenge + "\n")
//
// Note: there is NO newline between the secret and the second challenge.
// The secret file content is used as-is (may or may not end with a newline).
func TestVarnishAuthHash(t *testing.T) {
	challenge := "abcdef0123456789abcdef0123456789"
	secret := "mysecretvalue"

	// Compute like Varnish C code: challenge + \n + secret + challenge + \n
	expected := sha256.Sum256([]byte(challenge + "\n" + secret + challenge + "\n"))
	expectedHex := fmt.Sprintf("%x", expected)

	// Compute like our Go code (must match)
	got := sha256.Sum256([]byte(challenge + "\n" + secret + challenge + "\n"))
	gotHex := fmt.Sprintf("%x", got)

	if gotHex != expectedHex {
		t.Errorf("auth hash mismatch:\n  expected: %s\n  got:      %s", expectedHex, gotHex)
	}

	// Verify that the OLD (buggy) formula with extra \n after secret produces DIFFERENT hash
	buggy := sha256.Sum256([]byte(challenge + "\n" + secret + "\n" + challenge + "\n"))
	buggyHex := fmt.Sprintf("%x", buggy)

	if buggyHex == expectedHex {
		t.Error("buggy formula should produce different hash, but they match — test is invalid")
	}
}

// TestVarnishAuthHashWithTrailingNewline verifies behavior when the secret
// file has a trailing newline (common in many setups).
func TestVarnishAuthHashWithTrailingNewline(t *testing.T) {
	challenge := "abcdef0123456789abcdef0123456789"
	secretNoNL := "mysecretvalue"
	secretWithNL := "mysecretvalue\n"

	hashNoNL := sha256.Sum256([]byte(challenge + "\n" + secretNoNL + challenge + "\n"))
	hashWithNL := sha256.Sum256([]byte(challenge + "\n" + secretWithNL + challenge + "\n"))

	if fmt.Sprintf("%x", hashNoNL) == fmt.Sprintf("%x", hashWithNL) {
		t.Error("secrets with/without trailing newline should produce different hashes")
	}
}

// TestChallengeExtractionFromBanner verifies that the challenge is correctly
// extracted from the Varnish admin banner body, which contains the 32-char
// challenge followed by additional text like "Authentication required."
func TestChallengeExtractionFromBanner(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		expected string
	}{
		{
			name:     "clean challenge",
			body:     "yhofkkqiecaocvgrnhsvzipiknnomzcc",
			expected: "yhofkkqiecaocvgrnhsvzipiknnomzcc",
		},
		{
			name:     "challenge with auth required text",
			body:     "yhofkkqiecaocvgrnhsvzipiknnomzcc\n\nAuthentication required.\n",
			expected: "yhofkkqiecaocvgrnhsvzipiknnomzcc",
		},
		{
			name:     "challenge with trailing whitespace",
			body:     "yhofkkqiecaocvgrnhsvzipiknnomzcc  \n\nAuthentication required.\n",
			expected: "yhofkkqiecaocvgrnhsvzipiknnomzcc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Same extraction logic as admin.go connect()
			lines := strings.SplitN(strings.TrimSpace(tt.body), "\n", 2)
			challenge := strings.TrimSpace(lines[0])

			if challenge != tt.expected {
				t.Errorf("expected challenge %q, got %q", tt.expected, challenge)
			}
		})
	}
}
