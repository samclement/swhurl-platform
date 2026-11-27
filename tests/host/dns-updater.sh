#!/usr/bin/env bash
# Runs host/aws-dns-updater.sh against fake curl and aws (tests/fixtures/dns):
# a failed address lookup must stop before any Route 53 call.
set -Eeuo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
failures=0

# run <name> <curl outcome> <old ip> <want exit> <want aws calls: none|read|write>
run() {
  local name="$1" curl="$2" old_ip="$3" want_exit="$4" want_aws="$5" got_exit=0 got_aws=none
  export FAKE_LOG="$work/$name.log" FAKE_CURL="$curl" FAKE_OLD_IP="$old_ip"
  : >"$FAKE_LOG"
  PATH="$root/tests/fixtures/dns:$PATH" bash "$root/host/aws-dns-updater.sh" >"$work/$name.out" 2>&1 || got_exit=$?
  grep -q '^aws route53 list-resource-record-sets' "$FAKE_LOG" && got_aws='read'
  grep -q '^aws route53 change-resource-record-sets' "$FAKE_LOG" && got_aws='write'
  if [[ "$got_exit" != "$want_exit" || "$got_aws" != "$want_aws" ]]; then
    echo "FAIL $name: exit $got_exit (want $want_exit), aws $got_aws (want $want_aws)"
    sed 's/^/  /' "$work/$name.out" "$FAKE_LOG"
    failures=$((failures + 1))
  else
    echo "ok   $name"
  fi
}

run changed-ip ok 198.51.100.1 0 write
run unchanged-ip ok 203.0.113.7 0 read
run http-error http-error 198.51.100.1 1 none
run timeout timeout 198.51.100.1 1 none
run not-an-ip not-an-ip 198.51.100.1 1 none

# The lookup is HTTPS, fails on an HTTP error status and is time-bounded.
for arg in --fail --max-time https://checkip.amazonaws.com; do
  if ! grep -q -- "^curl .*$arg" "$work/changed-ip.log"; then
    echo "FAIL curl-args: curl was not called with $arg"
    failures=$((failures + 1))
  fi
done

(( failures == 0 )) || { echo "$failures failure(s)"; exit 1; }
