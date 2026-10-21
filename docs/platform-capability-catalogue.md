# Developer platform capability catalogue

For the proposed Azure AKS platform, the scope can extend beyond **“build a container and run it”** to **“help a team build, connect, secure, release and operate a service throughout its lifecycle.”**

The table below is a **menu of potentially valuable capabilities, not a recommendation to build everything**. Some can be provided by integrating existing Azure or enterprise services rather than building platform-owned alternatives.

## How to read “Exists”

This column describes **the current POC**, not the proposed AKS platform:

- **Yes** — implemented for the POC’s supported applications, including its integrated templates and workflows.
- **Partial** — useful foundations exist, but the complete capability or self-service experience is missing.
- **No** — not found as an implemented platform capability; documentation-only plans do not count.

**A “Yes” demonstrates the capability, not enterprise readiness or an Azure implementation.** In particular, the POC is single-node k3s; its teams are ownership labels rather than human access-control boundaries.

## 1. Developer onboarding and application creation

| Feature | Exists | Use case it supports |
| --- | --- | --- |
| Self-service developer portal | **Yes** | Discover applications, inspect environments and request supported changes without using Kubernetes directly. |
| Application scaffolding and repository creation | **Yes** | Start with a working repository, pipeline and staging deployment rather than assembling them manually. |
| Supported application templates | **Yes** — TypeScript and Kotlin | Start with an approved framework, health checks, telemetry and platform integration already configured. |
| Different workload types | **Yes** — web apps, APIs, workers and scheduled jobs | Choose a suitable starting point for an interactive application, service or background workload. |
| Application-owned requirements declaration | **Yes** — `swhurl.yaml` | Declare ports, health checks, resources, secret names and other requirements alongside application code. |
| Automated template maintenance | **Yes** — reviewed Copier updates | Receive improvements to shared application foundations without manually copying changes between repositories. |
| Team ownership and application descriptions | **Yes** | Identify who owns a service and filter applications by responsible team. |
| Rich service catalogue and support metadata | **Partial** — ownership and API/event descriptions exist | Find a service’s purpose, maturity, support contact, runbook and lifecycle status before depending on it. |
| Supported local development environment | **No** — no integrated platform-wide setup | Run an app with local dependencies, test identities, events and seeded data without cloud credentials. |
| Import existing applications and arbitrary images | **No** — supported template lineage is required | Onboard established services without recreating them from a platform template. |
| Private source repositories and images | **No** | Build and deploy proprietary applications without exposing source or container images publicly. |

## 2. Build, release and environment management

| Feature | Exists | Use case it supports |
| --- | --- | --- |
| Standard application CI pipelines | **Yes** | Compile, test, build and smoke-test an application consistently. |
| Automated deployment to staging | **Yes** | See a merged application change running without a separate deployment ticket. |
| Immutable image promotion | **Yes** | Promote the exact image tested in staging to production without rebuilding it. |
| Independent environment configuration and data | **Yes** | Use separate production credentials, settings and databases rather than copying staging values or data. |
| GitOps reconciliation | **Yes** | Keep deployed configuration aligned with reviewed, version-controlled desired state. |
| Pull-request change review and conditional auto-merge | **Yes** | Automate routine changes while retaining review for access changes, first production deployments and other higher-risk operations. |
| End-to-end deployment progress | **Yes** | Distinguish “PR opened”, “merged”, “applied” and “the requested image is running”. |
| Safe rollback experience | **Partial** — older-image promotion and Helm remediation | Recover a release through an explicit workflow that also checks configuration and database compatibility. |
| Pull-request preview environments | **No** | Give reviewers a temporary URL and isolated environment for each proposed change. |
| Coordinated multi-service releases | **No** | Release compatible changes across several services with explicit ordering and verification. |
| Canary or blue/green releases | **No** | Expose a new version gradually, compare health and abort before affecting all users. |
| General environment lifecycle and expiry | **No** | Create test environments beyond staging/production and automatically remove temporary ones. |

## 3. Identity, authentication and authorisation

