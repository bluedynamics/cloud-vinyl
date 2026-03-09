package generator

import (
	"crypto/sha256"
	"fmt"
)

// HashVCL returns the SHA-256 hex digest of the given VCL string.
// Identical VCL always produces identical hashes — used for change detection.
func HashVCL(vcl string) string {
	sum := sha256.Sum256([]byte(vcl))
	return fmt.Sprintf("%x", sum)
}
