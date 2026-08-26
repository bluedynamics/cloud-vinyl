#!/usr/bin/env bash
# The E2E suite has two layers: chainsaw owns Kubernetes state, vinylprobe owns
# HTTP. A boundary that lives only in someone's head does not survive three
# sessions, so it is checked here. See
# docs/sources/explanation/testing-strategy.md.
set -euo pipefail

fail=0

if go list -deps ./cmd/vinylprobe 2>/dev/null | grep -q "^k8s.io/"; then
  echo "FAIL: cmd/vinylprobe pulls in k8s.io packages; it must speak only HTTP"
  go list -deps ./cmd/vinylprobe | grep "^k8s.io/" | sed 's/^/       /'
  fail=1
fi

if grep -rn "curl\|wget" e2e/tests/ >/dev/null 2>&1; then
  echo "FAIL: chainsaw tests invoke an HTTP client directly; use vinylprobe instead"
  grep -rn "curl\|wget" e2e/tests/ | sed 's/^/       /'
  fail=1
fi

[ "$fail" -eq 0 ] && echo "OK: E2E layer boundary intact"
exit "$fail"
