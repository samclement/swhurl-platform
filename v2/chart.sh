#!/usr/bin/env bash
# Resolve/acquire the profile-pinned chart for checks and CI. Only the cache is written.
set -Eeuo pipefail
if [ "$#" -ne 0 ]; then
  echo 'usage: chart.sh (no arguments)' >&2
  exit 2
fi
cd "$(git rev-parse --show-toplevel)"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
(cd v2 && GOTOOLCHAIN=go1.26.0 GOPROXY=off GOSUMDB=off CGO_ENABLED=0 go build -o "$work/swhurl-v2" ./cmd/swhurl-v2)
"$work/swhurl-v2" chart --profile v2/testdata/profile.json \
  --repositories clusters/home/flux-system/sources/helmrepositories.yaml \
  --cache "${XDG_CACHE_HOME:-$HOME/.cache}/swhurl-platform/charts"
