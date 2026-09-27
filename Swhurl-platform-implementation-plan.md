# Swhurl Platform — implementation plan

27 September 2026 · Proposed implementation; current platform already deployed

Based on repository commit `91e889353f656577b48efc6f85d9f1b4427852f7` and read-only checks of the current host and Kubernetes cluster on 27 September 2026\. The router, external backup destinations, Mac model server and application repositories remain uninspected\. Recheck the baseline after later commits\. This review changed only this plan; it did not change cluster resources\.

## 1\. Outcome and scope

Swhurl Platform supplies the shared infrastructure and deployment mechanisms that turn an application definition into a running, reachable and operable homelab service\. Application authors specify requirements; the platform supplies consistent deployment, ingress, identity integration, secret delivery, storage integration and operational visibility\.

The primary outcome is a short application workflow: create an instance definition, provide secrets, open a pull request, merge, and observe deployment\. A routine app should not require hand\-authoring Deployment, Service, Ingress, Certificate and environment patch files\.

Keep K3s, Flux, Traefik, cert\-manager, the existing secret\-encryption approach and shared sign\-in\. Introduce a small application chart, a scaffolding command and a clear image\-publication workflow\. Decouple unrelated capabilities and establish recovery before migrating stateful applications\.

Non\-goals for this iteration: replacing Kubernetes, building a developer portal, deploying a self\-hosted container registry, adopting a service mesh, replacing storage with a distributed storage system, or automatically provisioning the router\. These would need separate requirements\.

### Completion criteria

- A simple stateless app requires one HelmRelease instance definition, an optional encrypted Secret, and generated reconciliation wiring\. No hand\-written ingress or certificate patches\.
- A second app is onboarded in under 15 minutes after its image exists, excluding domain propagation and certificate issuance\. This is an acceptance target, not a measured current figure\.
- Publishing an app image creates a reviewable deployment update identifying its immutable digest\.
- An observability or optional object\-storage failure does not stop unrelated app reconciliation\.
- Changing a referenced runtime Secret triggers the required workload restart without a service\-specific operator command\.
- Operators can identify an app’s desired revision, running image, readiness, address and failure reason with one command\.
- Suspend, uninstall and data destruction have distinct documented semantics\.
- At least one stateful workload and the platform decryption material have been restored from an independent backup\.
- Hermes can run with persistent state and constrained access to a model server on the Mac without cluster\-administration credentials\.

## 2\. Decisions and live facts to establish

The existing platform is live on this host\. Use `KUBECONFIG=$HOME/.kube/config` for scripted `kubectl` and Flux checks here; the `/usr/local/bin/kubectl` k3s wrapper may otherwise select the root-owned k3s kubeconfig\. Proceed with the defaults below for design work\. Resolve the gated questions before the relevant live change; they do not block documentation, validation fixes or local chart development\.

### Observed baseline — 27 September 2026

|Area|Observed state|Execution consequence|
|---|---|---|
|Host and k3s|Arch Linux x86-64; `k3s` service active and enabled; one Ready control-plane node (`arch`, `192.168.1.200`) running `v1.34.4+k3s1` with containerd; Flux CLI/controllers `v2.8.1`|This is an in-place change to an existing single-node installation, not a new k3s bootstrap\. Record the installed server configuration and datastore type before recovery work\.|
|Capacity|31 GiB RAM, about 23 GiB available at inspection; root filesystem 239 GiB with about 169 GiB free|Measure workload, disk and backup growth before adding stateful services\.|
|Flux and releases|All six active Flux Kustomizations Ready at `main@sha1:91e88935`; cert-manager, oauth2-proxy, ClickStack, both OTel releases and MinIO Ready|Capture Flux inventories and Helm ownership before changing paths, pruning or release names\.|
|Ingress and TLS|Packaged Traefik and metrics-server Ready; Traefik Service uses HTTP NodePort `31514` and HTTPS `30313`; all three ClusterIssuers and six current Certificates Ready|Preserve these ports and routes during any handover; external router and public DNS still need separate verification\.|
|Persistent data|Four Bound `local-path` claims: ClickHouse data/logs, MongoDB and MinIO; default StorageClass and all four PVs use reclaim policy `Delete`|Treat namespace deletion, claim deletion, Helm uninstall and Flux pruning as data-loss risks until retention and independent restore are proven\.|
|Secrets and policy|`flux-system/sops-age` exists; local `age.agekey` exists; no Kubernetes backup CronJobs were found; NetworkPolicies are present only in `flux-system`|Key presence is not proof of an off-host backup; planned private workloads need a tested network-policy enforcement mechanism\.|
|Local checks|Active Kustomize entrypoints, `make verify-config`, tracked shell syntax, and install/teardown dry-runs pass; CI's `platform-services/runtime-inputs` render path fails because it does not exist|PR 01 can start locally\. CI must be repaired before its checks can be trusted\.|

