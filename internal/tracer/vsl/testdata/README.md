# VSL golden fixtures

Raw `varnishlog -g request` output recorded from varnish:8.0.2 by
`record-fixtures.sh`. Parser and span-builder tests assert against these
files. Never hand-edit a fixture; re-run the script (requires Docker) and
commit the diff together with whatever parser change made it necessary.
