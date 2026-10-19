#!/usr/bin/env bash
# Regenerate the app-template fixtures with the app generator (swhurl app-new). The test suite
# checks the committed fixtures match this output (Secret ciphertext excepted).
set -Eeuo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
root="${FIXTURE_ROOT:-tests/fixtures/apps}"
gen() { PYTHONPATH=tools uv run --frozen python -m swhurl app-new --root "$root" --no-register --team demo "$@"; }
gen smoke-worker --env staging --kind worker --image docker.io/library/busybox:1.37 --command 'sleep 3600000'
gen smoke-web --env staging --exposure authenticated-web --host smoke-web.homelab.swhurl.com \
  --image docker.io/nginxinc/nginx-unprivileged:1.27-alpine --uid 101 --health-path / \
  --secret-keys GREETING --issuer letsencrypt-staging --otlp
gen smoke-data --env staging --kind web --health-path / --uid 101 --database sqlite --database-size 64Mi --secret-keys GREETING \
  --image docker.io/nginxinc/nginx-unprivileged:1.27-alpine@sha256:65e3e85dbaed8ba248841d9d58a899b6197106c23cb0ff1a132b7bfe0547e4c0 \
  --command "sh -c 'date -u >> /data/log; exec nginx -g \"daemon off;\"'"
# Generate production through the same conversion as an operator, then retain only the prod fixture.
mkdir -p "$root/clusters/home"
printf 'resources: []\n' > "$root/clusters/home/kustomization.yaml"
PYTHONPATH=tools uv run --frozen python -c 'import sys; from pathlib import Path; from swhurl.apps.edit import promote; promote(Path(sys.argv[1]), "smoke-data")' "$root"
rm -r "$root/apps/smoke-data/staging"
rm "$root/clusters/home/app-smoke-data-staging.yaml" "$root/clusters/home/kustomization.yaml"
