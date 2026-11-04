#!/usr/bin/env bash
# Smoke test for the Vinyl Cache image (Dockerfile.vinyl).
# Usage: hack/smoke-vinyl-image.sh <image>
# Runs the image as UID 65532 and checks that the daemon, jemalloc, the VCL
# compiler toolchain, the vmods and the stat/log tools all work.
set -euo pipefail

image="${1:?usage: $0 <image>}"

fail() { echo "FAIL: $*" >&2; exit 1; }

run() { docker run --rm --user 65532:65532 --entrypoint "" "$image" "$@"; }

# Capture output first, then match: never pipe into grep -q (SIGPIPE under pipefail).
out=$(run vinyld -V 2>&1) || fail "vinyld -V failed in $image: $out"
case "$out" in
  *vinyl-cache-9.1.0*) ;;
  *) fail "vinyld -V does not report vinyl-cache-9.1.0: $out" ;;
esac

out=$(run sh -c 'ldd "$(command -v vinyld)"' 2>&1) || fail "ldd vinyld failed: $out"
case "$out" in
  *libjemalloc*) ;;
  *) fail "vinyld is not linked against libjemalloc: $out" ;;
esac

vcl='vcl 4.1;
import std;
import directors;
import purge;
import xkey;
import uuid;
backend default { .host = "127.0.0.1"; .port = "8081"; }
sub vcl_recv { set req.http.X-Id = uuid.uuid_v4(); }'
out=$(docker run --rm --user 65532:65532 --entrypoint "" -e VCL="$vcl" "$image" \
  sh -c 'printf "%s\n" "$VCL" > /tmp/smoke.vcl && vinyld -C -f /tmp/smoke.vcl >/dev/null' 2>&1) \
  || fail "vinyld -C failed to compile the smoke VCL (std, directors, purge, xkey, uuid): $out"

for tool in vinylstat vinyllog; do
  out=$(run "$tool" -V 2>&1) || fail "$tool -V failed (missing shared library?): $out"
done

echo "OK: $image passed the Vinyl smoke test"
