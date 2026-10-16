"""Live checks of the apps: what Git declares for the instances is what the cluster has.

Each takes a Runner and a Report and only reads; ``verify.CHECKS`` lists them (make verify-platform).
"""
from __future__ import annotations

import json
from pathlib import Path

import yaml

from swhurl import ROOT
from swhurl.apps import calling, contract, events, helmrelease, hooks
from swhurl.apps.instance import INSTANCE_ROOTS, instances
from swhurl.kube import ready_condition
from swhurl.report import Report
from swhurl.run import CommandError, Runner


def check_image_automation(runner: Runner, report: Report, root: Path = ROOT) -> None:
    """Automatic staging deploys: every app ImagePolicy is Ready, and staging runs the image it picked
    (the Deploy workflow writes it: docs/apps.md#deploy-a-new-image)."""
    report.section('Image Automation')
    try:
        policies = runner.json(['kubectl', '-n', 'flux-system', 'get', 'imagepolicies.image.toolkit.fluxcd.io',
                                '-l', contract.APP, '-o', 'json'])['items']
    except (CommandError, KeyError, TypeError):
        report.bad("could not read the apps' ImagePolicies")
        return
    for policy in sorted(policies, key=lambda p: p['metadata']['name']):
        name = policy['metadata']['name']
        status, message = ready_condition(policy)
        if status != 'True':
            report.bad(f'ImagePolicy {name} is not Ready: {message}')
            continue
        latest = ((policy.get('status') or {}).get('latestRef') or {}).get('tag', '')
        app = (policy['metadata'].get('labels') or {}).get(contract.APP, '')
        release = root / INSTANCE_ROOTS[0] / app / contract.AUTO_DEPLOY_ENV / 'helmrelease.yaml'
        running = str(helmrelease.image(yaml.safe_load(release.read_text())).get('tag', '')) if release.is_file() else ''
        if running == latest:
            report.ok(f'{name}: newest image {latest}, deployed')
        else:
            report.warn(f'{name}: newest image {latest}, staging has {running or "no instance"} in Git '
                        '(the Deploy workflow runs within the hour, or held it: see its last run)')


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
        report.bad('could not read app namespaces, ResourceQuotas or NetworkPolicies')
        return
    known = contract.teams(root)
    live = {n['metadata']['name']: n['metadata'].get('labels') or {} for n in namespaces}
    complete = 0
    for instance in instances(root / INSTANCE_ROOTS[0]):
        app, name = instance.app, instance.namespace
        if name not in live:
            report.bad(f'{name}: the namespace does not exist')
            continue
        labels = live[name]
        problems = []
        if labels.get(contract.TEAM) not in known:
            problems.append(f'team {labels.get(contract.TEAM)!r} is not in {contract.TEAMS_FILE}')
        if not any(q['metadata']['namespace'] == name and (q.get('spec') or {}).get('hard') == contract.QUOTA
                   for q in quotas):
            problems.append('no platform ResourceQuota')
        selector = {'matchLabels': {'app.kubernetes.io/instance': app}}
        if not any(p['metadata']['namespace'] == name and (p.get('spec') or {}).get('podSelector') == selector
                   for p in policies):
            problems.append("no NetworkPolicy on the app's pods")
        if problems:
            report.bad(f'{name}: ' + '; '.join(problems))
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
        report.bad('could not read the cluster\'s token issuer or signing keys')
        return
    path = root / calling.SIGNING_KEYS_FILE
    if not path.is_file():
        report.bad(f'{calling.SIGNING_KEYS_FILE} is missing: make platform-signing-keys')
    elif json.loads(path.read_text()) != live:
        report.bad(f'{calling.SIGNING_KEYS_FILE} is not the cluster\'s signing key set: apps refuse every call between '
                   'them until you run make platform-signing-keys, commit and push')
    else:
        report.ok(f'{calling.SIGNING_KEYS_FILE} holds the cluster\'s {len(live.get("keys") or [])} signing key(s)')
    if issuer == calling.ISSUER:
        report.ok(f'token issuer is {issuer}')
    else:
        report.bad(f'token issuer is {issuer}, but apps are told {calling.ISSUER} (apps/calling.py ISSUER)')


def check_events(runner: Runner, report: Report, root: Path = ROOT) -> None:
    """NATS and its stream controller run, and every stream and consumer the instances declare is Ready in NATS."""
    report.section('Events')
    try:
        server = runner.json(['kubectl', '-n', events.NAMESPACE, 'get', 'statefulset', 'nats', '-o', 'json'])
        controller = runner.json(['kubectl', '-n', events.NAMESPACE, 'get', 'deployment', 'nats-controller', '-o', 'json'])
        declared = runner.json(['kubectl', 'get', 'streams.jetstream.nats.io,consumers.jetstream.nats.io',
                                '--all-namespaces', '-o', 'json'])['items']
    except (CommandError, KeyError, TypeError):
        report.bad('could not read NATS, its controller, or the streams and consumers')
        return
    for name, workload in (('NATS', server), ('the stream controller', controller)):
        ready = ((workload or {}).get('status') or {}).get('readyReplicas', 0)
        (report.ok if ready else report.bad)(f'{name} is running' if ready else f'{name} is not running: no events are '
                                             'published or delivered (make reconcile UNIT=platform-nats)')
    waiting = []
    for item in declared:
        status, message = ready_condition(item)
        if status != 'True':
            waiting.append(f"{item['kind']} {item['metadata']['namespace']}/{item['metadata']['name']}: {message or 'not Ready'}")
    for line in waiting:
        report.bad(line)
    if declared and not waiting:
        report.ok(f'{len(declared)} declared stream(s) and consumer(s) exist in NATS')
    wanted = events.users_file(root)
    if wanted and (not wanted[0].is_file() or wanted[0].read_text() != wanted[1]):
        report.bad(f'{events.USERS_FILE} is not what the instances declare: make app-sync, commit and push')


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
    for app, repository in hooks.auto_deploy_repositories(root).items():
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
