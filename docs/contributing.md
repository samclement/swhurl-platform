# Contributing

How to change this repo without breaking the cluster or the docs. Commits go straight to `main`; CI ([`.github/workflows/validate.yml`](../.github/workflows/validate.yml)) is the gate, and Flux applies `main`.

## Validation

Run before every push:

```bash
make validate-repo test-safety app-policy
```

- `validate-repo` renders every active Flux path, validates Kubernetes and Flux schemas, checks SOPS structure, shell syntax and required substitutions, and checks that relative links in Markdown resolve. It never contacts the cluster.
- `test-safety` runs the offline unit tests in `tests/`: lifecycle guards, verifier secrecy, sign-in and Reloader policy, Flux unit rules, the app generator and policy.
- `app-policy` renders app instances with Helm and checks the app contract.

Prerequisites (CI pins the same): `kubectl`, `helm`, Python 3 with PyYAML (`requirements-validation.txt`), and `flux-schema`:

```bash
go install github.com/fluxcd/flux-schema/cmd/flux-schema@v0.9.0
export PATH="$(go env GOPATH)/bin:$PATH"
```

CI also runs every `DRY_RUN=true` target and `REQUIRE_HELM=1` so Helm-based tests cannot silently skip. `make secrets-check` needs the age key, so it runs locally only.

## Checklist for a change

- **Where it goes:** a shared capability gets its own directory and Flux unit in `clusters/home/infrastructure.yaml` or `platform.yaml`, with explicit `dependsOn`, `deletionPolicy: Orphan`, substitution only if its manifests use `${...}`, and decryption only if its path holds `*.sops.yaml`. Apps use `make app-new`.
- **Secrets:** beside the consumer, following the [Secret rules](operations.md#secrets); add a `.sops.yaml` rule for any new path.
- **Checks:** extend `scripts/verify-platform.sh` for live invariants and `tests/` for anything checkable offline. Prefer declarative wiring over new `FEAT_*` switches (`FEAT_VERIFY` is the only one).
- **Live proof:** for behaviour that only shows on the cluster, add or run a throwaway test like `make lifecycle-test`, then record what you saw, with the date and commit, in [current state](operations/current-state.md).
- **Root units** in `clusters/home/flux-system/kustomizations.yaml` are not reconciled by Flux; run `make flux-bootstrap` after changing them.
- **Generator changes:** regenerate the fixtures (`rm -rf tests/fixtures/apps && tests/fixtures/apps.sh`); `test-safety` fails if they drift.

## Documentation

- Update the docs in the same commit as the behaviour. Each topic has one page; link rather than repeat:

  | Topic | Page |
  | --- | --- |
  | Everyday loop, task index | [README](../README.md) |
  | Bare host to running platform | [bootstrap](bootstrap.md) |
  | Health, Secrets, certificates, lifecycle, backups, troubleshooting | [operations](operations.md) |
  | Every `make` target | [commands](commands.md) |
  | Shared services, settings, keys | [services](services.md) |
  | App instances, generator, policy | [apps](apps.md) |
  | Units, dependencies, ownership | [architecture](architecture.md) |
  | Dated live evidence | [current state](operations/current-state.md) |
  | Remaining planned work | [plan](../Swhurl-platform-implementation-plan.md) section 0 |

- Describe what is, not what was: git history records removals. ADRs in `docs/adr/` are historical decisions.
- Put a caveat next to the step it affects. Mark anything not verified on the cluster.

## Diagrams

C4 views are D2 sources in `docs/charts/c4/`; render with `make charts-generate` and commit the SVGs. With D2's default layouts only root-level `direction` applies, so place containers with `grid-rows`/`grid-columns` wrappers: an external row on top, the cluster below, request flow top-down and lanes left-to-right (edge, platform services, apps). Split sections with more than three or four nodes, add edges after placement, keep edge labels on representative edges only, and title charts with `diagram_title` at `near: top-left`. Show `Let's Encrypt (ACME)` wherever cert-manager appears, and the telemetry path (app → OTel collector → ClickStack) in the container view.
