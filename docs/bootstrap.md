# Bootstrap

Build the platform on a bare Linux host. The live host is already bootstrapped; use this page for a rebuild or a new host. The full sequence has not been run end to end since the Flux units were split (PR03); expect to adjust.

## 1. Host and tools

Install k3s with its defaults (flannel, packaged Traefik and metrics-server):

```bash
curl -sfL https://get.k3s.io | sudo INSTALL_K3S_EXEC="server" sh -
sudo cp /etc/rancher/k3s/k3s.yaml "$HOME/.kube/config"
sudo chown "$(id -u):$(id -g)" "$HOME/.kube/config" && chmod 600 "$HOME/.kube/config"
export KUBECONFIG=$HOME/.kube/config
kubectl -n kube-system get deploy traefik metrics-server
```

Local tools: `kubectl`, `flux`, `helm`, `sops`, `age`, `jq`, Python 3 with PyYAML. `d2` only for diagrams.

Point your router at the node: external `80 → 31514` and `443 → 30313` (Traefik NodePorts pinned in [`helmchartconfig-traefik.yaml`](../infrastructure/ingress-traefik/base/helmchartconfig-traefik.yaml)).

## 2. DNS

`*.homelab.swhurl.com` and `homelab.swhurl.com` are Route53 A records kept current by a systemd timer:

```bash
make host-dns DRY_RUN=true   # shows records, zone and profile
make host-dns                # installs the updater (needs sudo and AWS credentials)
```

Records come from `DYNAMIC_DNS_RECORDS` in [`config.env`](../config.env); override `AWS_ZONE_ID` or `AWS_PROFILE` in the environment. Re-run `make host-dns` after changing any of them. The wildcard covers single-label names only (`app.homelab.swhurl.com`, not `a.b.homelab.swhurl.com`).

## 3. Settings and Secrets

Non-secret cluster settings are in [`configmap-platform-settings.yaml`](../clusters/home/flux-system/sources/configmap-platform-settings.yaml): `CERT_ISSUER` (`letsencrypt-prod` or `letsencrypt-staging`) and `OAUTH_HOST` (the sign-in callback host).

Secrets are SOPS-encrypted files beside the service that uses them, encrypted to the age key in [`.sops.yaml`](../.sops.yaml). You need that private key (`age.agekey`, git-ignored; an encrypted backup exists off-host). To start with a new key instead, generate one with `age-keygen -o age.agekey`, put its public key (`age-keygen -y age.agekey`) in `.sops.yaml`, and re-create every Secret.

Set the platform Secrets ([conventions](operations.md#secrets)):

```bash
export SOPS_AGE_KEY_FILE=./age.agekey
sops platform-services/oauth2-proxy/base/secret-oauth2-proxy-shared.sops.yaml     # Google OAuth client-id, client-secret; cookie-secret
sops platform-services/clickstack/base/secret-clickstack-runtime-inputs.sops.yaml # CLICKSTACK_API_KEY (bootstrap key)
git commit -am "secrets: set platform secrets" && git push
```

The Google OAuth client must allow the redirect URI `https://<OAUTH_HOST>/oauth2/callback`. The OTel ingestion key is set later, in step 5.

## 4. Flux

```bash
flux check --pre
flux install --namespace flux-system
kubectl -n flux-system create secret generic sops-age \
  --from-file=age.agekey=./age.agekey --dry-run=client -o yaml | kubectl apply -f -
make flux-bootstrap   # applies the two root units and the Git/Helm sources
make install          # reconciles, then runs make verify-platform
```

Units come up in dependency order ([architecture](architecture.md#flux-units)). First image pulls can take several minutes; watch with `flux get kustomizations`. `make verify-platform` fails at this point on the ingestion key and on the MongoDB volume's reclaim policy; both are fixed below.

## 5. ClickStack first login and ingestion key

1. Open `https://clickstack.homelab.swhurl.com` and create the first team and user.
2. Copy the team's ingestion API key from the ClickStack UI.
3. Store it as `HYPERDX_API_KEY` in [`secret-hyperdx.sops.yaml`](../platform-services/otel/base/secret-hyperdx.sops.yaml). The file uses `data`, so encode it exactly once (`printf %s '<key>' | base64 -w0`); encoding twice silently breaks telemetry.
4. Commit, push, `make runtime-inputs-sync`. Reloader restarts the collectors.

## 6. Protect data, then verify

Set the ClickStack MongoDB volume to survive claim deletion ([recovery](operations.md#backups-and-recovery)):

```bash
pv="$(kubectl -n observability get pvc clickstack-mongodb -o jsonpath='{.spec.volumeName}')"
kubectl patch pv "$pv" -p '{"spec":{"persistentVolumeReclaimPolicy":"Retain"}}'
make verify-platform
make backup-clickstack-mongodb
```

`make verify-platform` checks every Flux unit, the HTTP→HTTPS redirect, the ingestion key (by bytes, never printed) and retention settings.
