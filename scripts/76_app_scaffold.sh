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

AUTH_URL_ANNOTATION=""
AUTH_SIGNIN_ANNOTATION=""
if [[ "$USE_AUTH" == true ]]; then
  AUTH_URL_ANNOTATION="nginx.ingress.kubernetes.io/auth-url: http://oauth2-proxy.ingress.svc.cluster.local/oauth2/auth"
  AUTH_SIGNIN_ANNOTATION="nginx.ingress.kubernetes.io/auth-signin: https://${OAUTH_HOST}/oauth2/start?rd=\$scheme://\$host\$request_uri"
fi

cat <<EOF | kubectl apply -f -
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ${NAME}
  namespace: ${NS}
spec:
  replicas: 1
  selector:
    matchLabels: { app: ${NAME} }
  template:
    metadata:
      labels: { app: ${NAME} }
    spec:
      containers:
        - name: web
          image: ${IMAGE}
          ports: [{ containerPort: 80 }]
---
apiVersion: v1
kind: Service
metadata:
  name: ${NAME}
  namespace: ${NS}
spec:
  selector: { app: ${NAME} }
  ports:
    - port: 80
      targetPort: 80
      name: http
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${NAME}
  namespace: ${NS}
spec:
  secretName: ${TLS_SECRET}
  issuerRef:
    name: ${ISSUER}
    kind: ClusterIssuer
  dnsNames:
    - ${HOST}
---
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: ${NAME}
  namespace: ${NS}
  annotations:
    kubernetes.io/ingress.class: nginx
    nginx.ingress.kubernetes.io/ssl-redirect: "true"
    nginx.ingress.kubernetes.io/force-ssl-redirect: "true"
    ${AUTH_URL_ANNOTATION}
    ${AUTH_SIGNIN_ANNOTATION}
spec:
  ingressClassName: nginx
  tls:
    - hosts: ["${HOST}"]
      secretName: ${TLS_SECRET}
  rules:
    - host: "${HOST}"
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: ${NAME}
                port:
                  number: 80
EOF

wait_deploy "$NS" "$NAME"
log_info "App '${NAME}' deployed at https://${HOST}"

