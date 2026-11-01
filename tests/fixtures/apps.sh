#!/usr/bin/env bash
# Regenerate the app-template fixtures with the app generator (swhurl app-new). The test suite
# checks the committed fixtures match this output (Secret ciphertext excepted).
set -Eeuo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
root="${FIXTURE_ROOT:-tests/fixtures/apps}"
gen() { PYTHONPATH=tools uv run --frozen python -m swhurl app-new --root "$root" --team demo "$@"; }
gen smoke-worker --env staging --kind worker --image docker.io/library/busybox:1.37 --command 'sleep 3600000'
gen smoke-web --env staging --exposure authenticated-web --host smoke-web.homelab.swhurl.com \
  --image docker.io/nginxinc/nginx-unprivileged:1.27-alpine --uid 101 --health-path / \
  --secret-keys GREETING --issuer letsencrypt-staging --otlp
gen smoke-data --env staging --kind web --health-path / --uid 101 --database sqlite --database-size 64Mi --secret-keys GREETING \
  --image docker.io/nginxinc/nginx-unprivileged:1.27-alpine@sha256:65e3e85dbaed8ba248841d9d58a899b6197106c23cb0ff1a132b7bfe0547e4c0 \
  --command "sh -c 'date -u >> /data/log; exec nginx -g \"daemon off;\"'"
# Production has no v1 writer now: the committed smoke-data/prod fixture stays frozen, and the generator test
# compares the staging fixtures only. v2's create path replaces this fixture in phase 4.
