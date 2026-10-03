#!/usr/bin/env bash
# Regenerate the app-template fixtures with the app generator (swhurl app-new). The test suite
# checks the committed fixtures match this output (Secret ciphertext excepted).
set -Eeuo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
root="${FIXTURE_ROOT:-tests/fixtures/apps}"
gen() { PYTHONPATH=tools uv run --frozen python -m swhurl app-new --root "$root" --no-register "$@"; }
gen smoke-worker --env staging --kind worker --image docker.io/library/busybox:1.37 --command 'sleep 3600000'
gen smoke-web --env staging --exposure authenticated-web --host smoke-web.homelab.swhurl.com \
  --image docker.io/nginxinc/nginx-unprivileged:1.27-alpine --uid 101 --health-path / \
  --secret-keys GREETING --issuer letsencrypt-staging --otlp
gen smoke-data --env staging --kind worker --database sqlite --database-size 64Mi \
  --image docker.io/library/busybox:1.37@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e \
  --command 'sh -c "date -u >> /data/log; exec sleep 3600000"'
# Generate production through the same conversion as an operator, then retain only the prod fixture.
mkdir -p "$root/clusters/home"
printf 'resources: []\n' > "$root/clusters/home/kustomization.yaml"
PYTHONPATH=tools uv run --frozen python -c 'import sys; from pathlib import Path; from swhurl.apps.edit import promote; promote(Path(sys.argv[1]), "smoke-data")' "$root"
rm -r "$root/apps/smoke-data/staging"
rm "$root/clusters/home/app-smoke-data-staging.yaml" "$root/clusters/home/kustomization.yaml"
