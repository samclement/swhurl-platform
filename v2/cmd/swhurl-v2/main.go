// swhurl-v2 is an offline adapter. Only explicitly named files are read.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/appsource"
	"github.com/samclement/swhurl-platform/v2/adapters/copier"
	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/git"
	"github.com/samclement/swhurl-platform/v2/adapters/github"
	"github.com/samclement/swhurl-platform/v2/adapters/helm"
	"github.com/samclement/swhurl-platform/v2/adapters/nats"
	"github.com/samclement/swhurl-platform/v2/adapters/ntfy"
	"github.com/samclement/swhurl-platform/v2/adapters/registry"
	"github.com/samclement/swhurl-platform/v2/adapters/s3"
	"github.com/samclement/swhurl-platform/v2/adapters/sops"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

const usage = `usage:
  swhurl-v2 release-id RELEASE.json
  swhurl-v2 instance-revision INSTANCE.json
  swhurl-v2 select-release --instance INSTANCE.json --release RELEASE.json --expect REVISION
  swhurl-v2 mint --instance INSTANCE.json --profile PROFILE.json --out DIR [--rotate]
  swhurl-v2 render --records DIR --profile PROFILE.json --out DIR [--revision REVISION] [--previous EGRESS.json] \
      [--check]
  swhurl-v2 policy --records DIR --profile PROFILE.json --out ROOT --chart LOCAL_DIR
  swhurl-v2 chart --profile PROFILE.json --repositories HELMREPOSITORIES.yaml --cache DIR
  swhurl-v2 proxy --allocation EGRESS.json --profile PROFILE.json [--out DIR]
  swhurl-v2 deploy --instance INSTANCE.json --release RELEASE.json --app APP.json --profile PROFILE.json \
      --expect REVISION --out DIR [--calls CALLS.json] [--egress EGRESS.json]
  swhurl-v2 deploy --records DIR --profile PROFILE.json --out DIR [--calls CALLS.json] [--egress EGRESS.json]
  swhurl-v2 promote --records DIR --app APPLICATION --env prod --profile PROFILE.json --expect REVISION \
      --out DIR [--calls CALLS.json] [--egress EGRESS.json]
  swhurl-v2 compile --instance INSTANCE.json --release RELEASE.json --app APP.json --profile PROFILE.json [--calls CALLS.json] [--out DIR]
  swhurl-v2 edit --records DIR --app APPLICATION [--env ENVIRONMENT] --set NAME=VALUE --profile PROFILE.json \
      --out DIR [--calls CALLS.json] [--egress EGRESS.json]
  swhurl-v2 create --records DIR --app APPLICATION --team TEAM --image REPO:<run>-<commit> --profile PROFILE.json \
      --out DIR [--exposure GRANT]
  swhurl-v2 change --kind KIND (--app APPLICATION --env ENVIRONMENT | --team TEAM) --requested-by IDENTITY \
      [--repository OWNER/REPO] [--revision COMMIT] [--set NAME=VALUE] [--call APP] [--subscribe APP.EVENT] \
      [--drop-call APP] [--drop-subscription APP.EVENT] [--allow NAME] [--disallow NAME] \
      [--description TEXT] [--rename NEW] [--transport ntfy] [--image REPO:TAG] [--preface TEXT] [--dry-run]
  swhurl-v2 app-repo --app NAME --type TYPE [--stack STACK] [--description TEXT] [--set NAME=VALUE]... \
      --requested-by IDENTITY [--repository OWNER/REPO] [--owner OWNER] [--hook-host HOST] \
      [--contract FILE] [--resume] [--dry-run]
  swhurl-v2 app-hooks --records DIR --owner OWNER --hook-host HOST [--dry-run]
  swhurl-v2 image-declaration [--app-dir DIR]
  swhurl-v2 declaration --label FILE --app APPLICATION --team TEAM [--exposure GRANT]
  swhurl-v2 names --catalogue NAMES.json --app-dir DIR
  swhurl-v2 remove --records DIR --app APPLICATION --env ENVIRONMENT --profile PROFILE.json \
      --calls CALLS.json --out DIR
  swhurl-v2 status --records DIR (--app APPLICATION --env ENVIRONMENT | --all) --profile PROFILE.json --revision COMMIT \
      (--kubeconfig KUBECONFIG.json | --in-cluster) [--calls CALLS.json] [--egress EGRESS.json] [--source NAME] \
      [--max-age DURATION] [--archive [--region REGION]] [--changes FILE [--repo-root DIR]] [--reviewed-digest DIGEST] [--json]
  swhurl-v2 restore --instance INSTANCE.json --release RELEASE.json --profile PROFILE.json \
      --revision COMMIT --kubeconfig KUBECONFIG.json [--calls CALLS.json] [--egress EGRESS.json] \
      [--max-age DURATION] [--from KEY] [--region REGION] \
      [--confirm APPLICATION/ENVIRONMENT [--age-key-file FILE] [--url-ttl DURATION]]
  swhurl-v2 team --records DIR --team TEAM --action ACTION --profile PROFILE.json --out DIR \
      [--description TEXT] [--rename NEW] [--transport ntfy]
  swhurl-v2 workstreams --repo-root DIR
  swhurl-v2 check-repo [--root DIR] [--rendered PATH | --json]
  swhurl-v2 check-secrets [--root DIR] --age-key-file FILE
  swhurl-v2 verify (--kubeconfig KUBECONFIG.json | --in-cluster) [--scope cluster] [--source NAME] [--json]
  swhurl-v2 verify --scope platform --kubeconfig KUBECONFIG.json --root DIR [--console-tag TAG]
      [--heartbeat FILE] [--source NAME] [--json]
  swhurl-v2 notify-serve [--listen ADDR]
  swhurl-v2 notifications-check [--dry-run] (--kubeconfig KUBECONFIG.json | --in-cluster)
  swhurl-v2 notifications-heartbeat [--dry-run] [--max-age DURATION] [--state FILE] --kubeconfig KUBECONFIG.json
  swhurl-v2 clickstack-dashboards (--cluster | --records DIR) [--dry-run] (--kubeconfig KUBECONFIG.json | --in-cluster)
  swhurl-v2 clickstack-bootstrap [--dry-run] --kubeconfig KUBECONFIG.json
  swhurl-v2 flags --records DIR --app APPLICATION --env ENVIRONMENT [--set FLAG=TEXT | --clear FLAG] [--json]
  swhurl-v2 console [--port PORT] (--kubeconfig KUBECONFIG.json | --in-cluster) [--dev]
  swhurl-v2 unit --action reconcile|suspend|resume --unit NAME (--kubeconfig KUBECONFIG.json | --in-cluster)
  swhurl-v2 flux-wait --kubeconfig KUBECONFIG.json

deploy brings one staging instance, or every v2-owned one, to the newest image its repository publishes by the
platform's tag policy, and writes the record pair and the compiled files under --out. With --records it reads
v2-records/<app>/staging under DIR: every folder holding an instance record is one delivery, production follows a
promotion and is not read, an instance that refuses or fails does not stop the others, and the exit status is 1 if
any failed, else 2 if any refused, else 0. The --egress document is given whole to every delivery and an instance
takes it only when the allocation records a grant for it, so a port kept for an instance with nothing to reach
puts nothing behind the proxy.

promote moves one app's production to the release its staging selects, from the tree both keep their records in:
DIR/<app>/staging is the source and DIR/<app>/<env> the target, whose own configuration, grants, calls, storage and
broker identity stay unchanged. It refuses a candidate that is not the release staging selects, a target that is not
production, another application, a user that changes on an instance with a volume, a stale --expect, and everything
compile refuses. Its --egress document is given whole, and production takes it only when the allocation records a
grant for it, exactly as a delivery does, so what a promotion writes equals what the whole-tree render writes. A
release already selected is a no-op that writes nothing. A promotion's direction is not decided from the records — a
release states no build order — so a person reads a rollback from the two releases it states.

compile reads the instance, its release, the app record (one per app: its team) and the platform profile;
the app record is required for delivery, because the namespace team label is the app's fact. Input files are
never overwritten. Without --out the compilation document goes to stdout and nothing
is written. With --out every compiled file is written under DIR, one "written PATH" or
"unchanged PATH" line each; DIR must not be inside apps/, and nothing outside it is touched. An instance that
signs in to the event service needs its seed with --events-secret: v2 copies that encrypted file beside the
manifests, which list it, and never reads or decrypts it.

status compares the cluster with what one instance's records say should run. It compiles the record pair under
DIR/APPLICATION/ENVIRONMENT, reads the unit, HelmRelease, Deployment, pods, route and certificates the compiled
documents name from the API server the kubeconfig names (the JSON form that "kubectl config view --raw -o json"
prints), and prints the instance's state (suspended, failing, updating, stopped, unknown or healthy) with the fact
that decided it, every finding, the route and its certificates and each container that is not ready and why.
--in-cluster reads instead with the pod's own ServiceAccount: the token, ca.crt and namespace under
/var/run/secrets/kubernetes.io/serviceaccount/ and KUBERNETES_SERVICE_HOST and KUBERNETES_SERVICE_PORT. Exactly one
of --kubeconfig and --in-cluster is required, and the report's source is the context or "serviceaccount in
NAMESPACE" unless --source names it.
--revision is the commit the records were read at, which Flux should have applied, and the expected image is the
record's release. It reads only. A database instance's status also carries its protection: the backup CronJob and
the runs its labels select, judged against --max-age (26h by default). With --archive it also lists the instance's
own prefix in the object store, with the operator's AWS credentials from the environment, and says whether an
archive exists and how old the newest one is; the region is discovered from the bucket unless --region states it.
--json prints the v2-observation document (version 5) instead of the text. An application and environment with no
record under DIR is refused. --all is exclusive with --app/--env and returns a v2-observations collection (version 1)
in stable application/environment order. Core lists are batched; each database adds at most one newest-owner Job GET.
--changes FILE supplies strict v2-changes version 1 comparisons with exact from/to SHAs, status (complete,
incomplete or unavailable), and paths (including previous rename paths). Only a complete matching comparison with
no relevant changed path can exempt the revision check, never health or digest checks. --repo-root DIR (default .)
identifies the extracted repository root for repository-relative relevance; it requires --changes. Records, profile,
calls and egress must be inside that root. Shared profile/resolution/allocation changes conservatively require applying.
Every report includes promotion:{ready,reason}, evaluated for the selected digest by default. Single status accepts
--reviewed-digest DIGEST to evaluate the submitted review instead (including an empty/invalid digest, which refuses
readiness). It is forbidden with --all. This is a submission-time observation, not a later manual-merge guarantee.

verify runs the platform checks. --scope cluster, the default and what the console runs, is the six that only read
the cluster — every Flux unit Ready, Traefik's HTTPS redirect, the Flux alerts and their providers, the notify service
on the console's image, and the console's dashboard and notification CronJobs succeeding within five minutes — with
the credential status takes. --scope platform is all of them: those six, then the checks that compare the cluster with
the checkout at --root (its v2 records, compiled tree, profile and egress allocation), the registry and the object
store — each app's ImagePolicy and staging image, app isolation, the token issuer and signing keys, NATS and the
declared streams and consumers, the egress proxy and its ports, the notification routes and channel Secrets, the
MongoDB and SQLite backups, and the console's image against --console-tag (the checkout's content tag,
images/content-tag.sh console) and its job record. It also checks GitHub hooks and console tokens, the collector's
runtime ingestion Secret and its bytes against the ClickStack team key, closed registration, ClickHouse TTLs and
failed merges, and MongoDB volume retention. Pod queries use fixed read-only commands over API-server WebSocket v5,
with credentials only on stdin and no subprocess. It needs --kubeconfig (--in-cluster is a usage error); registry
reads are anonymous; the object store is read with AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY and AWS_SESSION_TOKEN from
the environment, and a backup is too old after BACKUP_MAX_AGE_HOURS (default 26). An input it needs but was not given
(the credentials, the tag) is a bad entry saying it was not supplied. It prints each check's section and [OK], [WARN]
or [BAD] lines, or with --json the v2-platform-checks document (version 1). A failed read is a bad entry, never
absence. The exit status is 1 when an entry is bad, after the report is printed, and 2 for a usage error, an unusable
credential or a request an adapter refuses. The platform scope also judges the host: the Flux controllers against
clusters/home/flux-system/install/flux.json in the checkout, and the notification heartbeat timer and service from
--heartbeat FILE, the output of systemctl show --timestamp=unix for both units, which make verify-platform writes.
The cluster scope reads neither, and refuses both flags.

notifications-heartbeat watches the notification checker from the host: it reads the console-notifications CronJob
and alerts the platform topic when it is missing, suspended or has not succeeded within --max-age (10m by default; a
number and s, m or h), reminds hourly while it stays stale and says when it recovers. The incident is kept in --state
(default ~/.local/state/swhurl-platform/notification-heartbeat.json, the file the Python heartbeat kept), written
atomically and privately only after ntfy accepts a notice; the topic's address is read from the console's
notification-ntfy Secret only when a notice is due. A CronJob that cannot be read is unknown, not stale: nothing is
sent. --dry-run reads and decides, and neither posts nor saves. Each job is a JSON log line; the exit status is 1
when the state cannot be read or is invalid (it is left unchanged) or any job failed.

clickstack-dashboards keeps one ClickStack dashboard per app, the app's default alerts and the two reserved
webhooks. The apps are those whose HelmReleases Flux applied (--cluster; none found deletes nothing) or those the
v2 records tree names (--records; no app or a bad record is a failure). It repairs both webhooks, checked before
either is written, then creates, updates and deletes dashboards tagged swhurl-app and named App: <app>, records
each app's dashboard id in console/app-dashboards, and keeps each app's alerts. It reads its credentials by name
each pass and reaches HyperDX and MongoDB by API-server exec with credentials only on stdin. --dry-run reads and
plans, and writes nothing. Each outcome is a JSON log line; the exit status is 1 when the pass stopped.

clickstack-bootstrap makes HyperDX's first account and its team's ingestion key the ones in
observability/clickstack-runtime-inputs, which HyperDX has no setting for. It waits up to five minutes for the API,
registers CLICKSTACK_ADMIN_EMAIL with CLICKSTACK_ADMIN_PASSWORD when no team exists, then sets the one team's key to
CLICKSTACK_INGESTION_KEY when it differs; run again it changes nothing. Credentials travel only in the programs it
runs by API-server exec, on stdin. --dry-run reads and decides, and writes nothing. It prints [OK], [INFO] and
[WARN] lines, and [BAD] with exit status 1 where it stopped.

flux-wait waits up to twenty minutes for every Flux unit in flux-system to report Ready at the revision the
swhurl-platform GitRepository fetched, following the source to a newer revision if a commit lands meanwhile. It
prints [OK] for each unit as it gets there and [WARN] for a suspended one, which it skips, and stops with [BAD] and
exit status 1 at the first unit that fails at that revision, with the unit's own message, or at the timeout. A
failed read is an error and exit status 1. It only reads.

policy is read-only: it validates the complete records tree and team/channel availability under --out,
then templates the explicit local --chart for every compiled instance, production included. Chart metadata
must match the profile and compiled release. It reports sorted accumulated findings and separate redacted
render failures, never environment/Secret values. Any instance failing makes the command nonzero; invalid
inputs print no partial report. No chart acquisition, cluster access or file writes occur.

chart resolves the profile's chart name/version and named HelmRepository from the explicit YAML stream,
then returns its absolute local directory under --cache. A missing chart is pulled with Helm into a private
temporary directory, checked against the pin and published by rename. Cached and concurrently published
charts must have matching Chart.yaml metadata. Helm's repository config/index cache are isolated inside
the private pull directory, so only --cache is written. No cluster access or installation occurs. Resolution
or acquisition failures exit 1 with fixed diagnostics, not Helm output. The renderer still accepts only an explicit local chart.

egress is render's allocation: one proxy port per instance, keeping every port the previous allocation gave and
granting each database instance the profile's backup host, so its nightly upload passes the proxy. compile refuses a
database instance without --egress: its backup job may reach only the proxy, so it could not upload. A database
instance also renders its backup from the profile's backup facts.

proxy renders the platform's egress proxy from one allocation: its configuration, with a port per
instance and the hosts each instance's grants allow, and a policy admitting each port from that
instance's namespace only. Grants are enforced, so an instance reaches what it declares and nothing
else. The proxy's deployment stays the platform's hand-written HelmRelease, which reads what proxy
writes; --out writes the rendered files under DIR and nothing else.

mint gives one v2-owned instance a broker identity: it draws a seed, derives the nkey pair, encrypts the
Secret that holds the seed to the profile's SOPS recipient with sops, writes that file beside the instance's
record, and then writes the record that states the public key. The seed is never printed. It refuses an
instance that already has a key unless --rotate is given, and --rotate without a key. The encrypted file goes
first, so a failure leaves a record that states no key rather than a key with no seed; compile still refuses
that folder until the file and the record agree. sops must be on PATH and no private key is read.

render renders every file v2 derives from its records in one run: the resolution at DIR/calls.json, every instance's
compiled files and unit, the egress allocation (keeping the ports --previous gives, else the ones the allocation under
--out holds) with the proxy's configuration and policy, the auth folder, NATS's user list, Reloader's watch list, and
each unit's line in clusters/home/kustomization.yaml (added after the entries there; the rest of that file is kept).
Every DIR/<app>/<env> folder holding an instance record is read, production as well as staging, with its seed copied as
bytes and its offers folder. Everything is decided before anything is written, so a record the compiler or a decoder
refuses writes nothing. A write needs --revision, the commit the records are read at, which the resolution and the
allocation record. It never touches the cluster, and DIR must be under --out. --check writes nothing at all: it compares
every file it renders with the one under --out, names each that differs, and exits 1 if any do; the revision the
resolution and the allocation record is provenance, not content, so it is not compared.

edit applies one named change to one v2-owned instance's record, compiles the edited record, and writes the
record and the files it renders under --out in one effect: the settings are replicas, memoryMiB, exposure,
calls, allows, subscriptions and egress, each the whole new value (an empty list clears it), and team, which is the
app record's fact and so names no environment and re-renders every one of the app's. A change the record or its
release does not permit is refused by name, and so is one that leaves the record as it was. alerts is a named
setting v2 does not model yet: it is refused rather than accepted as another fact. A change to calls,
allows or subscriptions writes the record the resolution predates: the report says so (resolutionStale) and the
operator regenerates v2-records/calls.json from the edited tree. A change to egress writes the record the
allocation predates: the proxy's port and configuration are the whole tree's render, so the report says so
(allocationStale) and the operator renders the tree before committing.

create states a new app's first instance from the image its repository published: the image resolves through the
registry, states its own declaration and the commit it was built from, and its records are written under --records.
The instance is staging's, with one replica, the memory the image says it runs in, a volume the image states when it
keeps a database, the exposure --exposure names (private by default: that grant is what edit changes), what the image
says about itself and the documents it offers, and a freshly minted broker identity whose encrypted seed is written
before the record that states its public key. The documents are written into the instance's offers folder under their
roles' fixed names, byte for byte: no index file names them. It writes records only: the compiled files, the unit and
the instance's proxy port and the unit's line in clusters/home/kustomization.yaml are the whole tree's, so the next
render decides them. It refuses an app that already has records.

change opens the pull request one kind of console change asks for, in the one process: it resolves main, downloads the
tarball at that commit, applies the kind's commands to a scratch copy of it, keeps only the files the kind derives,
and writes the blobs, tree, commit, branch and pull request through GitHub's API, the pull request's words being the
console's character for character. A kind states the environments its branch may name and the files it may carry,
so a pull request can carry nothing the console did not ask for. A connection, an allow or a new app's settings are
asked for as additions and removals (--call, --subscribe, --allow, ...) and merged into the record at the commit
being changed, never stated as a whole list; a new app's new kind states its image with --image, the settings it
starts with with --set (team and exposure included) and what its pull request says first with --preface. The token
comes from CONSOLE_TOKEN and the repository from CONSOLE_REPO or --repository, neither ever on the command line; every
line of progress goes to standard output and the pull request's URL is the last one, so the console records the job's
log and takes its link from that line. --dry-run applies and reports the change and writes nothing to GitHub.

app-repo creates a new app's public GitHub repository: it reads the stack's copier.yml for the questions its type
answers, renders the pinned template with one Copier run into a folder of its own, creates the repository with the
rendered tree as its first commit, adds the image webhook, waits for the app's own first image (run 1 or later; the
template's seed, 0-<commit>, is a copied build, so it is not a release v2 creates from) and then opens the ordinary
'new' pull request that creates its staging instance. --resume finds the repository an earlier job created instead of
creating one. The tokens are the environment's: CONSOLE_TOKEN for the pull request, APP_REPOS_TOKEN for the
repository, IMAGE_WEBHOOK_TOKEN for the webhook (a failure there is a warning), and CONSOLE_REPO or --repository names
the platform repository.

app-hooks keeps the image webhook on the repository of every app whose staging release under --records publishes
the app's own image, ghcr.io/OWNER/APP: GitHub then calls Flux's app-images Receiver on --hook-host when the
repository publishes one. A repository with no webhook to that host gets one, one that posts elsewhere (the token
was rotated), is disabled or takes other events is changed, and a correct one is left alone; none is deleted. The
tokens are the environment's: GH_TOKEN lists and writes the webhooks and IMAGE_WEBHOOK_TOKEN signs them. An app
whose record cannot be read or whose repository GitHub refuses is an [ERROR] line and the others still run; the exit
status is then 1. A records tree or record that cannot be read stops before any call. --dry-run lists the webhooks
and says what would change, and writes nothing.

declaration decodes the declaration label an image build printed, applies the creation rules an instance of it would
face, and reports what the image states. It reads no image, no registry and no cluster, and writes nothing: the stack
templates' check uses it to prove a rendered app declares what the platform can create.

names searches every file under --app-dir for the platform's names, SWHURL_* and platform.swhurl.com/*, and prints
each one the --catalogue (v2/contract/names.json) does not list as "NAME (PATH)", with the first file using it, sorted
by name. A family entry such as SWHURL_SERVICE_<APP>_URL lists its members and its prefix alone. It reads only, inside
DIR, and exits 1 when it printed a name: the stack templates' check runs it over each app type they render.

remove deletes one v2-owned instance: its record pair with its seed and the documents it publishes, the files
compiled from them, its unit, the namespace's auth object, and the app record when this was the app's last
environment. It writes the two kustomizations without the entries that named them in the same effect, so a commit
never lists a file it deleted. It refuses an instance the resolution still connects as a caller or a subscriber,
and it reports the platform documents that still name the instance instead of writing them: each is rendered from
a pinned revision of the whole tree, so they are regenerated after the removal is committed.

restore writes a v2-restore plan for one instance: the archive it would be restored from (the newest, or
--from KEY), what the bucket records beside it (both checksums, table count, source instance), the
instance's own claim, path and user, the observation it was decided from, and the make target that performs
the restore. It reads only, and restores nothing. Its state is ready, stale (older than --max-age),
incomplete (no metadata, or metadata that does not verify), no_archive or unavailable. With --confirm
APPLICATION/ENVIRONMENT it performs the restore instead: v2 suspends the release, stops the app, runs the
backup image's restore job with a presigned read of that one archive and the age identity from
--age-key-file (default ./age.agekey), then scales the app back and resumes the release. The document then
carries what each step did, the job's log as evidence, and the app's own state; the read URL is valid for
--url-ttl (default 1h) and travels in a Secret this command creates and deletes.

team changes the team registry, DIR/teams.json, and writes it with what is rendered from it: the notification
routes (platform/notify/routes.json) and the kustomization listing each channel's Secret. The actions are add and
describe (with --description, one line; describe without it clears it), rename (--rename), remove, channel and
drop-channel. Renaming or removing a team is refused while an app record names it or an instance allows it: edit
moves them. A team is removed only once its channel has gone. channel gives the team its notification channel,
named after the team, or a new address for the one it has: the address is the first line of standard input, never
an argument, and is encrypted with sops to the profile's recipient into platform/notify/channels/channel-NAME.sops.yaml
before anything else is written; it is never printed. drop-channel removes the channel and deletes its Secret once
nothing lists it. A change that leaves the registry as it was writes nothing new.

workstreams compares the workstream registry, DIR/docs/workstreams.md, and its progress pages with the checkout at
DIR, which must be the top of a Git working tree: each row's shape and page, one workstream in progress, each status
page's block against its row and against git status, the commits it cites against what a ref reaches, a change
under a workstream's paths that also changes its page, and each progress page's shape and size. Git is only read,
never written (not even the index), and only the Markdown files the check uses are opened, inside DIR. It prints one
line per check and exits 1 when any problem is found or a read fails, and 2 when DIR is not the top of a checkout.

check-repo decides what make check-repo checks in the checkout at --root (default the working directory), which must
be the top of a Git working tree: every relative Markdown link names a file the checkout holds and, for Markdown, a
heading it has; docs/architecture.md's Flux units table names exactly the shared units Git defines and their inputs;
docs/apps.md's app interface table is the names catalogue (v2/contract/names.json); v2's compiled tree gives apps only
listed names; every Secret under clusters, infra, platform and apps is SOPS age ciphertext in a .sops.yaml file and
every unit that decrypts holds one; platform-settings is strings and sets BASE_DOMAIN and CERT_ISSUER; and every folder
Flux renders is local, holds a kustomization and substitutes from a source this check maps. It prints one line per
check, then the active render paths, or with --json the v2-check-repo document (version 1) with each path's values.
Every check runs even when an earlier one failed, and a finding exits 1; a checkout that cannot be opened or read
exits 2. It renders nothing and starts no process: v2/check-repo.sh renders each path with Kustomize and passes the
result to --rendered PATH, which reads it on stdin, applies that path's substitution and prints what Flux applies; an
unresolved ${...} or a document without apiVersion, kind or metadata is a [BAD] line on stderr and exit 1. Git is
only read, and only files inside DIR are opened.

check-secrets decrypts, in memory, every *.sops.yaml and *.sops.yml file the checkout at --root (default the working
directory) tracks, apart from .sops.yaml, with the age identity in --age-key-file, which only sops reads. It prints one
line per Secret document and an [ERROR] for a file that does not decrypt or parse, a document that is not a Secret, an
empty value, a REPLACE_ME value outside tests/fixtures and a data value that is not base64, and a [WARN] for a data
value that is probably base64-encoded twice. It then compares, by SHA-256, the copies the platform keeps of the
ClickStack ingestion key, the image webhook token, the GitHub dispatch token and the ntfy platform destination. No
value or fingerprint is printed. An error exits 1; a missing key file or a checkout that cannot be read exits 2.

Exit 0: success/no-op (for status: a report was printed, whatever its state; for restore --confirm: the
data was restored and the app is back); 2: usage/validation/refusal, nothing changed; 1: a restore that
started but did not complete, a render --check that found a difference, a name the catalogue does not list, a
policy render/rule failure, a workstream record that disagrees with its checkout, a check-repo finding, a check-secrets error, chart dependency
resolution/acquisition failure, or file and output I/O failure.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, readInput, files.New()))
}

// maxInput bounds what is read from a named file; a descriptor is a few hundred bytes.
const maxInput = 1 << 20

var errTooLarge = errors.New("input file is larger than 1 MiB")

func readInput(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxInput+1))
	if err == nil && len(data) > maxInput {
		return nil, errTooLarge
	}
	return data, err
}

func run(args []string, out, diagnostic io.Writer, readFile func(string) ([]byte, error), writer files.Writer) int {
	return runWith(args, out, diagnostic, cli{readFile: readFile, writer: writer, archives: archiveReader,
		registries: registryReader, seed: randomSeed, encrypter: sops.New(), stdin: os.Stdin,
		inCluster: podServiceAccount()})
}

// runWith runs one command with the cli a test assembled: the adapters a command needs supplied, everything else the
// binary's. It is the one dispatcher every test entry point goes through, so a test cannot exercise a different
// argument parser or exit status than the binary does.
func runWith(args []string, out, diagnostic io.Writer, c cli) int {
	r, outcome := parse(args)
	switch outcome {
	case help:
		return emit(out, diagnostic, []byte(usage))
	case misuse:
		fmt.Fprint(diagnostic, usage)
		return 2
	}
	c.out, c.diagnostic = out, diagnostic
	return commands()[r.command](c, r)
}

// runWithArchives runs one command with a supplied object-store reader, so a test can answer a listing
// without a credential or a network.
func runWithArchives(args []string, out, diagnostic io.Writer, readFile func(string) ([]byte, error),
	writer files.Writer, archives func(bucket, region string) (s3.Reader, error)) int {
	return runWith(args, out, diagnostic, cli{readFile: readFile, writer: writer, archives: archives,
		registries: registryReader, seed: randomSeed, encrypter: sops.New(), inCluster: podServiceAccount()})
}

// runWithEncrypter runs one command with a supplied encrypter and seed, so a mint's test needs neither sops nor
// randomness. Everything else is what the binary does.
func runWithEncrypter(args []string, out, diagnostic io.Writer, readFile func(string) ([]byte, error),
	writer files.Writer, encrypter sops.Encrypter, seed func() ([32]byte, error)) int {
	return runWith(args, out, diagnostic, cli{readFile: readFile, writer: writer, archives: archiveReader,
		registries: registryReader, seed: seed, encrypter: encrypter, inCluster: podServiceAccount()})
}

// runWithRegistry runs one command with a supplied image-registry reader, so a test can answer a tag and a
// declaration without a network.
func runWithRegistry(args []string, out, diagnostic io.Writer, readFile func(string) ([]byte, error),
	writer files.Writer, registries func(image string) (registry.Reader, error)) int {
	return runWith(args, out, diagnostic, cli{readFile: readFile, writer: writer, archives: archiveReader,
		registries: registries, seed: randomSeed, encrypter: sops.New(), inCluster: podServiceAccount()})
}

// registryReader is the image-registry reader the command builds: public packages, anonymous, over the
// registry the artifact image names.
func registryReader(image string) (registry.Reader, error) {
	return registry.New(image, &http.Client{Timeout: 30 * time.Second})
}

// defaultNATSURL is where the platform's broker answers inside the cluster, which the console's own environment names
// on every request; a local run port-forwards and states its own.
const defaultNATSURL = "nats://nats.messaging.svc.cluster.local:4222"

// archiveReader is the object-store reader the command builds from the operator's own credentials. They are
// read from the environment and never written down, and the region is the bucket's own when none is given.
func archiveReader(bucket, region string) (s3.Reader, error) {
	credentials, supplied := awsCredentials()
	if !supplied {
		return nil, errors.New("invalid: credentials: aws_credentials_are_not_set")
	}
	return s3.New(region, credentials, &http.Client{Timeout: 30 * time.Second}), nil
}

// awsCredentials are the operator's object-store credentials, from the environment; false when they are not set.
func awsCredentials() (s3.Credentials, bool) {
	credentials := s3.Credentials{AccessKey: os.Getenv("AWS_ACCESS_KEY_ID"),
		SecretKey: os.Getenv("AWS_SECRET_ACCESS_KEY"), SessionToken: os.Getenv("AWS_SESSION_TOKEN")}
	return credentials, credentials.AccessKey != "" && credentials.SecretKey != ""
}

// commands is every command and the step that runs it, in one table so the dispatcher and the usage check cannot
// disagree. It is a function rather than a variable because a change runs the commands it names: a table a command's
// own body refers to would otherwise depend on itself.
func commands() map[string]func(cli, request) int {
	return map[string]func(cli, request) int{
		"release-id":              func(c cli, r request) int { return c.releaseID(r.release) },
		"instance-revision":       func(c cli, r request) int { return c.instanceRevision(r.instance) },
		"select-release":          cli.selectRelease,
		"deploy":                  cli.deploy,
		"promote":                 cli.promote,
		"compile":                 cli.compile,
		"status":                  cli.status,
		"render":                  cli.render,
		"policy":                  cli.policy,
		"chart":                   cli.acquireChart,
		"proxy":                   cli.proxy,
		"mint":                    cli.mint,
		"create":                  cli.create,
		"declaration":             cli.declaration,
		"names":                   cli.names,
		"image-declaration":       cli.imageDeclaration,
		"edit":                    cli.edit,
		"remove":                  cli.remove,
		"restore":                 cli.restore,
		"team":                    cli.team,
		"workstreams":             cli.workstreams,
		"check-repo":              cli.checkRepo,
		"check-secrets":           cli.checkSecrets,
		"verify":                  cli.verify,
		"notify-serve":            cli.notifyServe,
		"notifications-check":     cli.notificationsCheck,
		"notifications-heartbeat": cli.notificationsHeartbeat,
		"clickstack-dashboards":   cli.clickstackDashboards,
		"clickstack-bootstrap":    cli.clickstackBootstrap,
		"flags":                   cli.flags,
		"console":                 cli.console,
		"change":                  cli.change,
		"app-repo":                cli.appRepo,
		"app-hooks":               cli.appHooks,
		"unit":                    cli.unit,
		"flux-wait":               cli.fluxWait,
	}
}

// request is one parsed command line: the command, the files, revision, credentials and source it
// names, and where to write.
type request struct {
	reviewedDigestSet                                  bool
	command, instance, release, expected, profile, out string
	revision, kubeconfig, source                       string
	scope, root, consoleTag, heartbeat                 string
	region                                             string
	from                                               string
	archive                                            bool
	app, callFile                                      string
	unit                                               string
	env, maxAge                                        string
	previous, egressFile, allocation                   string
	records, changes, repoRoot, reviewedDigest         string
	chart, repositories, cache                         string
	eventsSecret, offers, change, label                string
	team, image, exposure                              string
	appType, stack, owner, hookHost, contractPath      string
	resume                                             bool
	// spec is a change's own flags; no other command reads it.
	spec                                        *changeSpec
	action, description, rename, transport      string
	listen, appDir, catalogue, rendered         string
	statePath, port                             string
	stateFlag, maxAgeFlag                       *once
	rotate, check, json, inCluster, all, dryRun bool
	cluster, dev                                bool
	confirm, ageKey, cleared                    string
	urlTTL                                      time.Duration
	urlTTLSet                                   bool
	outFlag                                     *once
	descriptionFlag                             *once
}

type outcome int

const (
	parsed outcome = iota
	help
	misuse
)

func parse(args []string) (request, outcome) {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		return request{}, help
	}
	if len(args) == 0 {
		return request{}, misuse
	}
	if _, known := commands()[args[0]]; !known {
		return request{}, misuse
	}
	r := request{command: args[0]}
	flags, err := r.parseFlags(args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return r, help
	}
	if err != nil || !r.take(flags) {
		return r, misuse
	}
	return r, parsed
}

// parseFlags parses the flags a command takes into r; the other commands take none.
func (r *request) parseFlags(args []string) (*flag.FlagSet, error) {
	flags := flag.NewFlagSet(r.command, flag.ContinueOnError)
	flags.SetOutput(io.Discard) // flag errors can echo rejected values
	r.registerInputs(flags)
	r.registerExtras(flags, r.command)
	switch r.command {
	case "app-repo":
		r.registerAppRepoFlags(flags)
	case "app-hooks":
		r.registerAppHooksFlags(flags)
	}
	err := flags.Parse(args)
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "reviewed-digest" {
			r.reviewedDigestSet = true
		}
	})
	return flags, err
}

// registerInputs adds the file flags the v1-style commands share.
func (r *request) registerInputs(flags *flag.FlagSet) {
	switch r.command {
	case "select-release", "deploy", "compile", "restore", "mint":
		flags.Var(&once{value: &r.instance}, "instance", "instance file")
		flags.Var(&once{value: &r.release}, "release", "release file")
	}
}

// registerExtras adds what each command alone takes: its remaining inputs, and where a command writes.
func (r *request) registerExtras(flags *flag.FlagSet, command string) {
	if command == "team" {
		r.registerTeamFlags(flags)
		return
	}
	if command == "chart" {
		flags.Var(&once{value: &r.profile}, "profile", "platform profile")
		flags.Var(&once{value: &r.repositories}, "repositories", "HelmRepository YAML stream")
		flags.Var(&once{value: &r.cache}, "cache", "local chart cache")
		return
	}
	if command == "workstreams" {
		flags.Var(&once{value: &r.repoRoot}, "repo-root", "the top of the checkout whose workstream record is checked")
		return
	}
	if command == "flags" {
		flags.Var(&once{value: &r.records}, "records", "the tree every v2-owned instance keeps its records in")
		flags.Var(&once{value: &r.app}, "app", "application")
		flags.Var(&once{value: &r.env}, "env", "environment: staging or prod")
		flags.Var(&once{value: &r.change}, "set", "one flag's new value as the form's text, FLAG=TEXT")
		flags.Var(&once{value: &r.cleared}, "clear", "one flag whose override is removed, declared or not")
		flags.Var(&onceBool{value: &r.json}, "json", "print the v2-flags document instead of the rows")
		return
	}
	if r.registerCheckFlags(flags, command) {
		return
	}
	if command == "console" {
		r.port = "8080"
		flags.Var(&once{value: &r.port}, "port", "the port to serve on (default 8080)")
		flags.Var(&onceBool{value: &r.dev}, "dev", "serve a fixed identity on loopback only, reading this checkout")
		flags.Var(&once{value: &r.kubeconfig}, "kubeconfig", "kubeconfig file, in JSON")
		flags.Var(&onceBool{value: &r.inCluster}, "in-cluster", "use this pod's ServiceAccount instead of a kubeconfig")
		return
	}
	if r.registerFluxFlags(flags, command) {
		return
	}
	if command == "notify-serve" {
		r.listen = defaultListen
		flags.Var(&once{value: &r.listen}, "listen", "the address to serve on (default "+defaultListen+")")
		return
	}
	if r.registerRecordFlags(flags, command) {
		return
	}
	r.registerBridgeFlags(flags, command)
}

// registerCheckFlags adds the flags of the two checks that read the checkout at --root, and reports whether it
// handled the command.
func (r *request) registerCheckFlags(flags *flag.FlagSet, command string) bool {
	switch command {
	case "check-repo":
		r.root = "."
		flags.Var(&once{value: &r.root}, "root", "the top of the checkout to check (default: the working directory)")
		flags.Var(&once{value: &r.rendered}, "rendered", "an active render path whose rendered manifests are on stdin")
		flags.Var(&onceBool{value: &r.json}, "json", "print the v2-check-repo document instead of the text")
	case "check-secrets":
		r.root = "."
		flags.Var(&once{value: &r.root}, "root", "the top of the checkout whose Secrets are checked (default: the working directory)")
		flags.Var(&once{value: &r.ageKey}, "age-key-file", "the age identity sops decrypts with")
	default:
		return false
	}
	return true
}

// registerRecordFlags adds the flags of the commands that work from one instance's records, and reports
// whether it handled the command.
func (r *request) registerRecordFlags(flags *flag.FlagSet, command string) bool {
	if r.registerChangeFlags(flags, command) {
		return true
	}
	if r.registerDeliveryFlags(flags, command) {
		return true
	}
	return r.registerInstanceFlags(flags, command)
}

// registerChangeFlags is the flags of the commands that change or compile one instance's records.
func (r *request) registerChangeFlags(flags *flag.FlagSet, command string) bool {
	switch command {
	case "change":
		r.registerChangeCommand(flags)
	case "edit":
		flags.Var(&once{value: &r.records}, "records", "the tree every v2-owned instance keeps its records in")
		flags.Var(&once{value: &r.app}, "app", "application")
		flags.Var(&once{value: &r.env}, "env", "environment (omit for an app-level change)")
		flags.Var(&once{value: &r.change}, "set", "one named change, name=value")
		flags.Var(&once{value: &r.profile}, "profile", "platform profile file")
		flags.Var(&once{value: &r.callFile}, "calls", "resolved connections document (optional)")
		flags.Var(&once{value: &r.egressFile}, "egress", "egress allocation document (optional)")
		r.registerOut(flags)
	case "create":
		r.registerCreateFlags(flags)
		r.registerOut(flags)
	case "image-declaration":
		r.appDir = "."
		flags.Var(&once{value: &r.appDir}, "app-dir", "app checkout (default: working directory)")
	case "names":
		flags.Var(&once{value: &r.catalogue}, "catalogue", "the platform's names catalogue")
		flags.Var(&once{value: &r.appDir}, "app-dir", "the rendered app whose files are searched")
	case "declaration":
		flags.Var(&once{value: &r.label}, "label", "a file holding the declaration label an image build printed")
		flags.Var(&once{value: &r.app}, "app", "the application the label would create")
		flags.Var(&once{value: &r.team}, "team", "the owning team")
		flags.Var(&once{value: &r.exposure}, "exposure", "private (default), or authenticated-web")
	case "remove":
		flags.Var(&once{value: &r.records}, "records", "the tree every v2-owned instance keeps its records in")
		flags.Var(&once{value: &r.app}, "app", "application")
		flags.Var(&once{value: &r.env}, "env", "environment")
		flags.Var(&once{value: &r.profile}, "profile", "platform profile file")
		flags.Var(&once{value: &r.callFile}, "calls", "resolved connections document")
		r.registerOut(flags)
	case "compile":
		r.registerRecords(flags)
		flags.Var(&once{value: &r.app}, "app", "app record (its team)")
		flags.Var(&once{value: &r.callFile}, "calls", "resolved connections document (optional)")
		flags.Var(&once{value: &r.egressFile}, "egress", "egress allocation document (optional)")
		flags.Var(&once{value: &r.eventsSecret}, "events-secret", "the instance's encrypted events Secret (optional)")
		flags.Var(&once{value: &r.offers}, "offers", "the folder holding the documents the instance publishes (optional)")
		r.registerOut(flags)
	default:
		return false
	}
	return true
}

// registerFluxFlags is the flags of the two Flux commands: one unit's action, and the wait for every unit.
func (r *request) registerFluxFlags(flags *flag.FlagSet, command string) bool {
	switch command {
	case "unit":
		flags.Var(&once{value: &r.action}, "action", "reconcile, suspend or resume")
		flags.Var(&once{value: &r.unit}, "unit", "the Flux unit, in the flux-system namespace")
		flags.Var(&once{value: &r.kubeconfig}, "kubeconfig", "kubeconfig file, in JSON")
		flags.Var(&onceBool{value: &r.inCluster}, "in-cluster", "use this pod's ServiceAccount instead of a kubeconfig")
	case "flux-wait":
		flags.Var(&once{value: &r.kubeconfig}, "kubeconfig", "kubeconfig file, in JSON")
	default:
		return false
	}
	return true
}

// registerDeliveryFlags is the flags of the commands that deliver a release: one instance, every staging instance, or
// a promotion into production.
func (r *request) registerDeliveryFlags(flags *flag.FlagSet, command string) bool {
	switch command {
	case "select-release":
		flags.Var(&once{value: &r.expected}, "expect", "reviewed instance revision")
	case "deploy":
		flags.Var(&once{value: &r.records}, "records", "every v2-owned staging instance under this tree")
		r.registerRecords(flags)
		flags.Var(&once{value: &r.app}, "app", "app record (its team)")
		flags.Var(&once{value: &r.expected}, "expect", "reviewed instance revision")
		flags.Var(&once{value: &r.callFile}, "calls", "resolved connections document (optional)")
		flags.Var(&once{value: &r.egressFile}, "egress", "egress allocation document (optional)")
		r.registerOut(flags)
	case "promote":
		flags.Var(&once{value: &r.records}, "records", "the tree both instances keep their records in")
		flags.Var(&once{value: &r.app}, "app", "application")
		flags.Var(&once{value: &r.env}, "env", "the environment promoted into (production)")
		flags.Var(&once{value: &r.profile}, "profile", "platform profile file")
		flags.Var(&once{value: &r.expected}, "expect", "the production instance revision the caller reviewed")
		flags.Var(&once{value: &r.callFile}, "calls", "resolved connections document (optional)")
		flags.Var(&once{value: &r.egressFile}, "egress", "egress allocation document (optional)")
		r.registerOut(flags)
	default:
		return false
	}
	return true
}

// registerInstanceFlags is the flags of the live commands and of minting a broker identity.
func (r *request) registerInstanceFlags(flags *flag.FlagSet, command string) bool {
	switch command {
	case "notifications-check":
		flags.Var(&once{value: &r.kubeconfig}, "kubeconfig", "kubeconfig file, in JSON")
		flags.Var(&onceBool{value: &r.inCluster}, "in-cluster", "use this pod ServiceAccount")
		flags.Var(&onceBool{value: &r.dryRun}, "dry-run", "read and evaluate without posting or saving")
	case "notifications-heartbeat":
		r.stateFlag = &once{value: &r.statePath}
		flags.Var(&once{value: &r.kubeconfig}, "kubeconfig", "kubeconfig file, in JSON")
		flags.Var(&onceBool{value: &r.dryRun}, "dry-run", "read and decide without posting or saving state")
		r.maxAgeFlag = &once{value: &r.maxAge}
		flags.Var(r.maxAgeFlag, "max-age", "the age before a watched job is stale, for every job (default 10m; s, m or h)")
		flags.Var(r.stateFlag, "state", "the state file (default ~/"+heartbeatStateFile+")")
	case "clickstack-bootstrap":
		flags.Var(&once{value: &r.kubeconfig}, "kubeconfig", "kubeconfig file, in JSON")
		flags.Var(&onceBool{value: &r.dryRun}, "dry-run", "read and decide without registering or setting the key")
	case "clickstack-dashboards":
		flags.Var(&once{value: &r.kubeconfig}, "kubeconfig", "kubeconfig file, in JSON")
		flags.Var(&onceBool{value: &r.inCluster}, "in-cluster", "use this pod's ServiceAccount")
		flags.Var(&onceBool{value: &r.cluster}, "cluster", "the apps whose releases Flux applied on the cluster")
		flags.Var(&once{value: &r.records}, "records", "the apps of this v2 records tree")
		flags.Var(&onceBool{value: &r.dryRun}, "dry-run", "read and plan without writing to ClickStack or the record")
	case "status":
		flags.Var(&once{value: &r.reviewedDigest}, "reviewed-digest", "submitted reviewed digest (single status only)")
		flags.Var(&onceBool{value: &r.all}, "all", "observe all recorded instances; exclusive with --app/--env")
		flags.Var(&once{value: &r.changes}, "changes", "complete, incomplete or unavailable revision comparisons")
		flags.Var(&once{value: &r.repoRoot}, "repo-root", "repository root for changed paths (default .; requires --changes)")
		flags.Var(&once{value: &r.records}, "records", "the tree every v2-owned instance keeps its records in")
		flags.Var(&once{value: &r.app}, "app", "application")
		flags.Var(&once{value: &r.env}, "env", "environment")
		r.registerLive(flags)
		flags.Var(&onceBool{value: &r.inCluster}, "in-cluster", "use this pod's ServiceAccount instead of a kubeconfig")
		flags.Var(&onceBool{value: &r.archive}, "archive", "also read the newest archive from the object store (needs AWS credentials)")
		flags.Var(&onceBool{value: &r.json}, "json", "print the v2-observation document instead of the text")
	case "verify":
		flags.Var(&once{value: &r.kubeconfig}, "kubeconfig", "kubeconfig file, in JSON")
		flags.Var(&onceBool{value: &r.inCluster}, "in-cluster", "use this pod's ServiceAccount instead of a kubeconfig")
		flags.Var(&once{value: &r.source}, "source", "name of the evidence source (default: the kubeconfig's current context)")
		flags.Var(&onceBool{value: &r.json}, "json", "print the v2-platform-checks document instead of the text")
		flags.Var(&once{value: &r.scope}, "scope", "cluster (the default) or platform")
		flags.Var(&once{value: &r.root}, "root", "the checkout the platform scope compares the cluster with")
		flags.Var(&once{value: &r.consoleTag}, "console-tag", "the checkout's console content tag (platform scope)")
		flags.Var(&once{value: &r.heartbeat}, "heartbeat", "systemctl show output for the host heartbeat units (platform scope)")
	case "restore":
		r.registerLive(flags)
		flags.Var(&once{value: &r.from}, "from", "the archive to restore (default: the newest)")
		flags.Var(&once{value: &r.confirm}, "confirm", "APPLICATION/ENVIRONMENT: perform the restore")
		flags.Var(&once{value: &r.ageKey}, "age-key-file", "the age identity that decrypts the archive (default ./age.agekey)")
		flags.Var(&onceDuration{value: &r.urlTTL, set: &r.urlTTLSet}, "url-ttl",
			"how long the pod's read URL stays valid (default 1h)")
	case "mint":
		flags.Var(&once{value: &r.profile}, "profile", "platform profile file")
		flags.Var(&onceBool{value: &r.rotate}, "rotate", "replace an existing broker identity")
		r.registerOut(flags)
	default:
		return false
	}
	return true
}

// registerBridgeFlags adds the flags of the commands that render the platform's own files.
func (r *request) registerBridgeFlags(flags *flag.FlagSet, command string) {
	switch command {
	case "policy":
		flags.Var(&once{value: &r.records}, "records", "the complete v2 records tree")
		flags.Var(&once{value: &r.profile}, "profile", "platform profile file")
		flags.Var(&once{value: &r.chart}, "chart", "explicit local chart directory, matching the profile name/version")
		r.registerOut(flags)
	case "render":
		flags.Var(&once{value: &r.records}, "records", "the tree every v2-owned instance keeps its records in")
		flags.Var(&once{value: &r.profile}, "profile", "platform profile file")
		flags.Var(&once{value: &r.revision}, "revision", "the commit the records are read at (required to write)")
		flags.Var(&once{value: &r.previous}, "previous", "a previous allocation to keep ports from (optional)")
		flags.Var(&onceBool{value: &r.check}, "check", "compare with the files under --out and write nothing")
		r.registerOut(flags)
	case "proxy":
		flags.Var(&once{value: &r.allocation}, "allocation", "egress allocation document")
		flags.Var(&once{value: &r.profile}, "profile", "platform profile file")
		r.registerOut(flags)
	}
}

// registerRecords adds the instance, its release and the profile.
func (r *request) registerRecords(flags *flag.FlagSet) {
	flags.Var(&once{value: &r.profile}, "profile", "platform profile file")
}

// registerCreateFlags adds what a create states about the app it makes: its team, the image it starts from and the
// one choice an image cannot make. Everything else the instance starts with is the image's declaration — what it needs,
// the memory it runs in, the volume its database is created with, what it says about itself and what it offers.
func (r *request) registerCreateFlags(flags *flag.FlagSet) {
	flags.Var(&once{value: &r.records}, "records", "the tree every v2-owned instance keeps its records in")
	flags.Var(&once{value: &r.app}, "app", "the application to create")
	flags.Var(&once{value: &r.team}, "team", "the owning team")
	flags.Var(&once{value: &r.image}, "image", "the image its repository published, REPO:<run>-<commit>")
	flags.Var(&once{value: &r.profile}, "profile", "platform profile file")
	flags.Var(&once{value: &r.exposure}, "exposure", "private (default), or authenticated-web")
}

// registerOut adds the destination a command writes its files under.
func (r *request) registerOut(flags *flag.FlagSet) {
	r.outFlag = &once{value: &r.out}
	flags.Var(r.outFlag, "out", "destination directory")
}

// registerLive adds the flags a live observation and a restore plan share: the documents, the source revision,
// the cluster credential, and the archive's threshold and region.
func (r *request) registerLive(flags *flag.FlagSet) {
	flags.Var(&once{value: &r.profile}, "profile", "platform profile file")
	flags.Var(&once{value: &r.callFile}, "calls", "resolved connections document (optional)")
	flags.Var(&once{value: &r.egressFile}, "egress", "egress allocation document (optional)")
	flags.Var(&once{value: &r.revision}, "revision", "source revision Flux should have applied")
	flags.Var(&once{value: &r.kubeconfig}, "kubeconfig", "kubeconfig file, in JSON")
	flags.Var(&once{value: &r.source}, "source", "name of the evidence source (default: the kubeconfig's current context)")
	flags.Var(&once{value: &r.maxAge}, "max-age", "the oldest acceptable backup (default 26h)")
	flags.Var(&once{value: &r.region}, "region", "the bucket's region (default: discovered from the bucket)")
}

// take fills r from the arguments left after the flags; false is a usage error.
func (r *request) take(flags *flag.FlagSet) bool {
	if named := r.named(); named != nil {
		if flags.NArg() != 0 || slices.Contains(named, "") {
			return false
		}
		// An explicitly empty destination is a usage error, not a request to print the document.
		return r.outFlag == nil || !r.outFlag.set || r.out != ""
	}
	if flags.NArg() != 1 {
		return false
	}
	if r.command == "release-id" {
		r.release = flags.Arg(0)
	} else {
		r.instance = flags.Arg(0)
	}
	return true
}

// named is the flag values a command requires, or nil for a command that takes one file argument.
func (r *request) named() []string {
	if named, found := r.platformNamed(); found {
		return named
	}
	if required, found := requiredFlags[r.command]; found {
		return required(r)
	}
	return nil
}

// requiredFlags is what each command that names its inputs requires. A command that is not listed takes one file
// argument instead, which is why the table is also the list of the two commands that do.
var requiredFlags = map[string]func(*request) []string{
	"policy":                  func(r *request) []string { return []string{r.records, r.profile, r.out, r.chart} },
	"chart":                   func(r *request) []string { return []string{r.profile, r.repositories, r.cache} },
	"compile":                 func(r *request) []string { return []string{r.instance, r.release, r.app, r.profile} },
	"select-release":          func(r *request) []string { return []string{r.instance, r.release, r.expected} },
	"mint":                    func(r *request) []string { return []string{r.instance, r.profile, r.out} },
	"create":                  func(r *request) []string { return []string{r.records, r.app, r.team, r.image, r.profile, r.out} },
	"image-declaration":       func(r *request) []string { return []string{r.appDir} },
	"declaration":             func(r *request) []string { return []string{r.label, r.app, r.team} },
	"names":                   func(r *request) []string { return []string{r.catalogue, r.appDir} },
	"remove":                  func(r *request) []string { return []string{r.records, r.app, r.env, r.profile, r.callFile, r.out} },
	"status":                  (*request).statusNamed,
	"restore":                 func(r *request) []string { return []string{r.instance, r.release, r.profile, r.revision, r.kubeconfig} },
	"promote":                 (*request).appNamed,
	"deploy":                  (*request).deployNamed,
	"edit":                    (*request).editNamed,
	"team":                    (*request).teamNamed,
	"workstreams":             func(r *request) []string { return []string{r.repoRoot} },
	"check-repo":              (*request).checkRepoNamed,
	"check-secrets":           func(r *request) []string { return []string{r.root, r.ageKey} },
	"verify":                  (*request).verifyNamed,
	"notify-serve":            func(r *request) []string { return []string{r.listen} },
	"notifications-check":     (*request).checkerNamed,
	"notifications-heartbeat": (*request).heartbeatNamed,
	"clickstack-dashboards":   (*request).dashboardsNamed,
	"clickstack-bootstrap":    func(r *request) []string { return []string{r.kubeconfig} },
	"flags":                   (*request).flagsNamed,
	"console":                 (*request).consoleNamed,
	"change":                  (*request).changeNamed,
	"app-repo":                (*request).appRepoNamed,
	"app-hooks":               func(r *request) []string { return []string{r.records, r.owner, r.hookHost} },
	"unit":                    (*request).unitNamed,
	"flux-wait":               func(r *request) []string { return []string{r.kubeconfig} },
}

// statusNamed is what a live read needs: the instance's records, the profile, the revision to expect and exactly one
// credential, a kubeconfig or the pod's own ServiceAccount. Neither or both is a usage error: nothing is detected.
func (r *request) statusNamed() []string {
	if (r.kubeconfig != "") == r.inCluster || (r.repoRoot != "" && r.changes == "") {
		return []string{""}
	}
	if r.all {
		if r.app != "" || r.env != "" || r.reviewedDigestSet {
			return []string{""}
		}
		return []string{r.records, r.profile, r.revision}
	}
	return []string{r.records, r.app, r.env, r.profile, r.revision}
}

// checkRepoNamed is the checkout check-repo reads; --rendered prints manifests, so it takes no --json.
func (r *request) checkRepoNamed() []string {
	if r.rendered != "" && r.json {
		return []string{""}
	}
	return []string{r.root}
}

// unitNamed is one action on one unit, and exactly one credential: a kubeconfig or the pod's own ServiceAccount.
func (r *request) unitNamed() []string {
	if (r.kubeconfig != "") == r.inCluster || r.action == "" || r.unit == "" {
		return []string{""}
	}
	return []string{r.unit}
}

// consoleNamed is the console's port and exactly one credential, a kubeconfig or the pod's own ServiceAccount
// (neither or both is a usage error).
func (r *request) consoleNamed() []string {
	if (r.kubeconfig != "") == r.inCluster {
		return []string{""}
	}
	return []string{r.port}
}

// flagsNamed is one instance's records, and one write at most: a set and a clear together are a usage error.
func (r *request) flagsNamed() []string {
	if r.change != "" && r.cleared != "" {
		return []string{""}
	}
	return []string{r.records, r.app, r.env}
}

// verifyNamed is the cluster scope's exactly one credential, a kubeconfig or the pod's own ServiceAccount (neither or
// both is a usage error), or the platform scope's kubeconfig and checkout: the pod's ServiceAccount cannot read what
// the platform scope needs, and the cluster scope compares with no checkout.
func (r *request) verifyNamed() []string {
	switch r.scope {
	case "", "cluster":
		if (r.kubeconfig != "") == r.inCluster || r.root != "" || r.consoleTag != "" || r.heartbeat != "" {
			return []string{""}
		}
		return []string{}
	case "platform":
		if r.inCluster {
			return []string{""}
		}
		return []string{r.kubeconfig, r.root}
	}
	return []string{""}
}

// appNamed is what a promotion needs: the tree both instances keep their records in, the application and the
// environment promoted into, the revision the caller reviewed, and where to write.
func (r *request) appNamed() []string {
	return []string{r.records, r.app, r.env, r.profile, r.expected, r.out}
}

// editNamed is what an edit requires: one named change, and the environment it applies to. A team is the app's fact,
// so that one change names no environment and every environment of the app is rewritten.
func (r *request) editNamed() []string {
	name, _, _ := strings.Cut(r.change, "=")
	if name == "team" {
		return []string{r.records, r.app, r.change, r.profile, r.out}
	}
	return []string{r.records, r.app, r.env, r.change, r.profile, r.out}
}

// deployNamed is the two forms the deploy command has: one instance's records named on the line, or the tree every
// v2-owned staging instance keeps its records in. Mixing them is a usage error.
func (r *request) deployNamed() []string {
	if r.records == "" {
		return []string{r.instance, r.release, r.app, r.profile, r.expected, r.out}
	}
	if r.instance != "" || r.release != "" || r.app != "" || r.expected != "" {
		return []string{""}
	}
	return []string{r.records, r.profile, r.out}
}

// platformNamed is the flags a command that renders or reads the platform's own documents requires: the records
// tree, or the allocation the proxy renders from.
func (r *request) platformNamed() ([]string, bool) {
	switch r.command {
	case "render":
		return []string{r.records, r.profile, r.out}, true
	case "proxy":
		return []string{r.allocation, r.profile}, true
	}
	return nil, false
}

// onceBool is a boolean flag that may be given at most once, like every other flag.
type onceBool struct {
	value *bool
	set   bool
}

func (o *onceBool) String() string { return "" }

func (o *onceBool) Set(value string) error {
	if o.set {
		return errors.New("flag repeated")
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return err
	}
	o.set, *o.value = true, parsed
	return nil
}

// IsBoolFlag makes -archive, without a value, set the flag.
func (o *onceBool) IsBoolFlag() bool { return true }

// once is a flag that may be given at most once: a repeat is a usage error, not the last one winning.
type once struct {
	value *string
	set   bool
}

func (o *once) String() string { return "" }

func (o *once) Set(value string) error {
	if o.set {
		return errors.New("flag repeated")
	}
	o.set, *o.value = true, value
	return nil
}

// onceDuration is a duration flag that may be given at most once, like every other flag.
type onceDuration struct {
	value *time.Duration
	set   *bool
}

func (o *onceDuration) String() string { return "" }

func (o *onceDuration) Set(value string) error {
	if *o.set {
		return errors.New("flag repeated")
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return err
	}
	*o.set, *o.value = true, parsed
	return nil
}

// cli runs one parsed command. Each step returns 0 or the exit status after its diagnostic.
type cli struct {
	out, diagnostic io.Writer
	charts          helm.Renderer
	acquirer        helm.Acquirer
	readFile        func(string) ([]byte, error)
	writer          files.Writer
	// archives builds the object-store reader; a test supplies one that answers without a network.
	archives func(bucket, region string) (s3.Reader, error)
	// registries builds the image-registry reader for one artifact image; a test supplies one that answers
	// from a script.
	registries func(image string) (registry.Reader, error)
	// seed and encrypter are the two effects a mint needs; a test supplies both, so it needs neither randomness
	// nor sops. Commands that never mint are given the real ones too.
	seed      func() ([32]byte, error)
	encrypter sops.Encrypter
	// decrypter is what check-secrets decrypts with, and nothing else reads it; nil is sops on PATH, and a test
	// supplies a fake so it needs neither sops nor a key.
	decrypter sops.Decrypter
	// stdin is where a channel's address is read from; a test supplies its own.
	stdin io.Reader
	// inCluster is where status --in-cluster finds the pod's ServiceAccount; a test supplies a folder and an
	// environment of its own.
	inCluster inCluster
	// git reads a checkout for the workstream check; nil is the git command on PATH.
	git       git.Reader
	appSource appsource.Reader
	github    github.Reader
	// consoleSource and publisher are the GitHub ports a change reads and writes through; nil is the real API, and
	// a test supplies fakes so an opened pull request needs no network.
	consoleSource github.Console
	publisher     github.Publisher
	// repositoryWriter and renderer are the two effects a new app repository needs; nil is the real GitHub write
	// port and Copier, and a test supplies fakes so no repository is created.
	repositoryWriter github.RepositoryWriter
	renderer         copier.Renderer
	// sleep is the wait between polls; nil is time.Sleep.
	sleep func(time.Duration)
	// unitClusterOf is the cluster a unit action changes; nil is the API server the credential names, and a test
	// supplies a scripted cluster so an action needs no network.
	unitClusterOf func() (orchestration.Cluster, error)
	// platform publishes to the platform topic; nil is ntfy over HTTPS. A test supplies a fake.
	platform ntfy.Publisher
	// getenv reads the environment the flags card signs in with; nil is os.Getenv, and a test supplies its own.
	getenv func(string) string
	// natsStore signs in to the event service; nil is the protocol's own adapter, and a test supplies a fake store.
	natsStore func(context.Context, string, string, string) (nats.KV, error)
	// clock is the time an override's age is measured from; nil is the wall clock.
	clock func() time.Time
}

// environment is one variable of this process's environment.
func (c cli) environment(name string) string {
	if c.getenv == nil {
		return os.Getenv(name)
	}
	return c.getenv(name)
}

// now is the instant an override's age is measured from.
func (c cli) now() time.Time {
	if c.clock == nil {
		return time.Now()
	}
	return c.clock()
}

func (c cli) releaseID(path string) int {
	release, status := c.release(path)
	if status != 0 {
		return status
	}
	return emit(c.out, c.diagnostic, []byte(release.ReleaseID))
}

func (c cli) instanceRevision(path string) int {
	spec, status := c.instance(path)
	if status != 0 {
		return status
	}
	revision, err := spec.Revision()
	if err != nil {
		return fail(c.diagnostic, err)
	}
	return emit(c.out, c.diagnostic, []byte(revision))
}

func (c cli) selectRelease(r request) int {
	spec, status := c.instance(r.instance)
	if status != 0 {
		return status
	}
	release, status := c.release(r.release)
	if status != 0 {
		return status
	}
	selected, err := kernel.SelectRelease(spec, release, kernel.InstanceRevision(r.expected))
	if err != nil {
		return fail(c.diagnostic, err)
	}
	data, err := kernel.EncodeInstance(selected)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	return emit(c.out, c.diagnostic, data)
}

func (c cli) compile(r request) int {
	inputs, status := c.inputs(r)
	if status != 0 {
		return status
	}
	if r.eventsSecret != "" {
		data, status := c.read(r.eventsSecret, "events secret")
		if status != 0 {
			return status
		}
		inputs.EventsSecret = data
	}
	documents, status := c.offersAt(r.offers)
	if status != 0 {
		return status
	}
	inputs.Offers = documents
	if r.out != "" {
		if c.appTreeRefused(r.out) {
			return 2
		}
		result, err := orchestration.CompileInto(inputs, r.out, c.writer)
		return c.wrote(result.Written, err)
	}
	compiled, err := orchestration.Compile(inputs)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	return emit(c.out, c.diagnostic, kernel.EncodeCompilation(compiled))
}

// inputs reads the documents a compilation takes: the instance's records, the profile, and the two optional
// documents. Which of them a use case needs is the kernel's to refuse, not this command's to remember.
func (c cli) inputs(r request) (orchestration.Inputs, int) {
	var inputs orchestration.Inputs
	for _, read := range []struct {
		path   string
		what   string
		target *[]byte
	}{
		{r.instance, "instance", &inputs.Instance},
		{r.release, "release", &inputs.Release},
		{r.app, "app", &inputs.App},
		{r.profile, "profile", &inputs.Profile},
		{r.callFile, "", &inputs.Calls},
		{r.egressFile, "", &inputs.Egress},
	} {
		if read.path == "" {
			continue // an optional document the operator did not give
		}
		data, status := c.read(read.path, read.what)
		if status != 0 {
			return orchestration.Inputs{}, status
		}
		*read.target = data
	}
	return inputs, 0
}

// wrote reports a use case's file outcomes and maps its failure to an exit status. Outcomes are printed
// before the failure, so a partly written destination is visible.
func (c cli) wrote(outcomes []files.Outcome, err error) int {
	for _, outcome := range outcomes {
		if _, writeErr := fmt.Fprintf(c.out, "%s %s\n", outcome.State, outcome.Path); writeErr != nil {
			fmt.Fprintln(c.diagnostic, "io: output: cannot write result")
			return 1
		}
	}
	if err == nil {
		return 0
	}
	return c.failure(err)
}

// failure reports a use case's error and maps it to an exit status. Reading an input is the command's own
// step and already reports its I/O as 1, so what reaches here is a refusal or validation (2) unless the
// filesystem itself failed while writing (1).
func (c cli) failure(err error) int {
	fmt.Fprintln(c.diagnostic, err)
	return exitFor(err)
}

// exitFor is the status an error earns wherever a delivery reports one: 1 for I/O and an unavailable registry,
// 2 for anything the instance or the caller refused.
func exitFor(err error) int {
	var failure *files.Error
	if errors.As(err, &failure) && failure.Code == files.IO {
		return 1
	}
	var unavailable *registry.Error
	if errors.As(err, &unavailable) && unavailable.Code == registry.Unavailable {
		return 1
	}
	return 2
}

// writeFiles writes rendered files under dir and reports each one, for a command whose use case returns the
// files rather than writing them. A destination inside the checkout's apps/ is refused here: that tree is
// where v1's instances are recorded, which is this checkout's rule rather than a use case's.
func (c cli) writeFiles(dir string, rendered []kernel.File) int {
	if c.appTreeRefused(dir) {
		return 2
	}
	outcomes, err := c.writer.Write(dir, rendered)
	return c.wrote(outcomes, err)
}

// appTreeRefused reports and refuses a destination inside the checkout's apps/ tree.
func (c cli) appTreeRefused(dir string) bool {
	if _, inside := appTree(dir); !inside {
		return false
	}
	fmt.Fprintf(c.diagnostic, "%s: destination: %s\n", files.RefusedDestination, "inside the v1 app tree")
	return true
}

// appTree reports the v1 app tree a destination would write into, if any: the apps/ directory of the
// checkout that contains it. A checkout has .git or clusters/ beside apps/, which is what makes an
// apps/ directory this repository's rather than an unrelated folder that happens to be called apps.
func appTree(dir string) (string, bool) {
	resolved, err := resolve(dir)
	if err != nil {
		return "", false
	}
	for current := resolved; ; current = filepath.Dir(current) {
		if isCheckout(current) {
			tree := filepath.Join(current, kernel.AppTree)
			if resolved == tree || strings.HasPrefix(resolved, tree+string(filepath.Separator)) {
				return tree, true
			}
			return "", false
		}
		if parent := filepath.Dir(current); parent == current {
			return "", false
		}
	}
}

func isCheckout(dir string) bool {
	for _, marker := range []string{".git", "clusters"} {
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return true
		}
	}
	return false
}

// resolve is filepath.Abs plus the symlinks of the nearest existing ancestor: the destination may not
// exist yet, but a link the operator passes must not hide where the files would really go.
func resolve(dir string) (string, error) {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	existing, rest := absolute, ""
	for {
		if _, err := os.Lstat(existing); err == nil {
			resolved, err := filepath.EvalSymlinks(existing)
			if err != nil {
				return "", err
			}
			return filepath.Join(resolved, rest), nil
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return absolute, nil
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = parent
	}
}

func (c cli) release(path string) (kernel.ReleaseBundle, int) {
	data, status := c.read(path, "release")
	if status != 0 {
		return kernel.ReleaseBundle{}, status
	}
	release, err := kernel.DecodeRelease(data)
	if err != nil {
		return kernel.ReleaseBundle{}, fail(c.diagnostic, err)
	}
	return release, 0
}

// profile reads and decodes the platform profile, which the compiler and the preview both need.
func (c cli) profile(path string) (kernel.PlatformProfile, int) {
	data, status := c.read(path, "profile")
	if status != 0 {
		return kernel.PlatformProfile{}, status
	}
	profile, err := kernel.DecodeProfile(data)
	if err != nil {
		return kernel.PlatformProfile{}, fail(c.diagnostic, err)
	}
	return profile, 0
}

func (c cli) instance(path string) (kernel.InstanceSpec, int) {
	data, status := c.read(path, "instance")
	if status != 0 {
		return kernel.InstanceSpec{}, status
	}
	spec, err := kernel.DecodeInstance(data)
	if err != nil {
		return kernel.InstanceSpec{}, fail(c.diagnostic, err)
	}
	return spec, 0
}

// read refuses a file over maxInput (exit 2, like any refused input); other read errors are I/O (exit 1).
// offersAt reads the documents an instance publishes: the plain files of one folder, each named by its own name
// inside the instance's offers folder, in name order. An absent folder is no documents rather than a failure; the
// use case refuses a record that states a provenance without documents, or documents without one.
func (c cli) offersAt(folder string) ([]kernel.File, int) {
	if folder == "" {
		return nil, 0
	}
	entries, err := os.ReadDir(folder)
	if err != nil {
		// A folder named on the line is read or the command fails: silence would look like an instance that
		// publishes nothing.
		fmt.Fprintf(c.diagnostic, "io: offers: cannot read %s\n", folder)
		return nil, 1
	}
	documents := make([]kernel.File, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			fmt.Fprintf(c.diagnostic, "invalid: offers: %s is a folder\n", entry.Name())
			return nil, 2
		}
		data, status := c.read(filepath.Join(folder, entry.Name()), "offers document")
		if status != 0 {
			return nil, status
		}
		documents = append(documents, kernel.File{Path: entry.Name(), Document: data})
	}
	return documents, 0
}

func (c cli) read(path, what string) ([]byte, int) {
	data, err := c.readFile(path)
	switch {
	case errors.Is(err, errTooLarge):
		fmt.Fprintf(c.diagnostic, "invalid: %s: %v\n", what, errTooLarge)
		return nil, 2
	case err != nil:
		fmt.Fprintf(c.diagnostic, "io: %s: cannot read input file\n", what)
		return nil, 1
	}
	return data, 0
}

func fail(diagnostic io.Writer, err error) int { fmt.Fprintln(diagnostic, err); return 2 }

func emit(out, diagnostic io.Writer, data []byte) int {
	if len(data) == 0 || data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	n, err := out.Write(data)
	if err != nil || n != len(data) {
		fmt.Fprintln(diagnostic, "io: output: cannot write result")
		return 1
	}
	return 0
}