| Feature | Exists | Use case it supports |
| --- | --- | --- |
| Shared browser sign-in | **Yes** — Google-based in the POC | Protect applications without each team implementing a separate login flow. |
| Microsoft Entra ID integration | **No** | Use corporate identities, access policies and group membership to access the platform and applications. |
| App-to-app authentication | **Yes** — internal platform callers | Establish which application is making an API request without distributing long-lived shared tokens. |
| Automatic service-token refresh | **Yes** | Keep application calls working without developers implementing credential rotation. |
| API-owned caller grants | **Yes** — named apps and team grants | Let an API explicitly control which platform applications may connect to it. |
| Calls on behalf of a signed-in person | **Yes** — caller-asserted identity | Preserve both the calling application and the person it represents across service calls. |
| Endpoint- and business-level authorisation | **No** — application responsibility today | Allow an app to read orders but not cancel them, or restrict access according to business roles. |
| External M&S application and partner authentication | **No** | Admit callers outside the platform through approved identity federation or client credentials. |
| Azure workload identity | **No** | Let applications access Azure services without storing client secrets. |
| Human team-based platform permissions | **No** — teams are labels | Allow developers to manage their own services without granting control over other teams’ services. |
| Owner-routed access requests | **Partial** — grants and PR review exist | Request access to an API and route approval to its responsible team. |
| Time-limited elevated access | **No** | Grant emergency or diagnostic permissions with expiry, approval and an audit trail. |

## 4. Secrets and application configuration

| Feature | Exists | Use case it supports |
| --- | --- | --- |
| Third-party API secret storage and injection | **Yes** — encrypted Git and runtime environment variables | Supply a payment-provider token without committing its plaintext value to source control. |
| Environment-specific secret values | **Yes** | Keep sandbox and production credentials independent. |
| Secret-change propagation | **Yes** — automatic workload restart | Make a newly supplied credential take effect without a manual restart. |
| Missing and unused secret detection | **Yes** | Identify undeclared dependencies, missing configuration and credentials that may no longer be needed. |
| Team-scoped, write-only secret self-service | **No** — operator CLI and shared encryption key today | Let a developer update their app’s secret without seeing other secrets or obtaining a platform-wide key. |
| Azure Key Vault integration | **No** | Consume centrally governed secrets and certificates using enterprise access controls. |
| Automated credential issuance and rotation | **No** | Rotate the credential at its provider, update consumers and verify continued operation. |
| General typed runtime configuration | **Partial** — typed flags and selected platform settings | Validate application settings and update appropriate values without rebuilding the image. |

## 5. API discovery, consumption and lifecycle

| Feature | Exists | Use case it supports |
| --- | --- | --- |
| API catalogue with routes and descriptions | **Yes** | Find an existing API and understand its operations before building another one. |
| API specifications tied to deployed releases | **Yes** | See the contract belonging to the version running in each environment. |
| Service discovery and API client helpers | **Yes** | Call another application without hard-coding its address or implementing platform authentication. |
| Declared consumer and dependency map | **Yes** | See which applications are connected and which teams own them. |
| Actual caller identification | **Yes** — logs and traces | Determine which app made a particular request and, when propagated, which person it represented. |
| Consumer-by-endpoint usage analytics | **Partial** — caller telemetry exists, packaged reports do not | See traffic, errors and last-used dates by caller and endpoint. |
| API deprecation and retirement workflow | **No** | Notify consumers, track migration and retire an endpoint with evidence that it is no longer required. |
| API breaking-change detection | **No** | Warn or block a release that removes an operation or changes a response incompatibly. |
| Consumer-driven contract testing | **No** | Prove that a proposed provider release still satisfies the expectations of its consumers. |
| Interactive API sandbox, mocks and generated clients | **No** — descriptions and code samples exist | Try a service, develop against it before it is available and generate clients from its contract. |
| API gateway products, quotas and rate limits | **No** | Publish APIs to wider consumers with usage plans, subscriptions and protection against excessive traffic. |

## 6. Events and asynchronous processing

