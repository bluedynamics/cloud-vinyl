#!/usr/bin/env bash
# Records P3 truth fixtures: varnish 8.0.2 driven through a traffic-shaping
# backend (truthbackend.py) under coalescing, grace/bgfetch, ESI, restart,
# retry, pipe, and synth conditions. The vcl_recv traceparent-validation
# block and the vcl_backend_fetch minting block below are the P2 script's
# blocks verbatim (record-fixtures-tracing.sh) — keep them in sync with
# internal/generator/templates/{vcl_recv,vcl_backend_fetch}.vcl.tmpl. The
# scenario rules (pipe/synth/restart/retry/do_esi) are rig-only per the P3
# plan's global constraint; they exist ONLY here unless a later task
# explicitly adds them to a template.
set -euo pipefail
cd "$(dirname "$0")"

NET=vsl-fixtures-p3

cleanup() {
  docker rm -f vslp3-varnish vslp3-backend >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
  rm -f default.vcl
}
trap cleanup EXIT
cleanup

cat > default.vcl <<'EOF'
vcl 4.1;
import std;
import uuid;
backend default { .host = "vslp3-backend"; .port = "8000"; }
sub vcl_recv {
    if (req.http.traceparent &&
        req.http.traceparent !~ "^[0-9a-f]{2}-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$") {
        unset req.http.traceparent;
    }
    if (req.http.traceparent ~ "^[0-9a-f]{2}-0{32}-" ||
        req.http.traceparent ~ "-0{16}-[0-9a-f]{2}$" ||
        req.http.traceparent ~ "^ff-") {
        unset req.http.traceparent;
    }
    if (req.url ~ "^/pipe") { return (pipe); }
    if (req.url ~ "^/synth") { return (synth(410, "gone")); }
}
sub vcl_deliver {
    # restart once when the backend serves a teapot
    if (resp.status == 418 && req.restarts == 0) { return (restart); }
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
sub vcl_backend_response {
    if (beresp.http.Surrogate-Control ~ "ESI/1.0") {
        set beresp.do_esi = true;
    }
    if (beresp.status == 500 && bereq.retries == 0) { return (retry); }
    if (beresp.ttl <= 0s) { set beresp.ttl = 60s; }
    set beresp.grace = 1h;
}
EOF

docker network create "$NET" >/dev/null
docker run -d --name vslp3-backend --network "$NET" \
    -v "$PWD/truthbackend.py:/b.py:ro" \
    python:3.12-slim python /b.py >/dev/null
docker run -d --name vslp3-varnish --network "$NET" \
    -v "$PWD/default.vcl:/etc/varnish/default.vcl:ro" \
    varnish:8.0.2 -p feature=+esi_disable_xml_check >/dev/null
sleep 2

req() { # req <path> [traceparent]
  if [ $# -gt 1 ]; then
    docker run --rm --network "$NET" python:3.12-slim python -c "
import urllib.request, urllib.error
r = urllib.request.Request('http://vslp3-varnish$1', headers={'traceparent': '$2'})
try:
    urllib.request.urlopen(r).read()
except urllib.error.HTTPError:
    pass"
  else
    docker run --rm --network "$NET" python:3.12-slim python -c "
import urllib.request, urllib.error
try:
    urllib.request.urlopen('http://vslp3-varnish$1').read()
except urllib.error.HTTPError:
    pass"
  fi
}

req2parallel() { # two concurrent requests to the same path
  req "$1" & req "$1" & wait
}

record() { docker exec vslp3-varnish varnishlog -g request -d > "$1"; }

docker restart vslp3-varnish >/dev/null; sleep 2
req2parallel /slow;                               record coalesce.txt

docker restart vslp3-varnish >/dev/null; sleep 2
# grace: prime, let TTL lapse, hit stale (triggers bgfetch), settle
req /shortttl; sleep 2; req /shortttl; sleep 1;   record grace_bgfetch.txt

docker restart vslp3-varnish >/dev/null; sleep 2
req /esi;                                         record esi.txt

docker restart vslp3-varnish >/dev/null; sleep 2
req /teapot;                                      record restart.txt

docker restart vslp3-varnish >/dev/null; sleep 2
req /flaky;                                       record retry.txt

docker restart vslp3-varnish >/dev/null; sleep 2
req /pipe;                                        record pipe.txt

docker restart vslp3-varnish >/dev/null; sleep 2
req /synth;                                       record synth.txt

echo "Recorded: coalesce.txt grace_bgfetch.txt esi.txt restart.txt retry.txt pipe.txt synth.txt"
