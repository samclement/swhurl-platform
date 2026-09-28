# Onboard A New App With SOPS Secrets

This runbook explains where secrets should live when onboarding a new app, and gives a concrete example.

## Secret Placement Model

Use this split:

1. Shared platform/runtime secrets:
`platform-services/<service>/base/*.sops.yaml`
Use for Secrets consumed by shared platform services. Place each Secret with the service that consumes it. Non-secret shared settings belong in `clusters/home/flux-system/sources/configmap-platform-settings.yaml`.

2. App-specific secrets:
`tenants/apps/<app>/.../secret-*.sops.yaml`
Use for credentials owned by one app (API keys, DB URLs, webhook secrets).

If a secret is not shared by multiple services, keep it with the app.

## Example: onboard `weather-api` with an app-local Secret

1. Generate the instance with a Secret stub. The generator writes it already encrypted (`.sops.yaml` covers `tenants/apps/`), sets `spec.decryption` on the app's Flux unit, injects it with `envFrom`, and opts the workload into Reloader:

   ```bash
   make app-new NAME=weather-api ARGS="--env staging --image ghcr.io/me/weather-api:1.4.0 \
     --health-path /ready --secret-keys API_TOKEN,DB_URL"
   ```

2. Replace the `REPLACE_ME` values in your editor (needs the age key, for example `SOPS_AGE_KEY_FILE=./age.agekey`):

   ```bash
   SOPS_AGE_KEY_FILE=./age.agekey sops tenants/apps/weather-api/staging/secret.sops.yaml
   ```

3. Check, commit, push and reconcile:

   ```bash
   make app-policy
   git add tenants/apps/weather-api clusters/home platform-services/reloader
   git commit -m "apps: add weather-api staging" && git push
   make flux-reconcile
   ```

4. Verify without printing values:

   ```bash
   flux get kustomization homelab-app-weather-api-staging
   kubectl -n weather-api-staging get secret weather-api-secret -o json | jq '.data | keys'
   ```

Later value changes: edit with `sops`, commit, push, `make flux-reconcile`; Reloader restarts the workload.

## When To Use Platform Runtime Inputs

Use `platform-services/<service>/base/*.sops.yaml` only for Secrets consumed by shared platform components (for example oauth2-proxy credentials, ClickStack chart API keys, or OTel ingestion keys), not app-only credentials.
