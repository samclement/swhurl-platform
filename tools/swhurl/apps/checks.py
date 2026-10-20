"""Live checks of the apps: what Git declares for the instances is what the cluster has.

Each takes a Runner and a Report and only reads; ``verify.CHECKS`` lists them (make verify-platform).
"""
from __future__ import annotations

import json
from pathlib import Path

from swhurl import ROOT
from swhurl.apps import calling, contract, egress, events, hooks
from swhurl.apps import policy as app_policy
from swhurl.apps.instance import INSTANCE_ROOTS, Instance
from swhurl.apps.model import store
from swhurl.kube import ready_condition
from swhurl.report import Report
from swhurl.run import CommandError, Runner


def app_of(policy: dict) -> str:
    return (policy['metadata'].get('labels') or {}).get(contract.APP, '')


def check_image_automation(runner: Runner, report: Report, root: Path = ROOT) -> None:
    """Automatic staging deploys: every app ImagePolicy is Ready, and staging runs the image it picked
    (the Deploy workflow writes it: docs/apps.md#deploy-a-new-image)."""
    report.section('Image Automation')
    try:
        policies = runner.json(['kubectl', '-n', 'flux-system', 'get', 'imagepolicies.image.toolkit.fluxcd.io',
                                '-l', contract.APP, '-o', 'json'])['items']
    except (CommandError, KeyError, TypeError):
        report.bad("cannot read the apps' ImagePolicies, so no staging deploy can be checked "
                   '(kubectl -n flux-system get imagepolicies; is image-reflector-controller running? make flux-install)')
        return
    world, errors = store.world(root)
    if errors:
        for error in errors:
            report.bad(error)
        return
    for policy in sorted(policies, key=lambda p: p['metadata']['name']):
        name = policy['metadata']['name']
        status, message = ready_condition(policy)
        if status != 'True':
            report.bad(f'ImagePolicy {name} is not Ready, so staging gets no new images: {message} '
                       f'(kubectl -n flux-system describe imagerepository {app_of(policy)}; make reconcile UNIT=app-{name})')
            continue
        latest = ((policy.get('status') or {}).get('latestRef') or {}).get('tag', '')
        app = app_of(policy)
        release = root / INSTANCE_ROOTS[0] / app / contract.AUTO_DEPLOY_ENV / 'release.yaml'
        entry = world.get(Instance.named(app, contract.AUTO_DEPLOY_ENV))
        running = entry.release.image.tag if entry else ''
        if running == latest:
            report.ok(f'{name}: newest image {latest}, deployed')
        elif not running:
            report.warn(f'{name}: newest image {latest}, but this checkout has no {release.relative_to(root)} '
                        '(git pull if it is behind main)')
        else:
            report.warn(f'{name}: newest image {latest} is not deployed; staging in Git runs {running}. The Deploy '
                        'workflow deploys it within the hour (git pull: it may already have); to deploy it now, or see '
                        f'why it is held: make app-deploy APP={app}, commit and push')


