# Runtime Inputs

This directory contains the final Kubernetes Secrets consumed by platform services. The files are SOPS-encrypted in Git and are decrypted directly by the `homelab-platform` Flux Kustomization.

Rendered target Secrets:

- `ingress/oauth2-proxy-shared-secret`
- `logging/hyperdx-secret`
- `observability/clickstack-runtime-inputs`

Non-secret runtime settings, such as `OAUTH_HOST`, live in `clusters/home/flux-system/sources/configmap-platform-settings.yaml` and are rendered by Flux post-build substitution.

## Conceptual Flow

There are two separate mechanisms:

1. SOPS encryption protects Secret manifests in Git.
2. Flux post-build substitution renders non-secret `${...}` placeholders from `platform-settings`.

End-to-end for Secrets:

1. The operator opens one of the `*.sops.yaml` files in this directory with `sops`.
2. Inside the editor, Secret values are plaintext. They come from an OAuth provider, ClickStack UI, or local random generation.
3. When the file is saved, SOPS writes encrypted `ENC[...]` ciphertext back to Git. SOPS generates the ciphertext, not the underlying Secret values.
4. `homelab-platform` decrypts these files in-cluster with `flux-system/sops-age`.
5. Flux applies the decrypted Kubernetes Secrets directly in the service namespaces.
6. Helm charts and workloads consume those final Secrets through their normal Kubernetes mechanisms.

There is no intermediate `platform-runtime-inputs` Secret. Workloads do not read the encrypted YAML files; they read the decrypted Kubernetes Secrets applied by Flux.

## SOPS And Age Keys

There is no SOPS password to remember. This repo uses SOPS with age keys.

- `.sops.yaml` contains an `age:` public recipient (`age1...`).
- Local editing requires the matching age private key.
- Put the private key in `~/.config/sops/age/keys.txt`, or run commands with `SOPS_AGE_KEY_FILE=./age.agekey`.
- Flux uses the same private key material from the `flux-system/sops-age` Secret.
- Keep `age.agekey` backed up and out of Git.

Generate or inspect age key material:

```bash
age-keygen -o age.agekey
age-keygen -y age.agekey
```

Edit a runtime Secret:

```bash
SOPS_AGE_KEY_FILE=./age.agekey sops platform-services/runtime-inputs/secret-oauth2-proxy-shared.sops.yaml
```

Commit only encrypted YAML.

## Runtime Input Details

### `OAUTH_HOST`

- Location: `clusters/home/flux-system/sources/configmap-platform-settings.yaml`
- Value source: operator-chosen public callback host, currently `oauth.homelab.swhurl.com`
- Render mechanism: Flux post-build substitution from `platform-settings`
- Rendered into: `platform-services/oauth2-proxy/base/helmrelease-oauth2-proxy-shared.yaml`
- Runtime consumer: oauth2-proxy HelmRelease values

This is not a Secret. Flux substitutes it directly into oauth2-proxy Helm values:

- `extraArgs.redirect-url`
- ingress `hosts`
- ingress TLS `hosts`

### oauth2-proxy Client Credentials

- File: `secret-oauth2-proxy-shared.sops.yaml`
- Rendered Secret: `ingress/oauth2-proxy-shared-secret`
- Runtime consumer: oauth2-proxy Helm chart via `config.existingSecret: oauth2-proxy-shared-secret`
- Consumption mechanism: Kubernetes Secret referenced by the Helm chart

Keys:

- `client-id`: OAuth/OIDC provider app client ID.
- `client-secret`: OAuth/OIDC provider app client secret.
- `cookie-secret`: locally generated oauth2-proxy cookie secret.

Generate a new cookie secret locally:

```bash
openssl rand -base64 32
```

### ClickStack API Key

- File: `secret-clickstack-runtime-inputs.sops.yaml`
- Rendered Secret: `observability/clickstack-runtime-inputs`
- Runtime consumer: ClickStack HelmRelease
- Consumption mechanism: Helm `valuesFrom`

The ClickStack HelmRelease reads the rendered Secret with:

```yaml
valuesFrom:
  - kind: Secret
    name: clickstack-runtime-inputs
    valuesKey: CLICKSTACK_API_KEY
    targetPath: hyperdx.apiKey
```

This is Helm `valuesFrom`, not a workload environment variable.

### ClickStack Ingestion Key For OTel

- File: `secret-hyperdx.sops.yaml`
- Rendered Secret: `logging/hyperdx-secret`
- Runtime consumer: OTel collector pods
- Consumption mechanism: `secretKeyRef` environment variable

Key:

- `HYPERDX_API_KEY`: ClickStack ingestion key created in the ClickStack UI after first-login setup.

During initial bootstrap, `HYPERDX_API_KEY` may temporarily match `CLICKSTACK_API_KEY`. After ClickStack first-login setup, create a real ingestion key in the ClickStack UI and update this Secret.

The OTel HelmReleases expose the rendered Secret as an environment variable:

```yaml
extraEnvs:
  - name: HYPERDX_API_KEY
    valueFrom:
      secretKeyRef:
        name: hyperdx-secret
        key: HYPERDX_API_KEY
```

The OTel collector config then references that runtime environment variable with an escaped expression:

```yaml
authorization: "$${env:HYPERDX_API_KEY}"
```

The double dollar is intentional. It prevents Flux from substituting `${env:HYPERDX_API_KEY}` and leaves the expression for the collector to resolve at runtime.

`secretKeyRef` environment variables do not hot-reload in running pods. After rotating `HYPERDX_API_KEY`, use:

```bash
make runtime-inputs-refresh-otel
```

## Update Workflow

Edit and commit the encrypted target Secret:

```bash
SOPS_AGE_KEY_FILE=./age.agekey sops platform-services/runtime-inputs/secret-hyperdx.sops.yaml
git add platform-services/runtime-inputs/secret-hyperdx.sops.yaml
git commit -m "runtime-inputs: update clickstack ingestion key"
git push
make runtime-inputs-sync
```

For ClickStack ingestion key updates, use the full refresh flow:

```bash
make runtime-inputs-refresh-otel
```
