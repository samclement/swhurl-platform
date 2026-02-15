# Platform Runbook (Phases)

This repo is organized into explicit phases so you can run, verify, and debug the platform in stages. Scripts should be thin wrappers; declarative state lives in Helmfile and Kustomize.

Print the current plan:

```bash
./scripts/02_print_plan.sh
./scripts/02_print_plan.sh --delete
```

## Phases (Install)

1) Prerequisites & verify
- `scripts/01_check_prereqs.sh`

2) DNS & Network Reachability
- `scripts/12_dns_register.sh` (feature-gated by `FEAT_DNS_REGISTER`)

3) Basic Kubernetes Cluster (kubeconfig)
- `scripts/15_kube_context.sh`
- Optional host bootstrap (not part of the default platform pipeline):
  - `scripts/10_install_k3s_cilium_minimal.sh`
  - `scripts/11_cluster_k3s.sh`
  - Enable with `FEAT_BOOTSTRAP_K3S=true`

4) Environment (profiles/secrets) & verification
- `scripts/94_verify_config_contract.sh` (feature-gated by `FEAT_VERIFY`)

5) Cluster deps (helm/cilium) & verification
- `scripts/25_helm_repos.sh`
- `scripts/20_namespaces.sh` (Helmfile local chart: `component=platform-namespaces`)
- `scripts/26_cilium.sh` (feature-gated by `FEAT_CILIUM`)

6) Platform services & verification
- `scripts/31_helmfile_core.sh` (Helmfile: `phase=core`, installs cert-manager + ingress-nginx)
- `scripts/35_issuer.sh` (Kustomize: `infra/manifests/issuers/*`, default `LETSENCRYPT_ENV=staging`)
- `scripts/29_platform_config.sh` (kubectl: secrets/configmaps required by Helm releases)
- `scripts/36_helmfile_platform.sh` (Helmfile: `phase=platform`, installs oauth2-proxy/clickstack/otel/minio based on feature flags)

Notes:
- `scripts/30_cert_manager.sh --delete` still exists as a delete-helper for cert-manager finalizers/CRDs; the apply path is driven by `scripts/31_helmfile_core.sh`.

7) Test application & verification
- `scripts/75_sample_app.sh`

8) Cluster verification suite
- `scripts/90_smoke_tests.sh`
- `scripts/91_validate_cluster.sh`
- `scripts/92_verify_helmfile_diff.sh`
- `scripts/93_verify_release_inventory.sh`
- `scripts/95_dump_context.sh`
- `scripts/95_verify_kustomize_builds.sh`
- `scripts/96_verify_script_surface.sh`

## Phases (Delete)

Delete runs in reverse order with deterministic finalizers:

1) Remove apps and platform services (reverse order)
2) Perform teardown sweep (namespaces, non-k3s-native secrets, platform CRDs)
3) Remove Cilium last
4) Verify cluster is clean (`scripts/98_verify_delete_clean.sh`)

Important: Cilium is deleted only after platform namespaces and PVCs are removed, so k3s/local-path helper pods can still run during namespace cleanup.