| Feature | Exists | Use case it supports |
| --- | --- | --- |
| Managed event publishing and subscriptions | **Yes** — NATS JetStream | Connect services asynchronously without each team operating a broker. |
| Event catalogue with schemas and examples | **Yes** | Discover published events and understand their payloads. |
| Publisher-side schema validation | **Yes** | Catch malformed event payloads before publishing them. |
| Durable consumption and bounded retries | **Yes** | Receive events missed during downtime and retry failed processing within documented retention limits. |
| Trace propagation through events | **Yes** | Follow a business operation from an HTTP request into asynchronous consumers. |
| Protection against removing subscribed events | **Yes** | Prevent a producer from dropping an event while declared subscribers still depend on it. |
| Event payload compatibility checks | **No** | Detect breaking field or type changes before deploying a producer. |
| Consumer lag, retry and failed-message dashboard | **No** — general logs and traces exist | See that a consumer is falling behind or repeatedly failing before users report missing work. |
| Dead-letter inspection and controlled replay | **No** | Diagnose exhausted messages and replay selected work after a fix. |
| Azure messaging and external event integration | **No** | Connect to Service Bus, Event Grid or enterprise event platforms without custom integration setup for every app. |
| Transactional outbox and idempotency patterns | **No** — no standard platform capability | Avoid losing events after database writes or processing duplicate deliveries incorrectly. |

## 7. Feature management and experimentation

| Feature | Exists | Use case it supports |
| --- | --- | --- |
| Typed feature flags | **Yes** | Turn functionality on or off with compiler-checked names and value types. |
| Environment-specific flag defaults | **Yes** | Enable a new flow in staging while leaving production unchanged. |
| Live feature-flag overrides | **Yes** | Disable a faulty feature without rebuilding or restarting an app. |
| Feature-flag audit and stale-override visibility | **Yes** | See when an override was applied and identify overrides that have become long-lived. |
| User, team or tenant targeting | **No** | Enable a feature for internal testers or selected customers before wider release. |
| Percentage rollout and experimentation | **No** | Release to a controlled cohort and measure business or reliability outcomes. |
| Scheduled changes and flag retirement workflow | **Partial** — stale overrides are highlighted | Schedule activation, assign an owner and remove obsolete flag code and overrides. |

## 8. Azure resource provisioning and application binding

These capabilities could be delivered through **ASO, approved infrastructure modules or another provisioning system**. The developer-facing feature is the outcome, not the provisioning tool.

| Feature | Exists | Use case it supports |
| --- | --- | --- |
| Self-service Azure resource catalogue | **No** | Request an approved cloud service without writing low-level infrastructure definitions. |
| Azure Blob Storage provisioning | **No** | Give an app somewhere to store receipts, documents or uploaded media. |
| Managed relational database provisioning | **No** | Request an approved Azure SQL or PostgreSQL configuration with clear operational ownership. |
| Managed NoSQL database provisioning | **No** | Obtain a document or key-value database where the application’s access pattern requires it. |
| Managed cache provisioning | **No** | Reduce repeated computation or database load through an approved shared-service pattern. |
| Service Bus and Event Grid provisioning | **No** | Create queues, topics and subscriptions with approved settings and permissions. |
| Automatic application binding | **No** for provisioned Azure services | Receive endpoints, identity configuration and scoped access without manually copying outputs between systems. |
| Infrastructure reconciliation and drift handling | **Partial** — Kubernetes resources only | Detect and reconcile changes to Azure resources as well as deployed workloads. |
| Approved resource sizes, regions and configurations | **No** for Azure resources | Request infrastructure within cost, resilience and compliance guardrails. |
| Cloud resource inventory and ownership | **No** | See every Azure resource associated with an app, its environment and its owner. |
| Safe infrastructure retention and deletion | **Partial** — retained local app data only | Remove an application without accidentally deleting a cloud database or leaving unowned billable resources. |
| Adoption of existing cloud resources | **No** | Connect an app to infrastructure that another team already owns without recreating or taking inappropriate ownership of it. |

## 9. Data management and recovery

| Feature | Exists | Use case it supports |
| --- | --- | --- |
| Persistent application storage | **Yes** — SQLite on retained volumes | Keep application data across pod restarts and deployments. |
| Database migration execution | **Yes** — template startup migrations | Apply schema changes consistently with application releases. |
| Automated encrypted backups | **Yes** — SQLite and platform MongoDB to S3 | Recover supported data after corruption, accidental modification or storage loss. |
| Backup freshness verification | **Yes** | Identify databases whose expected backup has not completed recently. |
| Verified restore tooling and restore tests | **Yes** — supported databases | Restore a checked backup and exercise the recovery path rather than assuming archives are usable. |
| Team-scoped self-service restore | **No** — operator tooling today | Recover an app’s data through an authorised workflow without broad storage credentials. |
| Point-in-time recovery | **No** | Restore to just before an accidental deletion rather than the last nightly backup. |
| Sanitised production-like test data | **No** | Reproduce realistic issues without exposing personal or sensitive production data in staging. |
| Migration compatibility and rollback checks | **Partial** — some unsafe transitions are refused | Establish whether an older app version can safely run after a schema migration. |
| Data retention, archival and erasure policies | **No** — no general application-data service | Meet business retention and deletion requirements without each team inventing its own process. |

