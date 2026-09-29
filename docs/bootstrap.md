# Bootstrap

Build the platform on a bare Linux host. The live host is already bootstrapped; use this page for a rebuild or a new host. Steps 1, 4 and 6 were rehearsed end to end on a throwaway k3d cluster on 28 September 2026 ([evidence](current-state.md#bootstrap-rehearsal)) with self-signed certificates; DNS, router forwarding and Let's Encrypt on a new host are unrehearsed.

## 1. Host and tools

Install k3s with its defaults (flannel, packaged Traefik and metrics-server):

```bash
curl -sfL https://get.k3s.io | sudo INSTALL_K3S_EXEC="server" sh -
sudo cp /etc/rancher/k3s/k3s.yaml "$HOME/.kube/config"
sudo chown "$(id -u):$(id -g)" "$HOME/.kube/config" && chmod 600 "$HOME/.kube/config"
export KUBECONFIG=$HOME/.kube/config
kubectl -n kube-system get deploy traefik metrics-server
```

Local tools: `kubectl`, `flux`, `helm`, `sops`, `age`, `curl`, Python 3.11+ with PyYAML. `aws` for the DNS updater.

Point your router at the node: external `80 → 31514` and `443 → 30313` (Traefik NodePorts pinned in [`infra/traefik/helmchartconfig.yaml`](../infra/traefik/helmchartconfig.yaml)).

## 2. DNS

`*.homelab.swhurl.com` and `homelab.swhurl.com` are Route53 A records kept current by a systemd timer:

```bash
make host-dns DRY_RUN=true   # shows records, zone and profile
make host-dns                # installs the updater (needs sudo and AWS credentials)
```

Records come from `DYNAMIC_DNS_RECORDS` in [`host/dns.env`](../host/dns.env), which the unit reads on every run, so edits apply within 10 minutes without reinstalling; `AWS_ZONE_ID` and `AWS_PROFILE` can be added there too. The wildcard covers single-label names only (`app.homelab.swhurl.com`, not `a.b.homelab.swhurl.com`).

## 3. Settings and Secrets

Non-secret cluster settings are in [`configmap-platform-settings.yaml`](../clusters/home/flux-system/sources/configmap-platform-settings.yaml): `CERT_ISSUER` (`letsencrypt-prod` or `letsencrypt-staging`) and `BASE_DOMAIN` (parent domain of every platform host and the sign-in cookie; see [settings](services.md#settings)).

Secrets are SOPS-encrypted files beside the service that uses them, encrypted to the age key in [`.sops.yaml`](../.sops.yaml). You need that private key (`age.agekey`, git-ignored; an encrypted backup exists off-host). To start with a new key instead, generate one with `age-keygen -o age.agekey`, put its public key (`age-keygen -y age.agekey`) in `.sops.yaml`, and re-create every Secret.

Set the platform Secrets ([conventions](operations.md#secrets)):

```bash
export SOPS_AGE_KEY_FILE=./age.agekey
sops platform/oauth2-proxy/secret.sops.yaml     # Google OAuth client-id, client-secret; cookie-secret
sops platform/clickstack/secret.sops.yaml      # ingestion key, admin login, internal passwords
sops platform/otel/secret.sops.yaml            # the same CLICKSTACK_INGESTION_KEY
git commit -am "secrets: set platform secrets" && git push
```

The Google OAuth client must allow the redirect URI `https://oauth.<BASE_DOMAIN>/oauth2/callback`. Generate the ingestion key yourself (for example `uuidgen`) and put the same value in both ClickStack and OTel files; `make check-secrets` checks they match ([what each value is](services.md#clickstack-and-otel)).

## 4. Flux

```bash
flux check --pre
make flux-install     # Flux at FLUX_VERSION (tools/swhurl/flux.py), with the settings in Git
kubectl -n flux-system create secret generic sops-age \
  --from-file=age.agekey=./age.agekey --dry-run=client -o yaml | kubectl apply -f -
make flux-bootstrap   # applies the two root units and the Git/Helm sources
make install          # reconciles, then runs make verify-platform
```

Units come up in dependency order ([architecture](architecture.md#flux-units)). First image pulls can take several minutes; watch with `flux get kustomizations`. `make verify-platform` fails at this point on the ingestion key and ClickStack sign-up until step 5.

## 5. ClickStack admin and ingestion key

```bash
make clickstack-bootstrap
```

It waits for the HyperDX API, registers the admin account from SOPS (HyperDX then closes registration) and writes `CLICKSTACK_INGESTION_KEY` into the team, so the collectors' data is accepted within a minute. Until it runs, the ClickStack UI (behind Google sign-in) offers open registration. To keep an old team, users and dashboards, restore a MongoDB backup first ([backups and recovery](operations.md#backups-and-recovery)), then run it.

## 6. Verify and back up

```bash
make backup-mongodb     # first backup, locally and to S3
make host-backup         # daily timer (asks for sudo)
make verify-platform
```

`make verify-platform` checks every Flux unit, the HTTP→HTTPS redirect, the ingestion key (by bytes, never printed), that ClickStack registration is closed, retention settings and the MongoDB volume's reclaim policy.
