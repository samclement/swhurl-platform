# Onboard A New App With SOPS Secrets

This runbook explains where secrets should live when onboarding a new app, and gives a concrete example.

## Secret Placement Model

Use this split:

1. Shared platform/runtime secrets:
`clusters/home/flux-system/sources/secret-platform-runtime-inputs.sops.yaml`
Use for values shared across platform services or used as postBuild substitution inputs.

2. App-specific secrets:
`tenants/apps/<app>/.../secret-*.sops.yaml`
Use for credentials owned by one app (API keys, DB URLs, webhook secrets).

If a secret is not shared by multiple services, keep it with the app.

## Example: Onboard `weather-api` With App-Local Secret

This example creates a per-app secret consumed by one deployment.

## 1) Add app secret manifest in app path

Create `tenants/apps/weather-api/base/secret-weather-api.sops.yaml`:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: weather-api-runtime
  namespace: apps-staging
  labels:
    platform.swhurl.com/managed: "true"
type: Opaque
stringData:
  WEATHER_API_KEY: "<set-me>"
  DATABASE_URL: "<set-me>"
```

## 2) Ensure `.sops.yaml` covers app secret paths

Add a creation rule in `.sops.yaml` so app-local `*.sops.yaml` files encrypt automatically:

```yaml
creation_rules:
  - path_regex: clusters/home/flux-system/sources/.*\.sops\.ya?ml$
    encrypted_regex: '^(data|stringData)$'
    age: <your-age-recipient>
  - path_regex: tenants/apps/.*/.*\.sops\.ya?ml$
    encrypted_regex: '^(data|stringData)$'
    age: <your-age-recipient>
```

## 3) Encrypt the app secret file

```bash
sops --encrypt --in-place tenants/apps/weather-api/base/secret-weather-api.sops.yaml
```

## 4) Wire the secret into the app deployment

In `tenants/apps/weather-api/base/deployment-weather-api.yaml`:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: weather-api
  namespace: apps-staging
spec:
  template:
    spec:
      containers:
        - name: app
          image: ghcr.io/example/weather-api:1.0.0
          envFrom:
            - secretRef:
                name: weather-api-runtime
```

Add the secret to app base kustomization:

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - deployment-weather-api.yaml
  - service-weather-api.yaml
  - ingress-weather-api.yaml
  - secret-weather-api.sops.yaml
```

## 5) Enable SOPS decryption for the app Flux Kustomization

If the app path contains encrypted manifests, the app-level Flux Kustomization must include decryption.

Example `clusters/home/app-weather-api.yaml`:

```yaml
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: homelab-app-weather-api
  namespace: flux-system
spec:
  dependsOn:
    - name: homelab-tenants
  interval: 10m
  sourceRef:
    kind: GitRepository
    name: swhurl-platform
  path: ./tenants/apps/weather-api
  decryption:
    provider: sops
    secretRef:
      name: sops-age
  prune: true
  wait: true
  timeout: 20m
```

Add it to `clusters/home/kustomization.yaml` resources.

## 6) Commit, push, and reconcile

```bash
git add .sops.yaml tenants/apps/weather-api clusters/home/app-weather-api.yaml clusters/home/kustomization.yaml
git commit -m "apps(weather-api): onboard with app-local sops secret"
git push
make flux-reconcile
```

## 7) Verify

```bash
flux get kustomizations -A
kubectl -n apps-staging get secret weather-api-runtime
kubectl -n apps-staging get deploy weather-api
```

## When To Use The Central Platform Runtime Secret

Use `clusters/home/flux-system/sources/secret-platform-runtime-inputs.sops.yaml` only for shared keys that feed multiple platform components (for example shared OIDC settings or ClickStack ingestion values), not app-only credentials.
