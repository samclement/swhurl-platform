#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

ensure_context

APP_NS=apps
APP_NAME=hello-web
HOSTNAME="hello.${BASE_DOMAIN}"

if [[ "$DELETE" == true ]]; then
  kubectl delete -n "$APP_NS" ingress "$APP_NAME" --ignore-not-found
  kubectl delete -n "$APP_NS" certificate "$APP_NAME" --ignore-not-found || true
  kubectl delete -n "$APP_NS" deploy "$APP_NAME" svc "$APP_NAME" --ignore-not-found
  kubectl delete ns "$APP_NS" --ignore-not-found
  exit 0
fi

kubectl_ns "$APP_NS"

cat <<EOF | kubectl apply -f -
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ${APP_NAME}
  namespace: ${APP_NS}
spec:
  replicas: 1
  selector:
    matchLabels: { app: ${APP_NAME} }
  template:
    metadata:
      labels: { app: ${APP_NAME} }
    spec:
      containers:
        - name: web
          image: docker.io/nginx:1.25-alpine
          ports: [{ containerPort: 80 }]
---
apiVersion: v1
kind: Service
metadata:
  name: ${APP_NAME}
  namespace: ${APP_NS}
spec:
  selector: { app: ${APP_NAME} }
  ports:
    - port: 80
      targetPort: 80
      name: http
---
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: ${APP_NAME}
  namespace: ${APP_NS}
  annotations:
    kubernetes.io/ingress.class: nginx
    nginx.ingress.kubernetes.io/ssl-redirect: "true"
    nginx.ingress.kubernetes.io/force-ssl-redirect: "true"
    # Use in-cluster service for auth-url to avoid external TLS/DNS dependency
    $( [[ "${FEAT_OAUTH2_PROXY:-true}" == "true" ]] && echo "nginx.ingress.kubernetes.io/auth-url: http://oauth2-proxy.ingress.svc.cluster.local/oauth2/auth" )
    $( [[ "${FEAT_OAUTH2_PROXY:-true}" == "true" ]] && echo "nginx.ingress.kubernetes.io/auth-signin: https://${OAUTH_HOST}/oauth2/start?rd=\$scheme://\$host\$request_uri" )
spec:
  ingressClassName: nginx
  tls:
    - hosts: ["${HOSTNAME}"]
      secretName: ${APP_NAME}-tls
  rules:
    - host: "${HOSTNAME}"
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: ${APP_NAME}
                port:
                  number: 80
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${APP_NAME}
  namespace: ${APP_NS}
spec:
  secretName: ${APP_NAME}-tls
  issuerRef:
    name: ${CLUSTER_ISSUER}
    kind: ClusterIssuer
  dnsNames:
    - ${HOSTNAME}
EOF

wait_deploy "$APP_NS" "$APP_NAME"
log_info "Sample app deployed at https://${HOSTNAME}"
