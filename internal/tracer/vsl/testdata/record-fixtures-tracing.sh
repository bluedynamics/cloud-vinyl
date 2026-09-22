#!/usr/bin/env bash
# Records P2 VSL fixtures: varnish 8.0.2 WITH the tracing VCL active, so
# BereqHeader carries a VCL-minted traceparent. The vcl below mirrors the
# tracing blocks of internal/generator/templates/{vcl_recv,vcl_backend_fetch}
# .vcl.tmpl — keep them in sync when the templates change.
set -euo pipefail
cd "$(dirname "$0")"

NET=vsl-fixtures-p2
TP='00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01'

cleanup() {
  docker rm -f vslp2-varnish vslp2-backend >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
  rm -f default.vcl
}
trap cleanup EXIT
cleanup

cat > default.vcl <<'EOF'
vcl 4.1;
import std;
import uuid;
backend default { .host = "vslp2-backend"; .port = "8000"; }
sub vcl_recv {
    if (req.http.traceparent &&
        req.http.traceparent !~ "^[0-9a-f]{2}-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$") {
        unset req.http.traceparent;
    }
    if (req.http.traceparent ~ "^[0-9a-f]{2}-0{32}-" ||
        req.http.traceparent ~ "-0{16}-[0-9a-f]{2}$") {
        unset req.http.traceparent;
    }
}
sub vcl_backend_fetch {
    if (bereq.http.traceparent) {
        set bereq.http.traceparent = regsub(bereq.http.traceparent,
            "^([0-9a-f]{2}-[0-9a-f]{32}-)[0-9a-f]{16}(-[0-9a-f]{2})$",
            "\1" + std.tolower(regsub(uuid.uuid_v4(),
                "^(........)-(....)-(....).*$", "\1\2\3")) + "\2");
    } else {
        set bereq.http.traceparent = "00-" +
            std.tolower(regsuball(uuid.uuid_v4(), "-", "")) + "-" +
            std.tolower(regsub(uuid.uuid_v4(),
                "^(........)-(....)-(....).*$", "\1\2\3")) + "-01";
    }
    return(fetch);
}
EOF

docker network create "$NET" >/dev/null
docker run -d --name vslp2-backend --network "$NET" \
    python:3.12-slim python -m http.server 8000 >/dev/null
docker run -d --name vslp2-varnish --network "$NET" \
    -v "$PWD/default.vcl:/etc/varnish/default.vcl:ro" \
    varnish:8.0.2 >/dev/null
sleep 2

req() { # req <path> [traceparent]
  if [ $# -gt 1 ]; then
    docker run --rm --network "$NET" python:3.12-slim python -c "
import urllib.request, urllib.error
r = urllib.request.Request('http://vslp2-varnish$1', headers={'traceparent': '$2'})
try:
    urllib.request.urlopen(r).read()
except urllib.error.HTTPError:
    pass"
  else
    docker run --rm --network "$NET" python:3.12-slim python -c "
import urllib.request, urllib.error
try:
    urllib.request.urlopen('http://vslp2-varnish$1').read()
except urllib.error.HTTPError:
    pass"
  fi
}

record() { docker exec vslp2-varnish varnishlog -g request -d > "$1"; }

req /rewrite "$TP";                 record rewrite_miss.txt
docker restart vslp2-varnish >/dev/null; sleep 2
req /selfroot;                      record rewrite_selfroot.txt

echo "Recorded: rewrite_miss.txt rewrite_selfroot.txt"
