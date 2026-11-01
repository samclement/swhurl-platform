"""Live checks of the apps: what Git declares for the instances is what the cluster has.

Every instance is v2's, so what Git declares is v2's compiled tree: ``v2-apps/instances/<app>/<env>/`` holds the
objects v2 rendered from the records — the Namespace, the ResourceQuota, the NetworkPolicy, each stream and
consumer — and ``make check-apps`` (v2's own ``render --check``) keeps that equal to the records. The checks read
no v1 record: there is none to read, and re-deriving v2's namespace names and labels here would be a second model.

Each takes a Runner and a Report and only reads; ``verify.CHECKS`` lists them (make verify-platform).
"""
from __future__ import annotations

import json
from pathlib import Path

from swhurl import ROOT, interface
from swhurl.apps import calling, contract, egress, events, hooks, images
from swhurl.kube import ready_condition
from swhurl.report import Report
from swhurl.run import CommandError, Runner

V2_RECORDS = Path('v2-records')
COMPILED = Path('v2-apps/instances')
ALLOCATION = Path('platform/egress/allocation.json')


def app_of(policy: dict) -> str:
    return (policy['metadata'].get('labels') or {}).get(interface.APP, '')


def compiled(root: Path = ROOT) -> list[tuple[str, str, Path]]:
    """Every instance v2 compiled from its records — ``(app, env, folder)`` — in order.

    The folder is what Git declares to the cluster for that instance, so a check compares live objects with it
    rather than deriving v2's names and labels again; an empty tree (a checkout that has never rendered) is []
    and every instance is then simply not checked here.
    """
    base = root / COMPILED
    return [(folder.parent.name, folder.name, folder) for folder in sorted(base.glob('*/*')) if folder.is_dir()]


def declared(folder: Path, name: str) -> dict:
    """One object v2 rendered into an instance's folder (``namespace.json``, ``stream.json``, ...)."""
    return json.loads((folder / name).read_text())


def judge_staging(report: Report, root: Path, app: str, name: str, latest: str,
                  opener: images.Opener = images._open) -> None:
    """Does staging run the image its ImagePolicy picked, read from the record that owns the instance?

    The instance is v2's: ``v2-records/<app>/staging/release.json`` states the digest of the image's
    ``linux/amd64`` manifest, so the tag the policy picked is resolved through the record's own image (a public
    package, no credential) and that entry compared with what the record states.
    """
    record = root / V2_RECORDS / app / contract.AUTO_DEPLOY_ENV / 'release.json'
    stated = record.relative_to(root)
    try:
        artifact = json.loads(record.read_text())['artifact']
        digest, image = artifact['digest'], artifact.get('image') or ''
    except FileNotFoundError:
        report.warn(f'{name}: newest image {latest}, but this checkout has no {stated} (git pull if it is behind main)')
        return
    except (OSError, ValueError, AttributeError, KeyError, TypeError) as error:
        report.bad(f'{stated} cannot be read ({error}), so whether staging runs {latest} is unchecked')
        return
    repository = image.removeprefix(f'{images.REGISTRY}/')
    if not repository:
        report.bad(f'{stated} states no image, so whether staging runs {latest} is unchecked')
        return
    platform = str(artifact.get('platform') or 'linux/amd64')
    try:
        found = images.platform_digest(repository, latest, platform, opener=opener)
    except (images.ImageError, OSError) as error:
        report.warn(f'{name}: cannot read the digest of {repository}:{latest} from the registry ({error}), so whether '
                    f'{stated} is deployed is unchecked')
        return
    if found == digest:
        report.ok(f'{name}: newest image {latest}, deployed')
        return
    report.warn(f'{name}: newest image {latest} is not what v2 records state: {stated} names {digest}, the registry '
                f'says {found}. The Deploy v2 workflow deploys it within the hour (git pull: it may already have); '
                'to deploy it now: make v2-deploy, commit and push')