These checks establish availability and configuration shape, not data recoverability, external reachability, authentication policy or telemetry correctness\. Do not run `make verify-platform` or a normal `make install` as a baseline check until PR 02 removes the verifier's plaintext credential output: `make install` invokes that verifier when `FEAT_VERIFY=true`\. Do not run `make teardown` or `make reinstall` against this live stack while its deletion effects and backups are unproven\.

### Execution readiness

PR 01 is ready to execute as a repository-only change\. The current installation is healthy enough to inventory without reinstalling k3s or Flux\. The whole plan is not ready for an uninterrupted live rollout: an independent restore has not been demonstrated, the deletion/ownership graph has not been recorded, the router and external dependencies have not been checked, and the verifier can print credentials on failure\. Complete the backup/restore gate and the specific live checks above before the dependent changes\. Do not treat a successful dry-run as evidence that deletion preserves data\.

|Decision           |Proposed default                                                             |Confirm before               |
|-------------------|-----------------------------------------------------------------------------|-----------------------------|
|Cluster topology   |Confirmed one Ready node today; plan for single-host failure                 |Storage and isolation changes|
|Application mix    |Support ordinary single-container apps and upstream Helm charts              |Finalising chart scope       |
|Environments       |Preserve existing staging and production; new utilities may have one instance|Migrating app reconciliation |
|Registry visibility|Private by default; public only by explicit choice                           |First image publication      |
|Exposure           |Private by default; authenticated browser access opt-in; public explicit     |First new ingress            |
|Permitted users    |Explicit identities rather than every Google account                         |Authentication change        |
|Persistent data    |Preserve during app uninstall; erase separately; current PVs use `Delete`    |Any deletion/migration       |
|Backup destination |Outside the homelab host and its disks                                       |Stateful migration           |
|Recovery objectives|Record acceptable data loss and restore time per app                         |Backup scheduling            |
|Model server       |Native service on Mac, private endpoint and token                            |Hermes deployment            |
|Agent execution    |Treat generated commands and installed tools as untrusted                    |Selecting Hermes isolation   |

Remaining inventory: k3s server configuration/datastore and host backups; Flux inventories; router mappings and public DNS; live authentication restrictions; workloads not in Git; image publication workflows; external backup destination and restore evidence; Mac model endpoint and network path\.

Keep the sanitised inventory in this plan for the review; move it to `docs/operations/current-state.md` in PR 01 and update it as unknowns are verified\. Do not commit exported Secrets, kubeconfig credentials or private keys\. Record sensitive backup material only in the chosen secure recovery location\.

## 3\. Ownership and configuration contracts

Avoid a directory\-wide rename during functional migration\. Introduce interfaces in the existing structure, then rename only if it improves navigation\.

|Concern              |Authoritative configuration                                                         |Owner and interface                                                                      |
|---------------------|------------------------------------------------------------------------------------|-----------------------------------------------------------------------------------------|
|Host/network         |Existing `host/`, dedicated non-secret host configuration and network inventory     |Host operator supplies addresses, disks, remote access and ingress port mappings         |
|Cluster foundation   |Versioned K3s bootstrap/configuration documentation, later host automation if needed|Supplies networking, scheduling and named storage classes                                |
|Deployment control   |`clusters/home/flux-system` and child Flux definitions                              |Reads Git, decrypts authorised manifests and reconciles independently                    |
|Shared capabilities  |Existing `infrastructure/` and `platform-services/`                                 |Publishes named ingress class, issuer, authentication middleware and telemetry interfaces|
|Shared non-secrets   |`clusters/home/flux-system/sources/configmap-platform-settings.yaml`                |Shared domain and capability defaults; no credentials                                    |
|Application instances|`tenants/apps/<app>/<instance>/`                                                    |Image, resources, ports, health, exposure, configuration, storage and Secret references  |
|Application templates|New `charts/swhurl-app/`                                                            |Converts supported app requirements into standard Kubernetes resources                   |
|Secrets              |Encrypted manifests beside consuming service/instance                               |SOPS (Secrets OPerationS) encryption; recovery key held separately                       |
|Image builds         |Each application repository                                                         |Tested image, supported architectures, source revision and digest                        |
|External services    |Non-secret endpoint catalogue plus application-owned credentials                    |Address, protocol, authentication, availability and network policy                       |
|Recovery             |New `docs/operations/recovery.md` and scheduled backup definitions                  |Retention, restore sequence and evidence                                                 |

