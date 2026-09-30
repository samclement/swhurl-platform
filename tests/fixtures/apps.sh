#!/usr/bin/env bash
# Regenerate the app-template fixtures with the app generator (swhurl app-new). The test suite
# checks the committed fixtures match this output (Secret ciphertext excepted).
set -Eeuo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
root="${FIXTURE_ROOT:-tests/fixtures/apps}"
gen() { PYTHONPATH=tools python3 -m swhurl app-new --root "$root" --no-register "$@"; }
gen smoke-worker --env staging --kind worker --image docker.io/library/busybox:1.37 --command 'sleep 3600000'
gen smoke-web --env staging --exposure authenticated-web --host smoke-web.homelab.swhurl.com \
  --image docker.io/nginxinc/nginx-unprivileged:1.27-alpine --uid 101 --health-path / \
  --secret-keys GREETING --issuer letsencrypt-staging --otlp
gen smoke-data --env prod --kind worker --persistence 64Mi \
  --image docker.io/library/busybox:1.37@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e \
  --command 'sh -c "date -u >> /data/log; exec sleep 3600000"'
