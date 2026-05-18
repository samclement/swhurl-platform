# Add Feature Checklist

Use this checklist when adding a new platform feature.

## 1) Declarative wiring (Flux)

- Add/update component manifests under `infrastructure/*`, `platform-services/*`, or `tenants/*`.
- Add/update Flux stack wiring in `clusters/home/{infrastructure,platform,tenants}.yaml` and layer overlay kustomizations.
- Keep `dependsOn` explicit.

## 2) Config and contracts

- Default model: prefer declarative composition and runtime-input wiring over new `FEAT_*` switches.
- Keep non-secrets in `config.env`.
- Keep shared platform runtime Secrets as final SOPS Secret manifests next to the platform service that consumes them.
- Keep app-specific secrets in app paths (`tenants/apps/<app>/.../secret-*.sops.yaml`).
- Update `make verify-config` and `scripts/verify-platform.sh` when feature contracts change.
- If a new feature switch is absolutely necessary, document it in `docs/orchestration-api.md` and keep scope narrow (current default switch is `FEAT_VERIFY`).

## 3) Runtime inputs (if feature needs secrets)

- Shared platform secret flow:
  - Add/update final `*.sops.yaml` Secret manifests in the relevant `platform-services/<service>/base`.
  - Keep `clusters/home/platform.yaml` decryption contract (`spec.decryption.secretRef.name=sops-age`) valid.
- App-specific secret flow:
  - Add app-local `secret-*.sops.yaml` under `tenants/apps/<app>/...`.
  - Ensure app Flux Kustomization includes `spec.decryption.provider=sops` with `secretRef.name=sops-age`.
  - See `docs/runbooks/onboard-app-with-sops-secrets.md`.

## 4) Verification updates

- `make verify-config` (Makefile inline check)
- `scripts/verify-platform.sh`

## 5) Documentation

- `README.md`
- `docs/runbook.md`
- `AGENTS.md`

## 6) Validation before PR

- `bash -n scripts/*.sh host/dynamic-dns.sh`
- `make install DRY_RUN=true`
- `make teardown DRY_RUN=true`
- Optional cluster-backed checks:
  - `make verify`
