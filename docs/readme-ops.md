# Ops Details

Detailed operational content moved from the top-level `README.md`.

## Layers

Layer selection:
- Shared infrastructure composition: `infrastructure/overlays/home/kustomization.yaml`
- Shared platform services composition: `platform-services/overlays/home/kustomization.yaml`
- Tenant environments: `tenants/app-envs`
- App composition: `tenants/apps/example` (deploys both `staging` and `prod`)
- Platform cert issuer selection: `flux-system/platform-settings` (`CERT_ISSUER`)

Layer boundaries:
- `clusters/home/` is the Flux cluster entrypoint layer (`flux-system`, source + stack Kustomizations)
- `infrastructure/` is shared cluster infra (networking, cert-manager, issuers, ingress/storage providers)
- `platform-services/` is shared platform service installs
- `tenants/` is app-environment scope (staging/prod namespaces + sample app)
- `platform-runtime-inputs` is Git-managed in `clusters/home/flux-system/sources/secret-platform-runtime-inputs.sops.yaml`
- `Makefile` is the operator API layer (sync + reconcile workflows)

## Use Cases

### Install / teardown

Install:

```bash
$EDITOR config.env
sops clusters/home/flux-system/sources/secret-platform-runtime-inputs.sops.yaml
make install
```

Teardown:

```bash
make teardown
```

Notes:
- `make teardown` is stack-only: deletes `homelab-flux-stack` and `homelab-flux-sources`
- Flux controllers, cert-manager, CRDs, and cluster-level services remain installed
- Shortcuts: `make install`, `make teardown`, `make reinstall`
- Host dynamic DNS: `make host-dns` / `make host-dns-delete` (or `./host/dynamic-dns.sh`)

### Cert mode

```bash
make platform-certs-staging
make platform-certs-prod
```

`platform-certs-*` targets update:
- `clusters/home/flux-system/sources/configmap-platform-settings.yaml` (`CERT_ISSUER=letsencrypt-staging|letsencrypt-prod`)

Contract:
- These targets edit local Git files only
- Commit + push first, then run `make flux-reconcile`

### Runtime secrets + ClickStack keys

After editing `clusters/home/flux-system/sources/secret-platform-runtime-inputs.sops.yaml`:

```bash
git add clusters/home/flux-system/sources/secret-platform-runtime-inputs.sops.yaml
git commit -m "runtime-inputs: update platform secrets"
git push
make runtime-inputs-sync
make flux-reconcile
```

If you changed ClickStack keys (`CLICKSTACK_INGESTION_KEY` and/or `CLICKSTACK_API_KEY`):

```bash
make runtime-inputs-refresh-otel
```

Important contracts:
- `SHARED_OIDC_CLIENT_ID` / `SHARED_OIDC_CLIENT_SECRET` are used by shared oauth2-proxy
- `OAUTH_HOST` defines the shared callback host (`https://${OAUTH_HOST}/oauth2/callback`)
- `OAUTH_COOKIE_SECRET` must be exactly 16, 24, or 32 characters
- `CLICKSTACK_API_KEY` is required when ClickStack/OTel are enabled
- `CLICKSTACK_INGESTION_KEY` is optional initially; when unset it falls back to `CLICKSTACK_API_KEY`

ClickStack first-login flow:
1. Open `https://${CLICKSTACK_HOST}` and complete first team/user setup.
2. In ClickStack UI, create a new ingestion key for OTel collectors.
3. Copy the ingestion key.
4. Update the Git-managed SOPS source:

```bash
sops clusters/home/flux-system/sources/secret-platform-runtime-inputs.sops.yaml
```

Set `data.CLICKSTACK_INGESTION_KEY` to the new value and save.

5. Commit, push, and apply:

```bash
git add clusters/home/flux-system/sources/secret-platform-runtime-inputs.sops.yaml
git commit -m "runtime-inputs: rotate clickstack ingestion key"
git push
make runtime-inputs-refresh-otel
```

6. Verify source/target secret alignment (without printing secret values):

```bash
src="$(kubectl -n flux-system get secret platform-runtime-inputs -o jsonpath='{.data.CLICKSTACK_INGESTION_KEY}')"
dst="$(kubectl -n logging get secret hyperdx-secret -o jsonpath='{.data.HYPERDX_API_KEY}')"
test -n "$src" && test "$src" = "$dst" && echo "OK: ingestion key propagated to logging/hyperdx-secret"
```

### Example app defaults

URL mapping:
- staging: `staging-hello.homelab.swhurl.com`
- prod: `hello.homelab.swhurl.com`

Certificate issuer mapping:
- staging: `letsencrypt-prod`
- prod: `letsencrypt-prod`

Detailed cert runbook: `docs/runbooks/promote-platform-certs-to-prod.md`

## Gotchas

1. k3s prerequisite: use default networking (`flannel`) with packaged `traefik` + `metrics-server` enabled.
2. Runtime inputs are Git-managed via SOPS: commit + push encrypted changes before `make runtime-inputs-sync` (or `make flux-reconcile`).
3. DNS wildcard scope: `*.homelab.swhurl.com` matches one-label hosts only; multi-label names need explicit records (or deeper wildcard). Add explicit hosts to `DYNAMIC_DNS_RECORDS` in `host/host.env`.
4. cert-manager issuance timing: first reconcile can fail until DNS propagates and ACME HTTP-01 checks can reach ingress.
5. ClickStack ingestion timing: OTLP ingestion is not fully active until initial team setup completes in UI.
6. OTel collector key reload: after key rotation, restart collectors (or use `make runtime-inputs-refresh-otel`) because `secretKeyRef` env values do not hot-reload in running pods.

## Defaults

Active composition assumes native k3s components:
- CNI: flannel
- ingress: Traefik
- metrics: metrics-server
- Traefik NodePorts pinned via `infrastructure/ingress-traefik/base/helmchartconfig-traefik.yaml`:
  - `80 -> 31514`
  - `443 -> 30313`
- Legacy optional manifests are under `legacy/infrastructure/*` and are not part of `infrastructure/overlays/home`

## Targets

- `make help`
- `make install`
- `make teardown`
- `make reinstall`
- `make flux-bootstrap`
- `make runtime-inputs-sync`
- `make runtime-inputs-refresh-otel`
- `make otel-collectors-restart`
- `make charts-generate`
- `make flux-reconcile`
- `make platform-certs-staging|platform-certs-prod`
- `make verify`

## Extra Docs

- `docs/runbook.md`
- `docs/orchestration-api.md`
- `docs/architecture.md`
