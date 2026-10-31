#!/usr/bin/env bash
# Every file v2 derives from the records must be what Git holds: the resolution (v2-records/calls.json), each
# instance's compiled folder and unit (v2-apps/ and clusters/home/app-*.json), the egress allocation and the
# proxy's two files, the auth folder, NATS's user list and Reloader's watch list. `render --check` renders them
# from the records in the working tree and compares, so an uncommitted record change counts; it writes nothing.
# make check-v2 runs it, which is how CI runs it. How to re-render: docs/services.md#egress-proxy.
set -Eeuo pipefail

cd "$(git rev-parse --show-toplevel)"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

(cd v2 && GOTOOLCHAIN=go1.26.0 GOPROXY=off GOSUMDB=off CGO_ENABLED=0 go build -o "$work/swhurl-v2" ./cmd/swhurl-v2)
if ! "$work/swhurl-v2" render --records v2-records --profile v2/testdata/profile.json --out . --check; then
  echo "not what the records render (docs/services.md#egress-proxy): render them from the working tree, review"
  echo "the diff and commit it; do not edit a derived file by hand."
  exit 1
fi