Separate host settings from Kubernetes settings\. Restrict `config.env` to local/host operation inputs or rename it accordingly\. Make `BASE_DOMAIN` genuinely authoritative for shared hosts, or remove the misleading setting\. Do not retain two settings with implied ownership of the same value\.

Application contracts must distinguish browser authentication from machine authentication\. Shared sign\-in establishes user identity; the application owns its internal permissions\. An inference application programming interface &#40;API&#41; uses token authentication, not browser redirects\.

## 4\. Delivery sequence

Implement as small pull requests &#40;PRs&#41;, with documentation updated in each behaviour\-changing PR as required by `AGENTS.md`\. The numbered table groups work; the safe live rollout order is PR 01, the independent backup and restore gate in PR 08, then live deletion/ownership changes in PR 02 and PR 03\. PR 02's verifier and documentation fixes can be prepared earlier\. Chart and generator work in PR 04 can proceed locally in parallel with the recovery gate\.

|PR|Deliverable                                                   |Depends on                   |Indicative hands-on effort  |
|--|--------------------------------------------------------------|-----------------------------|----------------------------|
|01|Inventory, corrected validation and reliable documentation    |None                         |0.5–1 day                   |
|02|Safe lifecycle, credential handling and explicit access policy|01; live deletion after 08    |1–2 days                    |
|03|Independent reconciliation and controller/issuer ordering     |02, 08 for live handover      |1–2 days                    |
|04|Application contract, shared chart and scaffolding            |01; rollout after 03         |2–3 days                    |
|05|Example migration and app operator commands                   |03, 04                       |1–2 days                    |
|06|Registry publishing and deployment-update workflow            |04, app repository access    |1–2 days                    |
|07|Secret rollout automation and configuration consolidation     |03–05                        |0.5–1.5 days                |
|08|Backup and restore implementation                             |01; before live deletion/ownership changes or stateful migration|1–2 days plus restore window|
|09|Hermes deployment and Mac inference integration               |03–08 as applicable          |1–2 days                    |
|10|Acceptance exercise and final documentation cleanup           |Previous deliverables        |0.5–1 day                   |

Estimate: roughly 10–18 hands\-on engineering days, with uncertainty around existing storage, provider authentication and undocumented live configuration\. It is not a delivery commitment\. For evening work, schedule by completed PR rather than calendar deadline\.

## 5\. PR 01 — establish a trustworthy baseline

### Changes

1. Record live inventory and classify findings as confirmed repository defects, runtime questions or desired improvements\.
2. Fix `.github/workflows/validate.yml`: remove the missing `platform-services/runtime-inputs` render path; invoke `bash -n` separately for every tracked shell script\.
3. Introduce one maintained list or discovery mechanism for active render entrypoints, reused by local validation and continuous integration &#40;CI&#41;\. Never silently skip a missing configured path\.
4. Add schema validation for rendered Kubernetes resources and known Flux resource types\. Account for Flux substitution and encrypted Secrets rather than applying ciphertext to a disposable cluster\.
5. Add a check for unresolved shared substitutions and a check that every Git\-managed Secret uses the intended encryption format\. Exempt documented bootstrap/recovery procedures, not arbitrary Secret files\.
6. Correct `docs/INFRASTRUCTURE.md`, `docs/PLATFORM-SERVICES.md`, `docs/runbook.md`, the contradictory ClickStack/OTel key comment in `Makefile`, and relevant agent guidance: remove deleted central\-secret paths and inconsistent ingestion\-key instructions\.
7. Keep the README short: setup prerequisites, routine application workflow, status and recovery links\.

### Acceptance