## 10. Networking, exposure and integration

| Feature | Exists | Use case it supports |
| --- | --- | --- |
| Private, signed-in and public application exposure | **Yes** | Choose whether an application is internal-only, available to authenticated people or publicly reachable. |
| Managed HTTPS certificates | **Yes** | Expose an app securely without manually issuing and renewing its TLS certificate. |
| Hostname and routing setup | **Partial** — generated routes and homelab DNS setup | Obtain a usable application address without manually coordinating ingress and DNS records. |
| Inbound application network isolation | **Yes** | Block undeclared pod-to-pod connections before they reach the application. |
| External dependency declarations and visibility | **Yes** — grants and proxy logs | Record and inspect the external hosts an application uses. |
| Enforced outbound network restrictions | **No** — observation only today | Prevent an app from connecting to unapproved external destinations. |
| Azure private connectivity and private DNS | **No** | Reach Azure services or corporate systems without exposing them publicly. |
| Web application firewall and edge protection | **No** | Protect public applications against common attacks and abusive traffic. |
| Standard outbound resilience policies | **No** | Apply appropriate timeouts, retry budgets and circuit breaking when calling unreliable dependencies. |

## 11. Observability, support and incident response

| Feature | Exists | Use case it supports |
| --- | --- | --- |
| Centralised searchable application logs | **Yes** | Investigate failures without accessing individual pods. |
| Distributed tracing and log correlation | **Yes** | Follow a request across services and connect its logs to the relevant trace. |
| Application and infrastructure metrics | **Yes** | Inspect traffic, latency, failures and resource behaviour through shared telemetry. |
| Automatically provisioned application dashboards | **Yes** | Get useful operational views as soon as an app is deployed. |
| Default application-error alerts | **Yes** | Notify the owning team about repeated errors without requiring each team to create baseline rules. |
| Deployment, prolonged-health and recovery notifications | **Yes** | Know whether a release succeeded, stalled, failed or recovered. |
| Team notification channels and application messages | **Yes** | Route operational and application-specific messages to the responsible team. |
| External availability and synthetic journey checks | **No** | Verify that HTTPS, sign-in and meaningful user actions work from outside the cluster. |
| Service-level objectives and error budgets | **No** | Judge reliability against agreed user-facing targets rather than pod readiness or fixed error counts. |
| Release-aware observability | **Partial** — change records and telemetry exist separately | Overlay deployments, configuration changes and flag changes on error and latency charts. |
| Incident workflow, runbooks and on-call integration | **Partial** — notifications and troubleshooting documentation | Assign incidents, acknowledge alerts and guide responders through service-specific recovery. |
| Telemetry privacy and sensitive-data controls | **Partial** — targeted secret redaction exists | Prevent personal data and credentials from entering logs, traces or unrestricted dashboards. |
| Diagnostic recommendations | **Partial** — common pod failures are explained | Translate symptoms into actionable guidance rather than presenting raw Kubernetes errors alone. |

## 12. Scaling, resilience and workload operations

| Feature | Exists | Use case it supports |
| --- | --- | --- |
| Self-service resource and replica changes | **Yes** — within quotas and workload limits | Increase memory or replicas through a supported workflow instead of editing Kubernetes manifests. |
| Resource minimums and namespace quotas | **Yes** | Prevent undersized deployments and stop one application exhausting shared capacity. |
| Health probes and bounded rollout failure handling | **Yes** | Detect unhealthy workloads and stop treating an indefinitely stalled deployment as successful. |
| Scheduled workload execution | **Yes** | Run periodic work with overlap prevention, execution limits and failed-run reporting. |
| Controlled one-off job execution | **No** — no general developer-facing workflow | Run a backfill, repair or administrative task with approved inputs, permissions and audit history. |
| Horizontal and event-driven autoscaling | **No** | Scale according to traffic, CPU or queue depth without manual intervention. |
| Multi-node and availability-zone resilience | **No** — single-node POC | Keep services available during node failure or planned maintenance. |
| Multi-region recovery or failover | **No** | Recover from a regional outage with explicit data-loss and recovery-time expectations. |
| Resilience and disaster-recovery exercises | **Partial** — focused live tests and restore tests | Prove behaviour under dependency failure, node loss and full-environment recovery. |
| Maintenance and upgrade coordination | **Partial** — reviewed chart updates exist | Upgrade cluster and shared services with compatibility checks, communication and predictable disruption. |

