package proxy

import (
	"fmt"
	"regexp"
	"strings"
)

// banExprPattern matches "<LHS> <op> <RHS>" ban expressions.
// Only simple two-operand expressions are accepted.
var banExprPattern = regexp.MustCompile(`^\s*(\S+)\s+(~|!~|==|!=)\s+(\S+)\s*$`)

// wildcardRHS is the set of RHS values that effectively match everything,
// which we reject to prevent accidental cache-wide invalidation.
var wildcardRHS = map[string]bool{
	".*": true,
	".":  true,
}

// ValidateBanExpression validates a Varnish ban expression.
// Only expressions with an obj.http.* LHS are permitted to guard against
// accidental full-cache invalidation.
//
// Returns nil if the expression is acceptable, or an error describing
// the rejection reason.
func ValidateBanExpression(expr string) error {
	if strings.TrimSpace(expr) == "" {
		return fmt.Errorf("ban expression must not be empty")
	}

	m := banExprPattern.FindStringSubmatch(expr)
	if m == nil {
		return fmt.Errorf("ban expression %q does not match expected format: <LHS> <op> <RHS>", expr)
	}

	lhs := m[1]
	rhs := m[3]

	// Only obj.http.* is allowed on the LHS.
	if !strings.HasPrefix(lhs, "obj.http.") {
		return fmt.Errorf("ban expression LHS %q is not allowed; only obj.http.* is permitted", lhs)
	}

	// Reject wildcard RHS values that would invalidate the entire cache.
	if wildcardRHS[rhs] {
		return fmt.Errorf("ban expression RHS %q matches everything; this would invalidate the entire cache", rhs)
	}

	return nil
}
