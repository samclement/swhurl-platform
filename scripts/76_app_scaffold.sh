#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

NAME=""
HOST=""
NS="apps"
IMAGE="docker.io/nginx:1.25-alpine"
ISSUER="${CLUSTER_ISSUER:-selfsigned}"
USE_AUTH=true
DELETE=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --name) NAME="$2"; shift 2 ;;
    --host) HOST="$2"; shift 2 ;;
    --namespace) NS="$2"; shift 2 ;;
    --image) IMAGE="$2"; shift 2 ;;
    --issuer) ISSUER="$2"; shift 2 ;;
    --no-auth) USE_AUTH=false; shift ;;
    --delete) DELETE=true; shift ;;
    -h|--help)
      cat <<USAGE
Scaffold a simple app + Service + Ingress + Certificate.

Usage: $(basename "$0") --name APP --host FQDN [--namespace apps] [--image IMAGE] [--issuer NAME] [--no-auth] [--delete]

Examples:
  $(basename "$0") --name myapp --host myapp.
  $(basename "$0") --name public --host public. --no-auth
USAGE
      exit 0 ;;
    *) echo "Unknown arg: $1" >&2; exit 1 ;;
  esac
done

# Do nothing when orchestrator runs this without params
if [[ -z "$NAME" && "$DELETE" != true ]]; then
  log_info "76_app_scaffold: no --name/--host provided; skipping"
  exit 0
fi

ensure_context

if [[ -z "$NAME" || -z "$HOST" ]]; then
  echo "--name and --host are required" >&2
  exit 1
fi

TLS_SECRET="${NAME}-tls"

if [[ "$DELETE" == true ]]; then
  kubectl delete -n "$NS" ingress "$NAME" --ignore-not-found
  kubectl delete -n "$NS" certificate "$NAME" --ignore-not-found || true
  kubectl delete -n "$NS" deploy "$NAME" svc "$NAME" --ignore-not-found
  exit 0
fi

kubectl_ns "$NS"

TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT
cp "$SCRIPT_DIR/../infra/manifests/templates/app/"*.yaml "$TMPDIR/"

export APP_NAME="$NAME" APP_NS="$NS" HOST="$HOST" IMAGE TLS_SECRET="${TLS_SECRET}" ISSUER OAUTH_HOST
for f in "$TMPDIR"/*.yaml; do
  envsubst < "$f" > "$f.rendered"
  mv "$f.rendered" "$f"
done

cat > "$TMPDIR/kustomization.yaml" <<K
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - deployment.yaml
  - service.yaml
  - certificate.yaml
  - $( [[ "$USE_AUTH" == true ]] && echo ingress-auth.yaml || echo ingress-public.yaml )
K

kubectl apply -k "$TMPDIR"

wait_deploy "$NS" "$NAME"
log_info "App '${NAME}' deployed at https://${HOST}"
