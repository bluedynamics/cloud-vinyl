#!/usr/bin/env bash
# Smoke test for the Vinyl Cache image (Dockerfile.vinyl).
# Usage: hack/smoke-vinyl-image.sh <image>
# Runs the image as UID 65532 and checks that the daemon, jemalloc, the VCL
# compiler toolchain, the vmods and the stat/log tools all work.
set -euo pipefail

image="${1:?usage: $0 <image>}"

# The expected version comes from the Dockerfile, so a version bump is a single
# edit there and the smoke test cannot drift from what the image is built from.
dockerfile="$(dirname "$0")/../Dockerfile.vinyl"
version=$(sed -n 's/^ARG VINYL_VERSION=//p' "$dockerfile")
[ -n "$version" ] || { echo "FAIL: no 'ARG VINYL_VERSION=' default in $dockerfile" >&2; exit 1; }

fail() { echo "FAIL: $*" >&2; exit 1; }

run() { docker run --rm --user 65532:65532 --entrypoint "" "$image" "$@"; }

# Capture output first, then match: never pipe into grep -q (SIGPIPE under pipefail).
out=$(run vinyld -V 2>&1) || fail "vinyld -V failed in $image: $out"
case "$out" in
  *"vinyl-cache-${version}"*) ;;
  *) fail "vinyld -V does not report vinyl-cache-${version}: $out" ;;
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
# vinyld -C makes a throwaway -n (mkdtemp under /tmp) and exits.
out=$(docker run --rm --user 65532:65532 --entrypoint "" -e VCL="$vcl" "$image" \
  sh -c 'printf "%s\n" "$VCL" > /tmp/smoke.vcl && vinyld -C -f /tmp/smoke.vcl >/dev/null' 2>&1) \
  || fail "vinyld -C failed to compile the smoke VCL (std, directors, purge, xkey, uuid): $out"

for tool in vinylstat vinyllog; do
  out=$(run "$tool" -V 2>&1) || fail "$tool -V failed (missing shared library?): $out"
done

# Start the daemon the way the pod does: read-only root, /var/lib/varnish and
# /tmp as fresh tmpfs (the operator mounts emptyDirs there, hiding anything the
# image put in those paths), then read the live VSM with vinylstat.
out=$(docker run --rm --read-only --user 65532:65532 \
  --tmpfs /var/lib/varnish:exec,mode=0777 --tmpfs /tmp:exec,mode=1777 \
  --entrypoint "" "$image" sh -c '
    printf "vcl 4.1;\nbackend default none;\n" > /tmp/d.vcl
    vinyld -f /tmp/d.vcl -n /var/lib/varnish/varnishd -a :8080 -j none -s malloc,16m
    sleep 2
    vinylstat -1 -n /var/lib/varnish/varnishd -f MAIN.uptime' 2>&1) \
  || fail "vinyld did not start with a read-only root and tmpfs /var/lib/varnish (pod conditions): $out"
case "$out" in
  *MAIN.uptime*) ;;
  *) fail "vinylstat could not read MAIN.uptime from the running daemon: $out" ;;
esac

echo "OK: $image passed the Vinyl smoke test"
