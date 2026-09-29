# Console

A web page at `https://console.<BASE_DOMAIN>` (`console.homelab.swhurl.com`) for looking at the platform and changing it without a terminal. Sign in with the same Google account as every other signed-in host. It changes the cluster in only three ways (reconcile, suspend and resume a Flux unit); every other change is a **pull request** it opens against this repo, which you review and merge like any other.

## Use it

| Page | Shows | Buttons and what they do |
| --- | --- | --- |
| **Apps** | Each app instance: Flux unit state and desired image | — |
| An app (`/apps/<app>/<env>`) | What `make app-status` shows: Git revision desired vs applied, image desired vs running, replicas, route, TLS, failing containers | **Reconcile** its unit. **Scale** (replicas and resources; current values in grey, empty fields unchanged), **Promote to prod** (staging only) and **Uninstall** each open a PR |
| **Flux units** | Every unit, in columns by `dependsOn`, with state and applied revision | **Reconcile**, **Suspend** / **Resume**. None on `cluster-sources` and `cluster-stack`, which `make flux-bootstrap` applies |
| **Platform** | The `make verify-platform` checks that need only cluster reads: Flux units and the HTTPS redirect | — (run `make verify-platform` for the rest) |
| **New app** | A form with the `make app-new` options; empty fields show the default that will be used | **Open pull request** |
| **Jobs** | Every action since the console last started, who ran it, and its output | — |

Each button starts a **job**: its page shows the output as it arrives and refreshes until it finishes; one job per unit or app instance at a time.

- **Cluster actions** make the changes `flux reconcile kustomization <unit> --with-source`, `flux suspend` and `flux resume` would, with `kubectl` (the image has no `flux` CLI): suspend and resume set the unit's `spec.suspend`; reconcile sets the `reconcile.fluxcd.io/requestedAt` annotation on the unit's Git source and then on the unit, and resume on the unit. Each waits (up to 10 minutes) until Flux reports the request handled, then shows the revision or the unit's error. A suspended unit is not reconciled: resume it. Suspending stops Git changes reaching the unit, as `make suspend` does ([lifecycle](operations.md#lifecycle)).
- **PR actions** download `main` through GitHub's API (the image has no `git`), run that tree's own `make app-new`, `app-promote`, `app-scale` or `app-remove` ([apps](apps.md)), so the change follows the rules CI will check, then commit the files it added, changed or deleted to a new `console/<change>-<app>-<env>-<commit>` branch (blobs, tree, commit and ref through the API; the job lists each file as `A`, `M` or `D`) and open a PR titled `[console] …` with a `Requested-by: <email>` line. If the command refuses (for example prod without a digest), nothing is written to GitHub and the job shows why. After merging: `make flux-reconcile`, or wait for Flux. Set any `REPLACE_ME` Secret values with `sops` on the PR branch before merging.
- **Audit:** every job logs `[AUDIT] <email> <action> <target>: started|succeeded (job N)` (with the PR's URL), or `[ERROR] … failed`; search `[AUDIT]` in ClickStack. The Jobs page itself is lost on restart.

## Deploy a new console

The image is built from this repo ([`images/console/Dockerfile`](../images/console/Dockerfile)) and published to `ghcr.io/samclement/swhurl-console` (public) after CI passes on `main`, tagged `src-<hash of its inputs>`. The same "Publish console image" run then deploys it: on the latest `main` it runs `swhurl console-image --expect src-<hash> --commit`, which pins the tag and digest in `platform/console/helmrelease.yaml` and pushes a `deploy: console image src-<hash> (publish workflow)` commit as `github-actions[bot]`; the [push webhook](services.md#push-webhook) has Flux apply it at once. So a push that changes `tools/`, `images/console/`, `pyproject.toml` or `uv.lock` is running in the console about 3 minutes later, with nothing to do:

```text
push → Validate (≈25s) → build and push the image (≈1 min) → bot commits the pin → webhook → console rolls out
```

- **Pull before your next push** (`git pull --rebase`): the bot's commit is on `main`.
- **Skipped on purpose** when `main` has moved on to other image inputs by the time the run pins (that commit's own run pins its image), and when the pin is already current (docs-only commits). Each run's summary says which.
- **The console restarts** when its pin changes; a job running at that moment stops, and the Jobs page is emptied as on any restart.
- **Pushes made with the workflow's token start no workflows**, so the pin commit runs neither Validate nor another publish.
- **By hand** (the run failed, or to pin an older image): `make console-image`, commit, push. `make verify-platform` warns while the running image is older than your checkout's tooling.

A commit that changes none of the image's inputs has the same hash: nothing to deploy. How the image and workflow are built: [contributing](contributing.md#operator-tooling).

## GitHub token

A fine-grained token for this repository only (Contents and Pull requests, read and write) in [`platform/console/secret.sops.yaml`](../platform/console/secret.sops.yaml). PRs and pushes appear as the token's owner. `make verify-platform` checks GitHub accepts it and warns 14 days before it expires. Replace it: [operations](operations.md#secrets); Reloader restarts the console.

## How it is protected

- **Who:** oauth2-proxy returns the signed-in email as `X-Auth-Request-Email` (it needs `set-xauthrequest`) and Traefik copies it to the request; without it the console answers 401 (except `/healthz`). A NetworkPolicy admits only Traefik's pods, so no other pod can send a forged header. A POST must carry an `Origin` naming the console's own host (403 otherwise), so another website cannot use your sign-in cookie to start an action.
- **What it may do in the cluster:** its ServiceAccount `console/console` may read Flux units, HelmReleases, the Git source, pods, workloads, Ingresses and Certificates, and in `flux-system` only patch Kustomizations and GitRepositories ([`rbac.yaml`](../platform/console/rbac.yaml)). No Secrets, no `pods/exec`, nothing else writable. RBAC cannot limit which fields a patch changes; the console's code does, and refuses the root units.
- **The token:** used only in the `Authorization` header of the console's GitHub API requests (download `main`, create a `console/*` branch and its commit, open the PR); never in a command line and redacted from all output. `main` is not protected, so the limit to `console/*` branches is the console's code (tested).
- **State:** none. It stores nothing; `/tmp` is a 1 GiB `emptyDir` for downloaded trees.

## Run it locally

`make console-dev` serves the same pages from your checkout on `http://127.0.0.1:8080` as a fixed identity (`operator@localhost (dev)`), refusing any address but loopback. It reads the cluster **with your kubeconfig**, so its buttons act with your admin rights, and it opens real PRs if `GITHUB_TOKEN` is set in your environment. Code layout: [contributing](contributing.md#operator-tooling).
