# Findings (ordered by severity)

1. **High: Makefile mode switches are GitOps-no-op until commit/push, but the workflow implies immediate apply.**  
`make platform-certs-*` / `make app-test-*` copy local files, then call reconcile. Reconcile pulls from Git remote, not your local working tree, so changes won’t reach cluster unless pushed.  
Refs: [Makefile](/home/sam/ghq/github.com/samclement/swhurl-platform/Makefile#L58), [run.sh](/home/sam/ghq/github.com/samclement/swhurl-platform/run.sh#L42), [gitrepositories.yaml](/home/sam/ghq/github.com/samclement/swhurl-platform/clusters/home/flux-system/sources/gitrepositories.yaml#L11), [README.md](/home/sam/ghq/github.com/samclement/swhurl-platform/README.md#L94)

2. **High: Infrastructure layer is coupled to platform runtime secrets, violating layer boundaries.**  
`homelab-infrastructure` requires `platform-runtime-inputs` and includes runtime-input secret targets used by oauth2/clickstack/otel. Missing app secrets can block infra reconciliation.  
Refs: [infrastructure.yaml](/home/sam/ghq/github.com/samclement/swhurl-platform/clusters/home/infrastructure.yaml#L14), [infrastructure overlay](/home/sam/ghq/github.com/samclement/swhurl-platform/infrastructure/overlays/home/kustomization.yaml#L5), [runtime-inputs README](/home/sam/ghq/github.com/samclement/swhurl-platform/infrastructure/runtime-inputs/README.md#L5)

3. **High: Live drift exists outside Flux ownership.**  
Cluster has deployed Helm releases not represented by Flux HelmReleases (`platform-namespaces`, `traefik`, `traefik-crd` from live `helm list -A`). Also defaults are contradictory: host defaults Traefik while cluster layer deploys ingress-nginx.  
Refs: [homelab.env](/home/sam/ghq/github.com/samclement/swhurl-platform/host/config/homelab.env#L2), [config.env](/home/sam/ghq/github.com/samclement/swhurl-platform/config.env#L35), [infra composition](/home/sam/ghq/github.com/samclement/swhurl-platform/infrastructure/overlays/home/kustomization.yaml#L10)

4. **Medium: Provider migration contract is advertised but not implemented declaratively.**  
Traefik/Ceph overlays are empty, while runbooks instruct switching to them.  
Refs: [ingress-traefik kustomization](/home/sam/ghq/github.com/samclement/swhurl-platform/infrastructure/ingress-traefik/base/kustomization.yaml#L3), [ceph kustomization](/home/sam/ghq/github.com/samclement/swhurl-platform/infrastructure/storage/ceph/base/kustomization.yaml#L3), [traefik runbook](/home/sam/ghq/github.com/samclement/swhurl-platform/docs/runbooks/migrate-ingress-nginx-to-traefik.md#L18), [ceph runbook](/home/sam/ghq/github.com/samclement/swhurl-platform/docs/runbooks/migrate-minio-to-ceph.md#L28)

5. **Medium: AGENTS/docs drift is significant and includes retired Helmfile guidance.**  
AGENTS still contains many Helmfile-era instructions; architecture doc still models old issuer/app namespace semantics.  
Refs: [AGENTS.md](/home/sam/ghq/github.com/samclement/swhurl-platform/AGENTS.md#L79), [AGENTS.md](/home/sam/ghq/github.com/samclement/swhurl-platform/AGENTS.md#L111), [architecture.d2](/home/sam/ghq/github.com/samclement/swhurl-platform/docs/architecture.d2#L40), [architecture.d2](/home/sam/ghq/github.com/samclement/swhurl-platform/docs/architecture.d2#L63)

6. **Medium: Config surface exposes vars that do not drive manifests (config illusion).**  
Examples: `RUN_HOST_LAYER`, `MINIO_ROOT_USER`, `CLICKSTACK_OTEL_ENDPOINT` are effectively dead/verify-only; host/domain values are mostly hardcoded in manifests.  
Refs: [config.env](/home/sam/ghq/github.com/samclement/swhurl-platform/config.env#L24), [config.env](/home/sam/ghq/github.com/samclement/swhurl-platform/config.env#L29), [config.env](/home/sam/ghq/github.com/samclement/swhurl-platform/config.env#L58), [oauth2-proxy values](/home/sam/ghq/github.com/samclement/swhurl-platform/platform-services/oauth2-proxy/base/helmrelease-oauth2-proxy.yaml#L30)

7. **Low: CI does not render all active mode overlays.**  
Current workflow renders default paths but not all mode/prod overlay combinations.  
Ref: [validate.yml](/home/sam/ghq/github.com/samclement/swhurl-platform/.github/workflows/validate.yml#L62)

## Open Questions / Assumptions

1. Should Flux remote Git remain the only source of truth for mode changes (strict GitOps), or do you want live cluster patching workflows?  
2. Do you want Traefik fully removed from host defaults, or kept intentionally as unmanaged system ingress?

## Suggested simplification path

1. **Fix operator truthfulness first:** make mode targets “edit-only” (or fail unless branch is pushed), and document push as required before reconcile.  
2. **Restore layer boundaries:** move runtime-input substitution/resources out of `homelab-infrastructure` into a dedicated platform/runtime Kustomization.  
3. **Resolve ownership drift:** remove legacy `platform-namespaces` release and choose one ingress owner (Flux nginx or Flux traefik, not mixed unmanaged).  
4. **Reduce contract surface:** delete dead env vars and stale docs/AGENTS guidance; keep env bridge only for secrets.  
5. **Tighten CI:** render all overlay/mode paths used by Make targets.
