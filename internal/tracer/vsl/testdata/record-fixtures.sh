#!/usr/bin/env bash
# Records golden VSL fixtures for the vinyl-tracer parser tests from a real
# varnish:8.0.2. Re-run when bumping the varnish baseline; commit the diff.
set -euo pipefail
cd "$(dirname "$0")"

NET=vsl-fixtures
TP='00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01'

cleanup() {
  docker rm -f vsl-varnish vsl-backend >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
  rm -f default.vcl
}
trap cleanup EXIT
cleanup

cat > default.vcl <<'EOF'
vcl 4.1;
backend default { .host = "vsl-backend"; .port = "8000"; }
sub vcl_recv {
    if (req.url ~ "^/pass") { return (pass); }
}
EOF

docker network create "$NET" >/dev/null
docker run -d --name vsl-backend --network "$NET" \
    python:3.12-slim python -m http.server 8000 >/dev/null
docker run -d --name vsl-varnish --network "$NET" \
    -v "$PWD/default.vcl:/etc/varnish/default.vcl:ro" \
    varnish:8.0.2 >/dev/null
sleep 2

req() { # req <path> [extra header]
  # The backend has no files, so every request gets a 404; urlopen() raises
  # HTTPError on non-2xx. We only need the request logged by varnish, not a
  # successful fetch, so the 404 is caught and ignored.
  if [ $# -gt 1 ]; then
    docker run --rm --network "$NET" python:3.12-slim python -c "
import urllib.error, urllib.request
r = urllib.request.Request('http://vsl-varnish$1', headers={'traceparent': '$2'})
try:
    urllib.request.urlopen(r).read()
except urllib.error.HTTPError:
    pass"
  else
    docker run --rm --network "$NET" python:3.12-slim python -c "
import urllib.error, urllib.request
try:
    urllib.request.urlopen('http://vsl-varnish$1').read()
except urllib.error.HTTPError:
    pass"
  fi
}

record() { # record <file>: drain processed log records, then truncate live log
  docker exec vsl-varnish varnishlog -g request -d > "$1"
}

req /miss-hit "$TP";  req /miss-hit "$TP";  record miss_then_hit.txt
docker restart vsl-varnish >/dev/null; sleep 2
req /plain;                                 record no_traceparent.txt
docker restart vsl-varnish >/dev/null; sleep 2
req /garbage "not-a-traceparent";           record invalid_traceparent.txt
docker restart vsl-varnish >/dev/null; sleep 2
req /pass "$TP";                            record pass.txt

echo "Recorded: miss_then_hit.txt no_traceparent.txt invalid_traceparent.txt pass.txt"