def check_isolation(runner: Runner, report: Report, root: Path = ROOT) -> None:
    """Every instance in Git has, live, a registered team, the platform quota and a NetworkPolicy on the app's pods.

    Namespaces kept for their data after an app was removed are not instances and are not checked.
    """
    report.section('App isolation')
    try:
        namespaces = runner.json(['kubectl', 'get', 'namespaces', '-l', contract.APP, '-o', 'json'])['items']
        quotas = runner.json(['kubectl', 'get', 'resourcequotas', '--all-namespaces', '-o', 'json'])['items']
        policies = runner.json(['kubectl', 'get', 'networkpolicies.networking.k8s.io', '--all-namespaces',
                                '-o', 'json'])['items']
    except (CommandError, KeyError, TypeError):
        report.bad('cannot read app namespaces, ResourceQuotas or NetworkPolicies, so app isolation is unchecked '
                   f'(kubectl get namespaces -l {contract.APP})')
        return
    known = contract.teams(root)
    live = {n['metadata']['name']: n['metadata'].get('labels') or {} for n in namespaces}
    world, errors = store.world(root)
    if errors:
        for error in errors:
            report.bad(error)
        return
    complete = 0
    for instance in world:
        app, name = instance.app, instance.namespace
        if name not in live:
            report.bad(f'{name}: the namespace does not exist, so the instance is not running '
                       f'(make app-reconcile APP={app} ENV={instance.env}; make flux-reconcile shows why it fails)')
            continue
        labels = live[name]
        problems = []
        team = labels.get(contract.TEAM)
        if team not in known:
            problems.append(f'team {team!r} is not in {contract.TEAMS_FILE} (make app-team APP={app} ARGS="--team <team>"'
                            + (f', or make team-add NAME={team})' if team else ')'))
        if not any(q['metadata']['namespace'] == name and (q.get('spec') or {}).get('hard') == contract.QUOTA
                   for q in quotas):
            problems.append('no platform ResourceQuota')
        selector = {'matchLabels': {'app.kubernetes.io/instance': app}}
        if not any(p['metadata']['namespace'] == name and (p.get('spec') or {}).get('podSelector') == selector
                   for p in policies):
            problems.append("no NetworkPolicy on the app's pods")
        if problems:
            report.bad(f'{name}: ' + '; '.join(problems) + f' (make check-apps, then make app-reconcile APP={app} ENV={instance.env})')
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
    if not path.is_file():
        report.bad(f'{calling.SIGNING_KEYS_FILE} is missing, so apps refuse every call between them: '
                   'make platform-signing-keys, commit and push')
    elif json.loads(path.read_text()) != live:
        report.bad(f'{calling.SIGNING_KEYS_FILE} is not the cluster\'s signing key set: apps refuse every call between '
                   'them until you run make platform-signing-keys, commit and push')
    else:
        report.ok(f'{calling.SIGNING_KEYS_FILE} holds the cluster\'s {len(live.get("keys") or [])} signing key(s)')
    if issuer == calling.ISSUER:
        report.ok(f'token issuer is {issuer}')
    else:
        report.bad(f'token issuer is {issuer}, but apps are told {calling.ISSUER}, so they refuse every call between '
                   'them: set ISSUER in tools/swhurl/apps/calling.py, make app-sync, commit and push')


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
    waiting = []
    for item in declared:
        status, message = ready_condition(item)
        if status != 'True':
            waiting.append(f"{item['kind']} {item['metadata']['namespace']}/{item['metadata']['name']} is not Ready, so its "
                           f"events are not kept or delivered: {message or 'no message'} (kubectl -n {item['metadata']['namespace']} "
                           f"describe {item['kind'].lower()} {item['metadata']['name']})")
    for line in waiting:
        report.bad(line)
    if declared and not waiting:
        report.ok(f'{len(declared)} declared stream(s) and consumer(s) exist in NATS')
    for problem in app_policy.check_event_users(root):
        report.bad(problem.removeprefix('event-users: ') + ', commit and push')


def check_egress(runner: Runner, report: Report, root: Path = ROOT) -> None:
    """The egress proxy runs every replica, with a port for each instance (its file is what they generate)."""
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
                    f'(kubectl -n {egress.NAMESPACE} get pods)')
    else:
        report.ok(f'the egress proxy runs {ready} replicas')
    found = egress.problems(root)
    for problem in found:
        report.bad(problem.removeprefix('egress-ports: ') + ', commit and push')
    if not found:
        report.ok(f'{len(store.recorded(root / INSTANCE_ROOTS[0]))} instance(s) each have a port of their own')


def check_secrets(runner: Runner, report: Report, root: Path = ROOT) -> None:
    """Every secret name an instance's image needs has a value in Git (warns otherwise: the app runs without it);
    a value no name asks for is reported. Reads Git only: names from the release, keys from the Secret file."""
    from swhurl.apps.ops import MISSING, UNUSED, secret_states
    report.section('App secrets')
    world, errors = store.world(root)
    if errors:
        for error in errors:
            report.bad(error)
        return
    found = False
    for instance, entry in world.items():
        folder = instance.folder(root)
        declared = entry.release.secrets
        path = folder / contract.SECRET_FILE
        states = secret_states(declared, contract.stored_secrets(path.read_text()) if path.is_file() else [])
        if not states:
            continue
        found = True
        missing = [name for name, state in states.items() if state == MISSING]
        unused = [name for name, state in states.items() if state == UNUSED]
        label = f'{instance.app}/{instance.env}'
        if missing:
            report.warn(f'{label}: value missing for {", ".join(missing)}, so the app runs without it; set it in your own '
                        f'terminal: make app-secret APP={instance.app} ENV={instance.env} NAME={missing[0]}')
        else:
            report.ok(f'{label}: every secret its image needs has a value')
        if unused:
            report.info(f'{label}: {", ".join(unused)} set, but its image no longer asks for it')
    if not found:
        report.ok('no app declares secrets')


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
