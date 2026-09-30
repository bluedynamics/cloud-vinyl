# VSL golden fixtures

Raw `varnishlog -g request` output recorded from varnish:8.0.2 by
`record-fixtures.sh`. Parser and span-builder tests assert against these
files. Never hand-edit a fixture; re-run the script (requires Docker) and
commit the diff together with whatever parser change made it necessary.

`record-fixtures-tracing.sh` records the P2 variants with the tracing VCL
active (VCL-minted `BereqHeader traceparent`); its embedded VCL mirrors the
generator templates' tracing blocks — keep them in sync.

`record-fixtures-truth.sh` records the P3 truth fixtures — `coalesce.txt`,
`grace_bgfetch.txt`, `esi.txt`, `restart.txt`, `retry.txt`, `pipe.txt`,
`synth.txt` — against varnish:8.0.2 driven through `truthbackend.py`, a
stdlib-only traffic-shaping backend (delay, short TTL, ESI markup, a
flaky-then-ok endpoint, and a teapot restart trigger) run in its own
container. Its embedded VCL keeps the same P2 tracing blocks verbatim, plus
scenario rules (pipe/synth/restart/retry/do_esi) that exist only in this
rig. `NOTES.md` is the authoritative shape catalog for these seven fixtures
— quoted `Link`/`Hit`/`Timestamp`/`BereqHeader traceparent` lines, group
nesting depths, and STOP-gate findings (e.g. ESI needing varnishd feature
`+esi_disable_xml_check`) that every later P3 task reconciles its code
against instead of guessing at VSL shapes.