- Local validation and CI use the same entrypoints and succeed on the active configuration\.
- A deliberately invalid second shell script is rejected, proving the workflow checks more than one file\.
- A broken manifest path or unresolved required substitution is rejected\.
- All current service secret\-edit paths exist; PR 01 validation does not decrypt or print credentials\. The existing live verifier's plaintext output remains a PR 02 fix\.

### Rollback

Revert validation/documentation changes if necessary\. No live resource changes in this PR\.

## 6\. PR 02 — lifecycle and access correctness

### Changes

1. Replace the ambiguous operational meaning of `teardown` with explicit operations: suspend reconciliation; resume; uninstall a named application; intentionally destroy a named scope\. Do not make destructive commands part of normal deployment\.
2. Trace actual Flux inventories and Helm ownership before changing deletion policies\. Parent and child Kustomizations currently use `prune: true`; deleting a parent can cascade\.
3. Define retention for namespaces, persistent volume claims, volumes and Helm\-managed data\. `deletionPolicy: Orphan` alone does not protect against deleting resources from Git or deleting their namespace\. Combine documented ownership, appropriate prune exclusions/retention and storage reclaim policy where required\.
4. Avoid implicit lifecycle cycles: after deliberately removing reconciliation parents, recovery must explicitly bootstrap them; reconciling a deleted object is not a reinstall strategy\.
5. Remove plaintext credential output from `scripts/verify-platform.sh`\. Determine the true ingestion\-key encoding and compare exactly the bytes received by collectors\. A Kubernetes `.data` read normally requires one base64 decode; do not assume the encrypted contents are correct without authorised inspection\.
6. Replace the broad Google email\-domain allowance with explicit approved identities, using the provider\-supported configuration\. Confirm external provider restrictions rather than relying on them implicitly\.
7. Document which existing ingress endpoints rely on shared sign\-in and which rely on application authentication\. Resolve intended visibility for MinIO, its console and ClickStack without assuming they are currently unauthenticated\.

### Acceptance

- In a disposable test scope, suspend changes no workload/data; uninstall removes only the selected application resources and preserves retained data\.
- A named data\-destruction procedure is separate and explains the affected storage\.
- Verification failures display resource names and safe diagnostics, never secret values\.
- An approved account succeeds and an unapproved account is rejected\. Existing valid sessions are considered during the change\.
- Bootstrap/recovery instructions actually recreate missing reconciliation objects\.

### Rollback

Use a pre\-change backup and retained workload manifests\. Revert policy changes before unfreezing reconciliation if the ownership graph differs from expectations\. A Git revert cannot recover deleted volume data\.

## 7\. PR 03 — independent reconciliation

### Target reconciliation units

|Unit                   |Installation dependency                            |Notes                                                                        |
|-----------------------|---------------------------------------------------|-----------------------------------------------------------------------------|
|Sources                |Flux controllers and repository access             |Sources/shared non-secret settings                                           |
|Namespaces             |Sources                                            |Baseline policies; no observability dependency                               |
|cert-manager controller|Namespaces                                         |Wait for Helm release/controller readiness                                   |
|Certificate issuers    |cert-manager controller                            |Apply only after resource definitions exist                                  |
|Traefik configuration  |Packaged K3s Traefik                               |Preserve current NodePorts/router mapping                                    |
|Shared sign-in         |Namespace, ingress and issuer readiness as required|Independent of telemetry/object storage                                      |
|ClickStack             |Namespace and its storage requirements             |Optional capability; independent app lifecycle                               |
|Telemetry collectors   |Namespaces; collector destination configuration    |May depend on ClickStack for initial install; apps never depend on collectors|
|MinIO                  |Namespace and storage                              |Only explicit consumers depend on it                                         |
|App instance           |Namespace plus interfaces it actually requires     |Workers do not depend on web ingress/sign-in                                 |

### Changes

- Split `clusters/home/infrastructure.yaml`, `platform.yaml` and `tenants.yaml` into capability\-specific reconciliation definitions\.
- Give each unit the required substitutions/decryption settings explicitly\. Do not assume parent settings propagate to child Flux Kustomizations\.
- Use targeted readiness and realistic timeouts\. A parent registry of child Kustomizations should not aggregate every optional capability into an application prerequisite\.
- Preserve existing resource names, namespaces, Helm release names and storage claims during the split\.
- Move a resource between Flux owners using a staged, documented handover: suspend affected owners, prevent old\-owner pruning, change inventories/paths, reconcile and verify the new owner, then retire obsolete ownership and restore normal pruning\. Validate the procedure on a disposable resource first; do not allow concurrent conflicting ownership\.

