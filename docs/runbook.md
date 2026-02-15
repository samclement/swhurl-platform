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
- `scripts/20_namespaces.sh` (Kustomize: `infra/manifests/platform`)
- `scripts/25_helm_repos.sh`
- `scripts/26_cilium.sh` (feature-gated by `FEAT_CILIUM`)

6) Platform services & verification
- `scripts/30_cert_manager.sh`
- `scripts/35_issuer.sh` (Kustomize: `infra/manifests/issuers/*`, default `LETSENCRYPT_ENV=staging`)
- `scripts/40_ingress_nginx.sh`
- `scripts/45_oauth2_proxy.sh` (feature-gated by `FEAT_OAUTH2_PROXY`)
- `scripts/50_clickstack.sh` (feature-gated by `FEAT_CLICKSTACK`)
- `scripts/51_otel_k8s.sh` (feature-gated by `FEAT_OTEL_K8S`)
- `scripts/70_minio.sh` (feature-gated by `FEAT_MINIO`)

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

