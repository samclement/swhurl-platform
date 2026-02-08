# Platform Plan (k3s-only, declarative)

This plan defines a simplified, k3s-only workflow. The repository focuses on declarative resources and values, with scripts acting as thin orchestrators.

## Goals

- k3s-only (remove kind/Podman support)
- Declarative manifests and values as the source of truth
- Clear, explicit execution order
- DNS is independent of Kubernetes but required before cert-manager

## Repository Shape

```
infra/
  manifests/
    namespaces.yaml
    apps/
    issuers/
    templates/
  values/
    ingress-nginx-logging.yaml
    fluent-bit-loki.yaml
    loki-single.yaml
    kube-prometheus-stack.yaml.gotmpl
    oauth2-proxy.yaml.gotmpl
    minio.yaml.gotmpl
profiles/
  secrets.env
  minimal.env
scripts/
  01_check_prereqs.sh
  11_cluster_k3s.sh
  12_dns_register.sh
  15_kube_context.sh
  20_namespaces.sh
  25_helm_repos.sh
  30_cert_manager.sh
  35_issuer.sh
  40_ingress_nginx.sh
  45_oauth2_proxy.sh
  50_logging_fluentbit.sh
  55_loki.sh
  60_prom_grafana.sh
  70_minio.sh
  75_sample_app.sh
  90_smoke_tests.sh
  95_dump_context.sh
  99_teardown.sh
run.sh
config.env
```

## Configuration

- `config.env` is the base config for non-secret values.
- `profiles/*.env` layer on top of `config.env`.
- Secrets belong in `profiles/secrets.env`.

## Execution Order (explicit)

1. `01_check_prereqs.sh`
2. `11_cluster_k3s.sh` (validates kube access only; does not install k3s)
3. `12_dns_register.sh` (DNS is outside Kubernetes and must be ready before cert-manager)
4. `15_kube_context.sh`
5. `20_namespaces.sh` (applies `infra/manifests/namespaces.yaml`)
6. `25_helm_repos.sh`
7. `30_cert_manager.sh`
8. `35_issuer.sh` (uses `infra/manifests/issuers/*`)
9. `40_ingress_nginx.sh`
10. `45_oauth2_proxy.sh` (if enabled)
11. `50_logging_fluentbit.sh` (if enabled)
12. `55_loki.sh` (if enabled)
13. `60_prom_grafana.sh` (if enabled)
14. `70_minio.sh` (if enabled)
15. `75_sample_app.sh`
16. `90_smoke_tests.sh`
17. `91_validate_cluster.sh`

Run with:

```
./run.sh
```

Use profiles:

```
./run.sh --profile profiles/minimal.env
```

## Declarative Principles

- Kubernetes objects live in `infra/manifests/` and are applied with `kubectl apply`.
- Helm values live in `infra/values/` and are referenced by scripts.
- Scripts do not embed long YAML; they render or apply files.

## DNS Separation

- `scripts/12_dns_register.sh` does not use `kubectl` or kubeconfig.
- It can be run independently and must complete before `35_issuer.sh` when using Let’s Encrypt.

## Teardown

- `./run.sh --delete` uninstalls components in reverse order.
- k3s uninstall is manual: `sudo /usr/local/bin/k3s-uninstall.sh` (or set `K3S_UNINSTALL=true`).
