package proxy

import (
	"fmt"
	"net"
)

// ACL enforces a source-IP allowlist based on CIDR ranges.
// An empty ACL (no configured networks) allows all traffic.
type ACL struct {
	nets []*net.IPNet
}

// NewACL creates an ACL from a list of CIDR strings.
// Returns an error if any CIDR is invalid.
func NewACL(cidrs []string) (*ACL, error) {
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q: %w", cidr, err)
		}
		nets = append(nets, ipNet)
	}
	return &ACL{nets: nets}, nil
}

// Allows reports whether the given address is permitted by the ACL.
// addr may be "IP:port" or plain "IP". An ACL with no networks allows all.
func (a *ACL) Allows(addr string) bool {
	if len(a.nets) == 0 {
		return true
	}

	// Strip port if present.
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// addr has no port — use as-is.
		host = addr
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}

	for _, n := range a.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
