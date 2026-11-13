#!/usr/bin/env bash
# Whether a change is only input records, so Validate checks it as the Render workflow will push it: rendered first
# (make -s v2-render), then check-repo, check-apps and the tests. Reads the changed paths NUL-separated on stdin
# (git diff --name-only -z) and prints true when there is at least one and every one is under v2-records/ or is a
# team's channel Secret (platform/notify/channels/channel-<team>.sops.yaml, which swhurl-v2 team writes). Anything
# else, or no paths, prints false: that change is checked as committed, so stale rendered files fail.
# Usage: git diff --name-only -z A B | .github/records-only.sh
set -Eeuo pipefail
channel='^platform/notify/channels/channel-[a-z]([a-z0-9-]{0,38}[a-z0-9])?\.sops\.yaml$'
any=false
while IFS= read -r -d '' path; do
  [ -n "$path" ] || continue
  if [[ "$path" != v2-records/?* && ! "$path" =~ $channel ]]; then
    echo false
    exit 0
  fi
  any=true
done
echo "$any"
