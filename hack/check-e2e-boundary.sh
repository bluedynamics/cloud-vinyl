#!/usr/bin/env bash
# The E2E suite has two layers: chainsaw owns Kubernetes state, vinylprobe owns
# HTTP. A boundary that lives only in someone's head does not survive three
# sessions, so it is checked here. See
# docs/sources/explanation/testing-strategy.md.
set -euo pipefail

fail=0

# Capture into a variable rather than piping straight into `grep -q`: grep
# exits as soon as it finds a match and closes its end of the pipe, which
# SIGPIPEs `go list` before it finishes writing. Under `pipefail` the
# pipeline's exit status then comes from the killed `go list`, not from
# grep's successful match, so `if go list ... | grep -q ...` silently passes
# on exactly the violation it exists to catch. Same failure shape as the
# grep/pipefail bug fixed earlier in hack/check-suite-labels.sh.
if ! deps="$(go list -deps ./cmd/vinylprobe 2>&1)"; then
  echo "FAIL: go list -deps ./cmd/vinylprobe failed; cannot verify the boundary"
  echo "$deps" | sed 's/^/       /'
  fail=1
elif echo "$deps" | grep -qE "^(k8s\.io|sigs\.k8s\.io)/"; then
  echo "FAIL: cmd/vinylprobe pulls in k8s.io/sigs.k8s.io packages; it must speak only HTTP"
  echo "$deps" | grep -E "^(k8s\.io|sigs\.k8s\.io)/" | sed 's/^/       /'
  fail=1
fi

# grep here runs standalone (output only redirected, not piped into a second
# process that can exit early), so it has no SIGPIPE/pipefail exposure the
# way the k8s.io check above did. Kept as a single grep run either way.
if grep -rn "curl\|wget" e2e/tests/ >/dev/null 2>&1; then
  echo "FAIL: chainsaw tests invoke an HTTP client directly; use vinylprobe instead"
  grep -rn "curl\|wget" e2e/tests/ | sed 's/^/       /'
  fail=1
fi

[ "$fail" -eq 0 ] && echo "OK: E2E layer boundary intact"
exit "$fail"
