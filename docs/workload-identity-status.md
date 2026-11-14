# Workload identity (SPIFFE): progress, remaining phases and the next gate

Status: proposed
Gate: phase 0, the issuer decision brief
Uncommitted: none

**The goal: every workload has one identity, and every channel checks it.** Today a workload proves who it is in a
different way on each channel: an audience-bound Kubernetes token on a call, its own NATS key on events, the proxy port
it was allocated for egress. When this is done, each instance has one [SPIFFE](https://spiffe.io) ID, derived from
what the records already hold (namespace `<app>-<env>`, service account `<app>`), for example
`spiffe://homelab.swhurl.com/ns/demo-shop-staging/sa/demo-shop`. It proves that ID with a short-lived X.509 certificate
over mutual TLS, and the callee, NATS and the egress proxy all check the same ID. The records stay the only source of
**who may do what** (`calls`/`allows`, the event permissions, `grants.egress`). SPIFFE replaces only **how a workload
proves who it is**, and removes the credentials that are each channel's own today.

This page is the one place this work's progress, phases and decisions live. Dated evidence is in
[current state](current-state.md); the registry is [workstreams](workstreams.md); the contracts it changes are in
[apps](apps.md#calling-another-app), [events](apps.md#events), [reaching outside the cluster](apps.md#reaching-outside-the-cluster)
and [signing keys](operations.md#signing-keys). Rows are overwritten in place; history is Git.

## Resume here

1. Read this page only, then `git log --oneline -5 -- docs/workload-identity-status.md`.
2. Phases that touch `v2/` follow the v2 [design principles](iteration-2-plan.md#design-principles): the ID is a pure
   function in the model, certificates are mounted by the platform, and no `kubectl`. Update [v2 status](v2-status.md)
   in the same change.
3. Anything new that runs or holds a credential (the issuer, the CA key) goes through `new-component-checklist`.

**Next:** phase 0. Write the decision brief for D1 to D3 (documentation only, nothing applied), then ask the operator to
approve the scope and move this row to `approved`.

## Where it stands

These rows are today's identity on each channel, the baseline this work replaces. `partial` means a workload is
identified on that channel, but by that channel's own credential rather than the shared ID.

| Capability | State | Evidence |
| --- | --- | --- |
| App → app calls: a Kubernetes token for the callee's audience (10 minutes), the cluster's signing keys copied into the profile's `auth` block and rendered into each namespace, a callers file, NetworkPolicy | partial | [record](current-state.md#a-rotation-and-a-new-caller-with-no-restart-29-october-2026) |
| Events: one NATS key per instance, its user generated into `platform/nats/users.yaml` | partial | [record](current-state.md#v2-minted-a-broker-identity-for-both-keyless-instances-29-october-2026) |
| Egress: identity by the proxy port allocated to the instance (`platform/egress/allocation.json`), enforced by NetworkPolicy | partial | [record](current-state.md#the-egress-cutover-v2-renders-the-proxy-and-grants-are-enforced-26-october-2026) |

## Remaining work

Ordered. Each phase leaves the platform working. A channel runs the old and the new mechanism side by side until every
instance uses the new one, and only then is the old one removed. Each phase is checked offline (`make check-v2`,
`make check-apps`), then live with a throwaway `swhurl-try-<n>` pair, and gets a dated `current-state.md` entry.

**P0. Decide the issuer** (decision brief: D1 to D3). Documentation only.

**P1. The identity in the model, offline.** A pure kernel function derives an instance's SPIFFE ID from its app,
environment and the trust domain in the profile, and [GLOSSARY](../GLOSSARY.md) defines it. Nothing is rendered yet.
Start at `v2/kernel/identity.go` and `v2/testdata/profile.json`.

**P2. A trust domain on the cluster.** With D1's recommendation:
- a cert-manager CA issuer;
- `csi-driver-spiffe` and its approver, which issue a certificate for a pod's own service account only;
- trust-manager, which publishes the CA bundle into every managed namespace.

These form one new Flux unit after cert-manager, with the CA key's backup and rotation settled (D3). This is the first
live change, and nothing uses it yet. Acceptance: a test pod's mounted certificate names its own ID and rotates.

**P3. Calls move to mutual TLS.**
- v2 renders the certificate volume.
- The templates (TypeScript, Kotlin) present the certificate on a call and reload it as it rotates.
- A callee accepts **either** a peer certificate whose ID is in its callers file **or** today's token.
- Health probes stay plain HTTP (D4).

Once every instance runs a template that does this, remove the tokens, the profile's `auth` block, its
`verify-platform` comparison and the `call-tokens` rule. NetworkPolicy stays as a second layer of defence.

**P4. Events.** NATS requires a client certificate and maps its ID to the user generated from the records
(`verify_and_map`). Retire the per-instance NATS keys after a period that accepts both. Start with a spike: NATS's
mapping by URI SAN is not verified.

**P5. Egress (optional).** The proxy identifies a client by its certificate instead of its port, and the port
allocation goes. Spike first: many HTTP clients cannot speak TLS to a proxy. Drop the phase if the templates' Node.js
and JVM clients cannot.

**P6. Outside the cluster (optional).** Backups get AWS credentials through OIDC federation instead of the static
`backup-writer` key. This also closes the plan's item to stop using IAM user `sam`. The cluster's own service-account
tokens could do this without SPIFFE, so the brief decides whether it belongs here.

### Open decisions

- **D1. Issuer.**
  - (a) cert-manager `csi-driver-spiffe` with trust-manager: reuses cert-manager, one DaemonSet and an approver, no
    datastore.
  - (b) SPIRE: its own server, agents and datastore. Stronger attestation and federation, which one node does not need.
  - (c) A service mesh (for example Linkerd): mutual TLS without changing the apps, but a much larger change.

  Recommended: (a).
- **D2. Trust domain and ID form.** `homelab.swhurl.com` with the driver's fixed `ns/<namespace>/sa/<account>` form
  (recommended, it needs no new name), or a platform-shaped `env/<env>/app/<app>` path, which needs SPIRE.
- **D3. CA key custody.** A self-signed root kept by cert-manager in the cluster, or a root kept offline that signs an
  in-cluster intermediate. Also: how the root is backed up and rotated, and what a lost key costs.
- **D4. Health probes.** `/healthz` stays plain HTTP on the app's port (recommended), or moves to its own port.

## Deferred features

| Feature | v2 today | Reopen when |
| --- | --- | --- |
| People's identity | Google sign-in through oauth2-proxy; an app names a person with `x-swhurl-user` on a call it authenticates | A call must prove the person, not only the app that names them |
| Federation with another trust domain | One cluster, one trust domain | A second cluster or an outside workload must call in |
| GitHub and registry credentials | The console's tokens and the cleanup tokens are static, in SOPS | GitHub accepts a workload identity for them |
| Attestation beyond the service account | A pod's ID is its namespace and service account; nothing checks the image | An ID must depend on what the pod runs |

## Durable decisions

- The records stay the only authority for grants; an ID only names who is asking.
- An instance's ID is derived from its namespace and service account; nothing stores it.
- A channel accepts both mechanisms until every instance uses the new one; removal is its own change.
- NetworkPolicy stays when a channel moves to mutual TLS.

## How this page is kept

- **At the end of every session** that worked on this, in the same commit as the work:
  1. Overwrite the capability rows the session moved (`done` or `partial`, one dated evidence link). A row becomes
     `done` when its channel checks the shared ID and its own credential is gone.
  2. Delete the delivered phase from Remaining work and the decisions it settled; move a settled rule to Durable
     decisions in one line.
  3. Rewrite **Next** in "Resume here".
  4. Set `Status`, `Gate` and `Uncommitted` here and the same `State` and `Next gate` in
     [workstreams](workstreams.md). Only one workstream may be `in progress`.
  5. Add one dated [current state](current-state.md) entry with the commands run and what was seen.
- Add this workstream's paths to its registry row when its first phase changes files outside `docs/`.
- A map, not a log: history is `git log -p -- docs/workload-identity-status.md`. Contracts stay on their owning pages
  and are linked. `make check-docs` enforces the shape, the registry match and the 250-line cap.