def check_image_automation(runner: Runner, report: Report, root: Path = ROOT,
                          opener: images.Opener = images._open) -> None:
    """Automatic staging deploys: every app ImagePolicy is Ready, and staging runs the image it picked
    (the Deploy workflow writes it: docs/apps.md#deploy-a-new-image)."""
    report.section('Image Automation')
    try:
        policies = runner.json(['kubectl', '-n', 'flux-system', 'get', 'imagepolicies.image.toolkit.fluxcd.io',
                                '-l', interface.APP, '-o', 'json'])['items']
    except (CommandError, KeyError, TypeError):
        report.bad("cannot read the apps' ImagePolicies, so no staging deploy can be checked "
                   '(kubectl -n flux-system get imagepolicies; is image-reflector-controller running? make flux-install)')
        return
    for policy in sorted(policies, key=lambda p: p['metadata']['name']):
        name = policy['metadata']['name']
        status, message = ready_condition(policy)
        if status != 'True':
            report.bad(f'ImagePolicy {name} is not Ready, so staging gets no new images: {message} '
                       f'(kubectl -n flux-system describe imagerepository {app_of(policy)}; make reconcile UNIT=app-{name})')
            continue
        latest = ((policy.get('status') or {}).get('latestRef') or {}).get('tag', '')
        judge_staging(report, root, app_of(policy), name, latest, opener)


def check_isolation(runner: Runner, report: Report, root: Path = ROOT) -> None:
    """Every instance v2 compiled has, live, its namespace, its team, the platform quota and a NetworkPolicy.

    What Git declares is v2's compiled folder: the namespace with its labels, the ResourceQuota and the
    NetworkPolicy, compared field by field with the cluster. Namespaces kept for their data after an app was
    removed are not instances and are not checked.
    """
    report.section('App isolation')
    try:
        namespaces = runner.json(['kubectl', 'get', 'namespaces', '-l', interface.APP, '-o', 'json'])['items']
        quotas = runner.json(['kubectl', 'get', 'resourcequotas', '--all-namespaces', '-o', 'json'])['items']
        policies = runner.json(['kubectl', 'get', 'networkpolicies.networking.k8s.io', '--all-namespaces',
                                '-o', 'json'])['items']
    except (CommandError, KeyError, TypeError):
        report.bad('cannot read app namespaces, ResourceQuotas or NetworkPolicies, so app isolation is unchecked '
                   f'(kubectl get namespaces -l {interface.APP})')
        return
    known = contract.teams(root)
    live = {n['metadata']['name']: n['metadata'].get('labels') or {} for n in namespaces}
    complete = 0
    for app, env, folder in compiled(root):
        try:
            namespace = declared(folder, 'namespace.json')
            quota = declared(folder, 'resourcequota.json')
            policy = declared(folder, 'networkpolicy.json')
        except (OSError, ValueError) as error:
            report.bad(f'{COMPILED}/{app}/{env}: its compiled objects cannot be read ({error}), so whether the '
                       'cluster has them is unchecked (make check-apps recompiles them)')
            continue
        name = namespace['metadata']['name']
        if name not in live:
            report.bad(f'{name}: the namespace does not exist, so the instance is not running '
                       f'(make app-reconcile APP={app} ENV={env}; make flux-reconcile shows why it fails)')
            continue
        problems = []
        found, wanted = live[name], namespace['metadata'].get('labels') or {}
        # The cluster adds its own labels to what Flux applies (kubernetes.io/metadata.name on every namespace,
        # kustomize.toolkit.fluxcd.io/name and .../namespace on the objects it owns): only Git's are compared.
        moved = sorted(key for key, value in wanted.items() if found.get(key) != value)
        if moved:
            problems.append('the namespace labels differ from Git (' + ', '.join(moved) + ')')
        team = wanted.get(interface.TEAM)
        if team not in known:
            problems.append(f'team {team!r} is not in {contract.TEAMS_FILE} '
                            f'(make team-add NAME={team} DESCRIPTION="...")')
        if not any(q['metadata']['namespace'] == name and (q.get('spec') or {}).get('hard') == quota['spec']['hard']
                   for q in quotas):
            problems.append('no ResourceQuota as Git declares it')
        selector = policy['spec']['podSelector']
        if not any(p['metadata']['namespace'] == name and (p.get('spec') or {}).get('podSelector') == selector
                   for p in policies):
            problems.append("no NetworkPolicy on the app's pods")
        if problems:
            report.bad(f'{name}: ' + '; '.join(problems)
                       + f' (make check-apps, then make app-reconcile APP={app} ENV={env})')
        else:
            complete += 1
    if complete:
        report.ok(f'{complete} app namespace(s) have a registered team, the platform quota and a NetworkPolicy')


