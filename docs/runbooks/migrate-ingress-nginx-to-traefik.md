# Runbook: Migrate Ingress NGINX to Traefik

This runbook moves cluster ingress ownership from `ingress-nginx` to Traefik with the
current repo controls.

## Scope

- Keep cert-manager, oauth2-proxy, clickstack, cilium, and sample app managed by the
  existing pipeline.
- Switch provider intent from `INGRESS_PROVIDER=nginx` to `INGRESS_PROVIDER=traefik`.
- Keep rollback simple by reverting the provider flag.

## Preconditions

1. Cluster is healthy and current pipeline converges:
   - `./run.sh`
   - `./scripts/91_verify_platform_state.sh`
2. You have a backup/snapshot and a maintenance window.
3. Traefik is available in k3s (default k3s ingress) or otherwise pre-installed.

## Migration Steps

1. Record current state.

```bash
helm list -A
kubectl get ingress -A
```

2. Set provider intent to Traefik by using the committed profile file
   `profiles/provider-traefik.env` (no ad-hoc profile creation required).

3. Reconcile core and platform with the new provider.

```bash
./run.sh --profile profiles/provider-traefik.env
```

4. Verify `ingress-nginx` is no longer managed/installed and ingress resources remain
   present with the expected hosts/TLS.

```bash
helm list -A | rg ingress-nginx || true
kubectl get ingress -A
./scripts/91_verify_platform_state.sh
```

5. Run drift and smoke checks.

```bash
./scripts/92_verify_helmfile_drift.sh
FEAT_VERIFY_DEEP=true ./run.sh --profile profiles/provider-traefik.env
```

## Expected Behavior in This Repo

- `scripts/31_sync_helmfile_phase_core.sh` installs ingress-nginx only when
  `INGRESS_PROVIDER=nginx`.
- Helmfile values switch ingress class via `computed.ingressClass`.
- NGINX-specific ingress annotations are gated off when provider is Traefik.
- `scripts/90_verify_runtime_smoke.sh` and `scripts/91_verify_platform_state.sh` skip
  NGINX-specific checks when provider is Traefik.

## Rollback

1. Restore provider intent to nginx:

```bash
INGRESS_PROVIDER=nginx ./run.sh
```

2. Re-run verification:

```bash
./scripts/91_verify_platform_state.sh
./scripts/92_verify_helmfile_drift.sh
```