## 13. Security, governance and software supply chain

| Feature | Exists | Use case it supports |
| --- | --- | --- |
| Hardened workload defaults | **Yes** | Run non-root with restricted privileges and a read-only root filesystem without configuring every control manually. |
| Deployment policy validation | **Yes** — CI and generated-manifest checks | Reject unsupported or unsafe application configuration before it reaches the cluster. |
| Admission-time policy enforcement | **No** | Prevent unsafe resources even when a caller bypasses the normal Git workflow. |
| Dependency and template update automation | **Yes** | Receive tested update proposals instead of allowing dependencies to age unnoticed. |
| Build provenance and immutable image identity | **Yes** — integrated template build flow | Trace deployed content to a build and preserve the tested image through promotion. |
| Image signatures and deployment-time verification | **No** | Reject images that lack an approved signature or trusted build identity. |
| SBOM and vulnerability-management workflow | **No** — no integrated platform capability found | Identify affected services, assign remediation and track security exceptions. |
| Standard security testing and secret scanning | **No** — no integrated SAST/DAST/scanning offering found | Catch vulnerable code, exposed credentials and application security issues in the delivery workflow. |
| Operational audit trail | **Partial** — Git history and console audits | Establish who changed what and when across deployments, access, secrets and cloud infrastructure. |
| Policy exceptions with approval and expiry | **Partial** — reviewed exceptions with reasons | Allow a justified deviation while ensuring it has an owner and does not become permanent by accident. |
| Application security and compliance scorecards | **No** | Show missing owners, unsupported dependencies, inadequate recovery and other actionable gaps in one place. |

## 14. Cost, sustainability and platform effectiveness

| Feature | Exists | Use case it supports |
| --- | --- | --- |
| Cost allocation by app, team and environment | **No** — ownership labels provide a starting point | Understand who is consuming AKS and Azure resources and what each service costs. |
| Cost estimates before provisioning | **No** | See the expected cost of a database or scaling change before requesting it. |
| Budgets and cost-anomaly alerts | **No** | Detect unexpected spend before the monthly bill arrives. |
| Rightsizing recommendations | **No** — telemetry and manual scaling exist | Identify overallocated or memory-constrained workloads using observed demand. |
| Non-production shutdown and resource expiry | **No** | Stop paying for idle development environments and abandoned cloud resources. |
| Orphaned-resource and unused-service detection | **Partial** — some unused secrets, grants and stale flags are visible | Identify billable resources and services that no longer have an owner or active consumer. |
| Platform adoption and developer-experience metrics | **No** | Measure onboarding time, deployment lead time, failed requests for help and use of supported capabilities. |
| Platform support and feedback workflow | **No** — no integrated offering | Request help, suggest capabilities and understand the status of a platform issue. |

## How to use this with the platform team

The strongest framing is:

> **AKS is the runtime foundation. The platform is the supported path from a developer’s intent to a secure, connected, observable and operable application—including the Azure services it needs.**

For scope discussions, classify each capability as:

1. **Platform-provided** — the team owns the supported experience and its operational contract.
2. **Enterprise-integrated** — the platform connects developers to an existing identity, API management, security, observability or cloud service.
3. **Application-owned** — the platform supplies patterns and integration points, but the application team owns the behaviour.
4. **Deferred** — valuable, but not justified by current demand.

This makes the POC useful evidence that the scope **can be broader**, without implying that one platform team must implement or operate every underlying system.

**POC evidence:** [applications and developer lifecycle](apps.md), [console](console.md), [shared services](services.md), [operations and recovery](operations.md), [architecture](architecture.md), and [remaining plans](plan.md). This is a repository-based capability inventory, not a fresh live-cluster certification.