def check_signing_keys(runner: Runner, report: Report, root: Path = ROOT) -> None:
    """The token issuer and public signing keys apps were given are the cluster's own."""
    report.section('Call identity')
    try:
        live = runner.json(['kubectl', 'get', '--raw', '/openid/v1/jwks'])
        issuer = runner.json(['kubectl', 'get', '--raw', '/.well-known/openid-configuration']).get('issuer')
    except (CommandError, AttributeError):
        report.bad('cannot read the cluster\'s token issuer or signing keys (kubectl get --raw /openid/v1/jwks)')
        return
    path = root / calling.SIGNING_KEYS_FILE
    try:
        auth = json.loads(path.read_text())['auth']
        stated = json.loads(auth['keys'])
    except (OSError, ValueError, KeyError, TypeError):
        report.bad(f'{calling.SIGNING_KEYS_FILE} states no signing keys, so apps refuse every call between them: '
                   'put the cluster\'s key set in its auth block and render platform/auth from it')
        return
    if stated != live:
        report.bad(f'{calling.SIGNING_KEYS_FILE} does not state the cluster\'s signing key set, so apps refuse every '
                   'call between them: put the cluster\'s key set in its auth block and render platform/auth from it '
                   '(docs/operations.md#signing-keys)')
    else:
        report.ok(f'{calling.SIGNING_KEYS_FILE} states the cluster\'s {len(live.get("keys") or [])} signing key(s)')
    if auth.get('issuer') == issuer:
        report.ok(f'token issuer is {issuer}')
    else:
        report.bad(f'token issuer is {issuer}, but the profile states {auth.get("issuer")}, so apps refuse every call '
                   'between them: fix its auth.issuer and render platform/auth from it (docs/operations.md#signing-keys)')


def declared_objects(folder: Path) -> list[dict]:
    """The stream and consumers v2 rendered for one instance: the events it publishes or subscribes to."""
    names = sorted([path.name for path in folder.glob('stream.json')]
                   + [path.name for path in folder.glob('consumer-*.json')])
    return [declared(folder, name) for name in names]


def check_events(runner: Runner, report: Report, root: Path = ROOT) -> None:
    """NATS and its stream controller run, and every stream and consumer the instances declare is Ready in NATS."""
    report.section('Events')
    try:
        server = runner.json(['kubectl', '-n', events.NAMESPACE, 'get', 'statefulset', 'nats', '-o', 'json'])
        controller = runner.json(['kubectl', '-n', events.NAMESPACE, 'get', 'deployment', 'nats-controller', '-o', 'json'])
        declared = runner.json(['kubectl', 'get', 'streams.jetstream.nats.io,consumers.jetstream.nats.io',
                                '--all-namespaces', '-o', 'json'])['items']
    except (CommandError, KeyError, TypeError):
        report.bad('cannot read NATS, its controller, or the streams and consumers (make reconcile UNIT=platform-nats)')
        return
    for name, workload in (('NATS', server), ('the stream controller', controller)):
        ready = ((workload or {}).get('status') or {}).get('readyReplicas', 0)
        (report.ok if ready else report.bad)(f'{name} is running' if ready else f'{name} is not running: no events are '
                                             'published or delivered (make reconcile UNIT=platform-nats)')
    found = {(item['kind'], item['metadata']['namespace'], item['metadata']['name']): item for item in declared}
    wanted = [doc for _app, _env, folder in compiled(root) for doc in declared_objects(folder)]
    missing, waiting = [], 0
    for doc in wanted:
        key = (doc['kind'], doc['metadata']['namespace'], doc['metadata']['name'])
        live = found.get(key)
        where = f'{key[0]} {key[1]}/{key[2]}'
        if live is None:
            missing.append(where)
            report.bad(f'{where} is what Git declares, but NATS has no such {key[0].lower()} (make reconcile '
                       'UNIT=platform-nats; make check-apps says whether Git holds the declaration)')
            continue
        status, message = ready_condition(live)
        if status != 'True':
            waiting += 1
            report.bad(f'{where} is not Ready, so its events are not kept or delivered: {message or "no message"} '
                       f'(kubectl -n {key[1]} describe {key[0].lower()} {key[2]})')
    if wanted and not missing and not waiting:
        report.ok(f'{len(wanted)} declared stream(s) and consumer(s) exist in NATS')