### Acceptance

- Fresh bootstrap creates cert\-manager then issuers without a manual resource\-definition installation step\.
- A controlled ClickStack failure does not prevent an unrelated app update\.
- MinIO can be unavailable without blocking namespace creation or unrelated releases\.
- No duplicate ownership, unexpected Helm uninstall, ingress interruption or persistent\-volume recreation occurs\.

### Rollback

Suspend affected reconciliation first; use recorded inventories to restore ownership and previous definitions\. Avoid a blind revert while pruning is active\.

## 8\. PR 04 — application contract and shared chart

### New files

- `charts/swhurl-app/Chart.yaml`, `values.yaml`, `values.schema.json` and templates\.
- `docs/application-contract.md` describing supported inputs and defaults\.
- `scripts/app-new.py` or equivalent small generator; choose one implementation language already available in CI\.
- Representative rendering fixtures for a worker, authenticated web app and persistent app\.

### Contract

|Input                       |Behaviour                                                                               |
|----------------------------|----------------------------------------------------------------------------------------|
|Image repository and digest |Required immutable identity for promoted releases                                       |
|Command/arguments           |Optional override for upstream images                                                   |
|Workload mode               |Ordinary Deployment initially; specialised apps may use upstream charts/manifests       |
|Port                        |Optional; no Service created for a worker without a port                                |
|Health checks               |Explicit application paths/commands and startup allowance; do not invent `/health`      |
|Resources                   |Requests/limits supplied or inherited from a documented small-app profile               |
|Exposure                    |`private`, `authenticated-web`, or `public`; precise network reachability documented    |
|Host                        |Required for web exposure; supplied once and reused in ingress/certificate configuration|
|Config and Secret references|Non-secret values separate from credentials                                             |
|Persistence                 |Existing claim or explicitly provisioned claim; mount, size, storage class and retention|
|Service account             |No mounted cluster token by default                                                     |
|Security                    |Non-privileged defaults; writable locations and exceptions explicit                     |
|External dependencies       |References to endpoints and associated network/credential requirements                  |

Use one HelmRelease as the primary app\-instance configuration, avoiding an additional custom application language and compiler\. The generator creates this, Kustomize composition, Flux registration and optional encrypted\-secret stub\. Generated wiring remains committed and reviewable\. It must not commit plaintext credentials or overwrite an existing app\.

Use ingress annotations for certificate creation where appropriate\. Make browser authentication conditional; public endpoints and machine APIs must not inherit browser redirects accidentally\.

Initially source the chart from this Git repository through Flux\. Do not add a separate chart registry solely for the first release\. Note that an in\-repository shared chart change can affect multiple apps: validate all consumers and use a staged rollout\. Introduce immutable chart distribution later if independent chart\-version promotion becomes necessary\.

### Acceptance

- A new app needs no edits inside chart templates\.
- Schema validation rejects missing image/host requirements and incompatible values\.
- Render fixtures confirm selectors, names, auth annotations, Secrets, persistence and token\-mount defaults\.
- Requests to an authenticated app redirect unauthenticated browsers; machine endpoints retain the intended authentication contract\.
- A private app has no public route; network policy controls pod access separately\.
- The chart remains small; special workloads have a documented upstream\-chart/raw\-manifest route\.

## 9\. PR 05 — migrate the example and improve operation

1. Generate an example instance in a temporary namespace and compare its rendered resources with the current example\.
2. Validate routing, certificate issuance, sign\-in and readiness before migration\.
3. Migrate staging first\. If preserving names while transferring from raw manifests to Helm, validate explicit Helm adoption and old Flux ownership removal\. Otherwise use new resource names and a controlled route cutover\. Do not assume Helm will adopt existing resources automatically\.
4. Migrate production separately; keep its Flux unit independent from staging\.
5. Delete old base/overlay resources only after confirming the new owner and pruning behaviour\.
6. Add `make app-check NAME=... INSTANCE=...`, `app-status`, `app-reconcile` and `app-logs`\. Status should show desired Git revision, applied revision, desired image digest, running image identity, ready replicas, address and actionable failure messages\.
7. Keep `make install` for bootstrap/full\-platform operations\. Explain normal Git\-driven deployment and optional targeted reconciliation\.

