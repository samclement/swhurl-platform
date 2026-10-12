# Glossary

The words this repository uses for its own concepts, one meaning each. Code, commands, the console and the docs use them as defined here; each entry links to the page that explains the thing itself.

| Term | Meaning | Avoid |
| --- | --- | --- |
| **App** | What a team deploys: one repository, one image, one name (`demo-orders`). The name is a DNS label of at most 40 characters ([apps](docs/apps.md)). | service (kept for what the platform provides), project |
| **Environment** | Where an app runs: `staging` or `prod`. In code, `env`. | stage, tier |
| **Instance** | One app in one environment (`demo-orders/staging`). Everything about it is named from that pair: the namespace `<app>-<env>`, the Flux unit `app-<app>-<env>` in `clusters/home/app-<app>-<env>.yaml`, and its manifests in `apps/<app>/<env>/`. [`instance.py`](tools/swhurl/apps/instance.py) is the one place that says so ([apps](docs/apps.md)). | deployment, release, target |
| **Unit** | One Flux `Kustomization` in `flux-system`: what Flux applies and reports on as a whole. An instance has exactly one; the platform's own are listed in [architecture](docs/architecture.md#flux-units). | layer, component |
