#!/usr/bin/env bash
# Every chainsaw test must carry exactly one suite label. Without this, a typo
# means a test runs in neither suite and disappears silently — the same class of
# failure as the --include-test-regex prefix trap documented in
# .github/workflows/e2e-chainsaw.yml.
set -euo pipefail

fail=0
# find, not a `*/` glob: chainsaw's --test-dir discovers chainsaw-test.yaml
# recursively at any depth, so a one-level glob would let a nested test run
# unchecked.
while IFS= read -r f; do
  # `|| true` guards against grep's exit-1-on-no-match under `set -e
  # -o pipefail`: without it, a file with zero suite labels aborts the loop
  # silently instead of reporting FAIL for it and continuing to the rest.
  value="$(grep -cE '^\s+suite:\s*(fast|full)\s*$' "$f" || true)"
  if [ "$value" -ne 1 ]; then
    echo "FAIL: $f has $value suite labels, expected exactly 1"
    fail=1
  fi
done < <(find e2e/tests -name chainsaw-test.yaml)
[ "$fail" -eq 0 ] && echo "OK: all chainsaw tests carry exactly one suite label"
exit "$fail"
