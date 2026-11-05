#!/usr/bin/env bash
# Every daemon, exporter and tracer image of the runtime catalog must carry an
# "@sha256:" digest. An empty or missing entry counts as unpinned. The release
# workflow runs this on the packaged values.yaml, after it has written the tracer
# digests, so a chart with a floating image is never published.
#
# Offline: this only inspects the text. That the digests exist in their
# repositories is check-catalog-digests.sh's job.
# Needs mikefarah yq v4. Usage: check-catalog-pinned.sh [values.yaml]
#
# No grep in a pipeline here: under `set -o pipefail`, `grep -q` at the end of a
# pipeline can make the producer die of SIGPIPE and flip the condition (see
# CLAUDE.md). Bash pattern matching has no such trap.
set -euo pipefail

values="${1:-charts/cloud-vinyl/values.yaml}"

fail=0
checked=0
# One line per image: "<flavor> <version> <field> <ref>". A missing field yields
# an empty ref, so it is reported as unpinned instead of being skipped.
while IFS=' ' read -r flavor version field ref; do
  [ -n "$flavor" ] || continue
  checked=$((checked + 1))
  if [[ "$ref" != *@sha256:?* ]]; then
    echo "FAIL: runtimes.$flavor.versions.\"$version\".$field is not pinned by digest: '${ref:-<empty>}'"
    fail=1
  fi
done < <(yq '.runtimes | to_entries | .[] | .key as $f | .value.versions | to_entries | .[] | .key as $v | .value as $e | ["daemon", "exporter", "tracer"] | .[] | $f + " " + $v + " " + . + " " + ($e[.] // "")' "$values")

if [ "$checked" -eq 0 ]; then
  echo "FAIL: no catalog entries found in $values"
  exit 1
fi
[ "$fail" -eq 0 ] && echo "OK: all $checked catalog images are pinned by digest"
exit "$fail"