Acceptance: onboard a second disposable app from an existing image within the 15\-minute target; change only one instance’s digest; identify and recover from an invalid image and a failing health check\. The other instance remains healthy and independently reconciled\.

Rollback: preserve the previous route/workload until cutover is validated\. Revert the app digest for normal release rollback\. Handle ownership rollback explicitly for this one\-time migration\.

## 10\. PR 06 — registry and release delivery

### Registry choice

Use GitHub Container Registry &#40;GHCR&#41; for first\-party images\. Keep third\-party upstream images unless a custom build or availability requirement justifies mirroring\. An in\-cluster registry adds storage, authentication, backup and bootstrap dependencies without solving the immediate usability problem\.

### Workflow

1. App repository workflow tests and builds an image\.
2. Publish `ghcr.io/samclement/<app>` with a source\-revision tag and optional release tag; capture its immutable digest\.
3. Use the workflow’s built\-in `GITHUB_TOKEN` with package\-write permissions for publication\.
4. Open a deployment PR against this platform repository updating only the intended instance’s digest\. Cross\-repository writes require an explicitly scoped credential, preferably a GitHub App; an app repository’s default token is not automatically authorised here\.
5. Platform validation renders the affected app and checks contracts\. Merge triggers Flux deployment\.
6. Promote the same digest from staging to production\. Never rebuild an image merely to promote it\.
7. Keep deployed digests and a documented rollback window during registry cleanup\.

For private packages, provision read\-only registry credentials as encrypted Kubernetes image\-pull Secrets in each consuming namespace\. Do not place a package\-write credential in application pods\. Document rotation and package\-access permissions\.

Build the architecture used by the homelab; publish both x86\-64 and ARM64 only if actual consumers require them\. Hosting inference natively on the Mac does not itself require ARM64 versions of every application image\.

Acceptance: build, publish, open PR, deploy, verify actual image identity, promote and roll back one real app\. Test pulling private images on a node without a cached copy\. Confirm CI has no direct cluster\-admin credentials\.

## 11\. PR 07 — secrets and shared settings

- Extend `.sops.yaml` rules to application instance paths before generating app Secrets\.
- Use `stringData` for human\-authored encrypted runtime Secrets where compatible with the chosen apply workflow; otherwise document exactly one required base64 layer for `data`\. Validate decrypted structure without logging values\.
- Select one restart mechanism for externally managed Secrets: a narrowly configured Secret\-watching rollout controller is a practical option\. A Helm template checksum alone does not detect arbitrary changes to an external Secret\.
- If using a rollout controller, define its namespace permissions, annotate only intended workloads and test rotation without broad cluster access\.
- Consolidate shared domains/hosts and remove stale local settings\. Keep application hosts overridable and environment\-specific\.
- Keep ClickStack bootstrap keys and ingestion keys as separate concepts unless verified implementation behaviour requires otherwise\.

Acceptance: rotate a disposable Secret and observe the intended rollout; unrelated apps do not restart; collectors authenticate after rotation; configuration edits have one documented authoritative path\.

## 12\. PR 08 — persistence and recovery

1. Classify data: reconstructible from Git, reconstructible from registry, expendable telemetry, and irreplaceable application state\.
2. Verify actual `local-path` placement and reclaim behaviour\. Local persistent storage is not replication or backup\.
3. Choose a backup destination outside the host failure domain and protect its credentials\. In\-cluster MinIO on the same disk is not sufficient as the only backup\.
4. Choose application\-consistent backup methods: database\-native backups for databases; stop/quiesce or snapshot\-aware procedures for filesystem state\. Avoid assuming a copy of a live database file is recoverable\.
5. Record per\-workload recovery point objective &#40;acceptable lost data&#41; and recovery time objective &#40;acceptable downtime&#41;, retention and capacity\.
6. Back up decryption keys and required host/bootstrap credentials separately\. Decide whether cluster\-database backup is required alongside declarative rebuild; use the mechanism matching the actual K3s datastore\.
7. Rebuild a clean test scope, restore an encrypted Secret and one persistent workload, and verify application behaviour—not merely that files exist\.