def port_problems(root: Path = ROOT) -> list[str]:
    """Every instance v2 compiled has a port of its own on the egress proxy, from v2's allocation."""
    try:
        allocation = json.loads((root / ALLOCATION).read_text())
        ports = {(each['application'], each['environment']): each['port'] for each in allocation['instances']}
    except (OSError, ValueError, KeyError, TypeError) as error:
        return [f'{ALLOCATION} cannot be read ({error}), so the proxy\'s ports are unchecked']
    found, owners = [], {}
    for app, env, _folder in compiled(root):
        port = ports.get((app, env))
        if port is None:
            found.append(f'{app}/{env} has no port on the egress proxy: its records render the allocation '
                         '(docs/services.md#egress-proxy)')
        else:
            owners.setdefault(port, []).append(f'{app}/{env}')
    found += [f"port {port} is shared by {', '.join(held)}" for port, held in sorted(owners.items()) if len(held) > 1]
    return found


def check_egress(runner: Runner, report: Report, root: Path = ROOT) -> None:
    """The egress proxy runs every replica, with a port for each instance v2 compiled."""
    report.section('Egress proxy')
    try:
        deployment = runner.json(['kubectl', '-n', egress.NAMESPACE, 'get', 'deployment', egress.NAME, '-o', 'json'])
        wanted, ready = deployment['spec'].get('replicas', 1), (deployment.get('status') or {}).get('readyReplicas', 0)
    except (CommandError, KeyError, TypeError):
        report.bad(f'cannot read the egress proxy ({egress.NAMESPACE}/{egress.NAME}): apps cannot reach outside the cluster '
                   '(make reconcile UNIT=platform-egress)')
        return
    if not ready:
        report.bad('the egress proxy is not running: apps cannot reach outside the cluster '
                   f'(kubectl -n {egress.NAMESPACE} get pods; make reconcile UNIT=platform-egress)')
    elif ready < wanted:
        report.warn(f'the egress proxy runs {ready} of {wanted} replicas: a restart would stop outbound traffic meanwhile '
                   '(kubectl -n egress get pods)')
    else:
        report.ok(f'the egress proxy runs {ready} replicas')
    found = port_problems(root)
    for problem in found:
        report.bad(problem)
    if not found:
        report.ok(f'{len(compiled(root))} instance(s) each have a port of their own')


def check_image_webhooks(runner: Runner, report: Report, root: Path = ROOT) -> None:
    """The app-images Receiver is Ready and each auto-deploy app's repository calls it (docs/services.md#image-webhook)."""
    report.section('Image Webhooks')
    try:
        receiver = runner.json(['kubectl', '-n', hooks.RECEIVER_NAMESPACE, 'get',
                                'receivers.notification.toolkit.fluxcd.io', hooks.RECEIVER, '-o', 'json'])
        status, message = ready_condition(receiver)
    except (CommandError, TypeError):
        status, message = 'Unknown', 'could not read it'
    name = f'{hooks.RECEIVER_NAMESPACE}/{hooks.RECEIVER}'
    if status == 'True':
        report.ok(f'Flux receiver {name} is Ready')
    else:
        report.bad(f'Flux receiver {name} is not Ready: {message} (make reconcile UNIT=platform-flux-webhook)')
    host = hooks.webhook_host(root)
    fallback = 'Flux finds its new images within the hour'
    try:
        repositories = hooks.auto_deploy_repositories(root)
    except hooks.HookError as error:
        report.bad(str(error))
        return
    for app, repository in repositories.items():
        try:
            hook = hooks.find(runner.json(['gh', 'api', f'repos/{repository}/hooks']), host)
        except CommandError as error:
            report.warn(f'{app}: could not list the GitHub webhooks of {repository} with gh: {error}')
            continue
        last = (hook or {}).get('last_response') or {}
        if not hook:
            report.warn(f'{app}: no image webhook on {repository}; {fallback} (make app-hooks)')
        elif not hook.get('active') or not set(hooks.EVENTS) <= set(hook.get('events') or []):
            report.warn(f'{app}: the image webhook on {repository} is disabled or lacks its events; {fallback} '
                        '(make app-hooks)')
        elif last.get('code') in (200, None):
            report.ok(f'{app}: image webhook on {repository}'
                      + (', latest delivery returned 200' if last.get('code') else ', no delivery yet'))
        else:
            report.warn(f'{app}: the latest image webhook delivery from {repository} returned {last.get("code")} '
                        f'{last.get("message") or ""}'.rstrip() + f'; {fallback} (after a token rotation: make app-hooks)')
