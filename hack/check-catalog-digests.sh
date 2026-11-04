#!/usr/bin/env bash
# Every daemon and exporter image of the runtime catalog must be pinned by a
# multi-arch index digest that exists in its repository.
#
#   fail    : no "@sha256:" in the entry
#   fail    : <repo>@<digest> does not resolve (typo, or a digest of another repo)
#   warning : <repo>:<tag> now resolves to a different digest. Upstream tags such
#             as docker.io/library/varnish:9.1.0 are rebuilt for Debian fixes, so
#             this must not turn CI red; it is a hint to bump the pin.
#
# Tracer entries are skipped (empty in the chart, filled in by the release).
# Needs mikefarah yq v4 and docker buildx. Usage: check-catalog-digests.sh [values.yaml]
#
# No grep in a pipeline here: under `set -o pipefail`, `grep -q` at the end of a
# pipeline can make the producer die of SIGPIPE and flip the condition (see
# CLAUDE.md). Bash pattern matching has no such trap.
set -euo pipefail

values="${1:-charts/cloud-vinyl/values.yaml}"

# inspect <ref>: resolve a reference; manifest JSON lands in $inspect_out. Runs in
# the current shell (not a $(...) subshell) so $inspect_err survives. Retries
# rate limits, 5xx and network blips on shared runners, but not "not found". On
# final failure the registry's stderr is in $inspect_err, so callers can tell
# "not found" from "rate limited".
inspect_out=""
inspect_err=""
inspect() {
  local attempt
  for attempt in 1 2 3; do
    if inspect_out="$(docker buildx imagetools inspect "$1" --format '{{json .Manifest}}' 2>&1)"; then
      return 0
    fi
    inspect_err="$inspect_out"
    [[ "$inspect_err" == *"not found"* ]] && return 1
    [ "$attempt" -lt 3 ] && sleep $((attempt * 2))
  done
  return 1
}

fail=0
checked=0
while IFS= read -r ref; do
  [ -n "$ref" ] || continue
  checked=$((checked + 1))
  if [[ "$ref" != *@sha256:* ]]; then
    echo "FAIL: $ref has no @sha256: digest"
    fail=1
    continue
  fi
  pinned="${ref#*@}"
  repo_tag="${ref%@*}"
  # A tag may contain no "/" or "@"; the repo is everything before the last ":"
  # that follows the last "/".
  last="${repo_tag##*/}"
  if [[ "$last" == *:* ]]; then
    repo="${repo_tag%:*}"
  else
    repo="$repo_tag"
  fi
  if ! inspect "$repo@$pinned"; then
    echo "FAIL: $repo@$pinned does not resolve: $inspect_err"
    fail=1
    continue
  fi
  if [[ "$repo_tag" == "$repo" ]]; then
    echo "OK: $repo@$pinned (no tag to compare)"
    continue
  fi
  if ! inspect "$repo_tag"; then
    echo "::warning::could not look up $repo_tag to check for a newer build (pinned $pinned): $inspect_err"
    continue
  fi
  current="$(yq -p=json '.digest' <<<"$inspect_out")"
  if [ "$current" != "$pinned" ]; then
    echo "::warning::$repo_tag now points to $current, pinned is $pinned"
  else
    echo "OK: $repo_tag@$pinned"
  fi
done < <(yq '.runtimes[].versions[] | (.daemon, .exporter) | select(. != null) // ""' "$values")

if [ "$checked" -eq 0 ]; then
  echo "FAIL: no daemon/exporter entries found in $values"
  exit 1
fi
[ "$fail" -eq 0 ] && echo "OK: all $checked catalog images are pinned by a resolvable digest"
exit "$fail"
