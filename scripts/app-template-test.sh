#!/usr/bin/env bash
# Deploy the generated app fixtures (tests/fixtures/apps, from the pushed Git
# revision) through real Flux units, check them, and remove them:
#   smoke-worker-staging  worker: runs, has no Service or Ingress
#   smoke-web-staging     authenticated web: SOPS Secret decrypted by its own
#                         unit and injected, route redirects to sign-in
#   smoke-data-prod       persistent, digest-pinned: claim on local-path-retain,
#                         data written
# Touches only the homelab-app-smoke-* units, their namespaces and PVs.
set +x
set -Eeuo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FIXTURES=tests/fixtures/apps
INSTANCES=(smoke-worker-staging smoke-web-staging smoke-data-prod)
HOST=smoke-web.homelab.swhurl.com

if [[ "${DRY_RUN:-false}" == "true" ]]; then
  sed -n '2,9p' "$0"
  exit 0
fi
for cmd in kubectl flux jq curl python3; do
  command -v "$cmd" >/dev/null 2>&1 || { echo "[ERROR] Missing required command: $cmd" >&2; exit 1; }
done
for i in "${INSTANCES[@]}"; do
  if kubectl get namespace "$i" >/dev/null 2>&1 || kubectl -n flux-system get kustomization "homelab-app-$i" >/dev/null 2>&1; then
    echo "[ERROR] $i already exists; clean up first" >&2; exit 1
  fi
done

fail=0
ok() { printf "[OK] %s\n" "$1"; }
bad() { printf "[BAD] %s\n" "$1"; fail=1; }

cleanup() {
  for i in "${INSTANCES[@]}"; do
    kubectl -n flux-system delete kustomization "homelab-app-$i" --ignore-not-found --wait=true >/dev/null 2>&1 || true
    kubectl delete namespace "$i" --ignore-not-found --wait=true --timeout=3m >/dev/null 2>&1 || true
    for pv in $(kubectl get pv -o json | jq -r --arg ns "$i" '.items[] | select(.spec.claimRef.namespace == $ns) | .metadata.name'); do
      kubectl patch pv "$pv" -p '{"spec":{"persistentVolumeReclaimPolicy":"Delete"}}' >/dev/null 2>&1 || true
      kubectl wait --for=delete "pv/$pv" --timeout=2m >/dev/null 2>&1 || true
    done
  done
  echo "[INFO] Cleaned up app-template test instances"
}
trap cleanup EXIT

# Apply each fixture's generated Flux unit, pointing it at the fixture path in Git.
for i in "${INSTANCES[@]}"; do
  python3 - "$ROOT/$FIXTURES/clusters/home/app-$i.yaml" "$FIXTURES" <<'PY' | kubectl apply -f - >/dev/null
import sys, yaml
unit = yaml.safe_load(open(sys.argv[1]))
unit['spec']['path'] = './' + sys.argv[2] + unit['spec']['path'][1:]
print(yaml.safe_dump(unit))
PY
done
for i in "${INSTANCES[@]}"; do
  if kubectl -n flux-system wait --for=condition=Ready "kustomization/homelab-app-$i" --timeout=10m >/dev/null; then
    ok "$i unit Ready (HelmRelease installed, workload healthy)"
  else
    bad "$i unit not Ready: $(kubectl -n flux-system get kustomization "homelab-app-$i" -o jsonpath='{.status.conditions[0].message}')"
  fi
done

ns=smoke-worker-staging
[[ -z "$(kubectl -n $ns get service,ingress -o name)" ]] && ok "worker has no Service or Ingress" || bad "worker exposes a Service or Ingress"

ns=smoke-web-staging
value="$(kubectl -n $ns get secret smoke-web-secret -o jsonpath='{.data.GREETING}' | base64 --decode)"
[[ "$value" == "REPLACE_ME" ]] && ok "SOPS Secret decrypted by the app's own Flux unit" || bad "Secret not decrypted"
[[ "$(kubectl -n $ns exec deploy/smoke-web -- printenv GREETING)" == "REPLACE_ME" ]] && ok "Secret injected into the container" || bad "Secret not injected"
[[ "$(kubectl -n $ns get deploy smoke-web -o jsonpath='{.spec.template.spec.securityContext.runAsNonRoot} {.spec.template.spec.automountServiceAccountToken}')" == "true false" ]] \
  && ok "web pod runs non-root without a service-account token" || bad "web pod security defaults missing"
location=""
for _ in $(seq 1 30); do
  location="$(curl -sk -o /dev/null -w '%{http_code} %{redirect_url}' "https://$HOST/")"
  [[ "$location" == 302\ https://accounts.google.com/* ]] && break
  sleep 5
done
[[ "$location" == 302\ https://accounts.google.com/* ]] && ok "https://$HOST redirects to Google sign-in" || bad "route not protected by sign-in: $location"

ns=smoke-data-prod
[[ "$(kubectl -n $ns get pvc -o jsonpath='{.items[0].spec.storageClassName} {.items[0].status.phase}')" == "local-path-retain Bound" ]] \
  && ok "claim bound on local-path-retain" || bad "claim not bound on local-path-retain"
kubectl -n $ns exec deploy/smoke-data -- test -s /data/log && ok "persistent volume written" || bad "no data written"
[[ "$(kubectl -n $ns get deploy smoke-data -o jsonpath='{.spec.template.spec.containers[0].image}')" == *@sha256:* ]] \
  && ok "prod image pinned by digest" || bad "prod image not digest-pinned"

(( fail )) && exit 1
printf "\nApp template test passed.\n"