Acceptance: dated restore evidence, documented dependencies and measured restore time\. Stateful application migration waits for this gate; stateless chart work need not\.

## 13\. PR 09 — Hermes and Mac model serving

### Deployment

- Use a dedicated Hermes namespace with persistent state at the image’s documented data path and a separate workspace\.
- Pin the official image\. Validate its startup/user requirements against the intended pod security settings rather than assuming the image runs under an arbitrary non\-root user\.
- Begin with one replica and a non\-overlapping update strategy if state is single\-writer\. Do not autoscale a shared agent home\.
- Disable service\-account token automount; provide no cluster\-admin credentials, host mounts, host networking or container\-runtime socket\.
- Select its execution backend explicitly\. Running commands locally inside the Hermes container shares its state and credentials; a separate execution sandbox is a stronger boundary\. The Docker backend is not automatically available inside a Kubernetes pod\.
- If adversarial\-code isolation is required, choose a separately isolated worker/virtual machine or validated sandbox runtime before rollout\. A namespace and network policy do not provide a separate kernel\.

### Model interface

- Mac runs the native model server with a stable private address, authenticated endpoint, known model identifier and restart/startup behaviour\.
- Store the endpoint as non\-secret app configuration and the token as an app Secret\.
- Restrict egress to the model port, name resolution and required services\. Enforce policy with the cluster’s actual network\-policy implementation and host/network controls; verify the source address observed by the Mac\.
- Keep Mac administration separate from model access\. Agent access to the model does not imply Secure Shell &#40;SSH&#41; access to the Mac\.
- Keep inference off public router forwards; use private network administration\.

Acceptance: tool\-use round trip through the chosen model; persistence after pod replacement; clear bounded timeout/retry when Mac is offline; recovery after it returns; inability to reach selected blocked cluster/host services; no access to cluster credentials\. Test backups/restores of Hermes state before relying on long\-term memory\.

Rollback: suspend Hermes, preserve its data, restore the previous image/configuration if compatible\. Do not silently downgrade state after an incompatible migration\.

## 14\. Final acceptance and operating documentation

Conduct one operator exercise using only documentation:

1. Onboard a web app and a background worker\.
2. Publish and deploy a new image; promote an existing digest\.
3. Diagnose a bad image and failing readiness check, then roll back\.
4. Rotate a Secret without printing it or remembering a service\-specific restart command\.
5. Demonstrate an unrelated app deploy during an observability outage\.
6. Suspend and resume reconciliation without deleting workloads\.
7. Uninstall a disposable persistent app and confirm declared data retention\.
8. Restore the selected stateful workload from independent backup\.
9. Demonstrate Hermes operation and graceful model\-server unavailability\.

Publish concise runbooks: bootstrap; add app; release/promote/rollback; inspect failures; rotate secrets; backup/restore; suspend/uninstall; administer external services\. Remove obsolete instructions and keep one canonical page per procedure\.

Only then consider directory renaming, a dashboard, Flux image\-update controllers, distributed storage or a local registry\. Each should solve a demonstrated remaining problem\.

## 15\. Evidence and references

- Repository baseline: https://github\.com/samclement/swhurl\-platform/tree/91e889353f656577b48efc6f85d9f1b4427852f7
- Existing lifecycle commands: `Makefile`; ownership: `clusters/home/flux-system/kustomizations.yaml` and `clusters/home/*.yaml`\.
- Existing app: `tenants/apps/example`; validation: `.github/workflows/validate.yml`; credential verification: `scripts/verify-platform.sh`\.
- Flux dependency, deletion and pruning semantics: https://fluxcd\.io/flux/components/kustomize/kustomizations/
- GitHub registry credentials and digest\-based pulls: https://docs\.github\.com/en/packages/working\-with\-a\-github\-packages\-registry/working\-with\-the\-container\-registry
- Certificate generation from ingress: https://cert\-manager\.io/docs/usage/ingress/
- Hermes container/state interface: https://hermes\-agent\.nousresearch\.com/docs/user\-guide/docker
- Model server network/authentication settings: https://lmstudio\.ai/docs/developer/core/server/settings

The original review checked shell syntax individually, local Kustomize path references, configuration validation and lifecycle dry\-runs\. It did not validate live state or fully render all resources\. The acceptance work above is proposed implementation verification, not work already completed\.
