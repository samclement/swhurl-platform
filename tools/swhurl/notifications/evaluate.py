"""Discover app targets and evaluate lifecycle events and health incidents."""
from __future__ import annotations

import copy
import datetime as dt
import re

from swhurl.apps import ops
from swhurl.run import Runner
from swhurl.verify import ready_condition

from .state import empty_state

APP_PATH = re.compile(r'^\./(?:tests/fixtures/apps/)?apps/([a-z][a-z0-9-]*)/(staging|prod)$')
GRACE = 300
CONSOLE_GRACE = 600
RECOVERY = 120
REMINDER = 3600
HISTORY = 7200
MAX_STATE_BYTES = 700_000
SUCCESS = {'InstallSucceeded': 'deployed', 'UpgradeSucceeded': 'deployed',
           'RollbackSucceeded': 'rolled back', 'UninstallSucceeded': 'uninstalled'}
FAILURES = {'InstallFailed', 'UpgradeFailed', 'RollbackFailed', 'UninstallFailed', 'ReconciliationFailed',
            'BuildFailed', 'ValidationFailed', 'ArtifactFailed', 'PruneFailed'}



def timestamp(value: str | None) -> float:
    return dt.datetime.fromisoformat(value.replace('Z', '+00:00')).timestamp() if value else 0


def snapshot(runner: Runner) -> dict:
    commands = {
        'units': ['-n', 'flux-system', 'get', 'kustomizations.kustomize.toolkit.fluxcd.io'],
        'releases': ['get', 'helmreleases.helm.toolkit.fluxcd.io', '--all-namespaces'],
        'workloads': ['get', 'deployments,statefulsets,daemonsets', '--all-namespaces'],
        'pods': ['get', 'pods', '--all-namespaces'],
        'events': ['get', 'events', '--all-namespaces'],
    }
    return {key: runner.json(['kubectl', *args, '-o', 'json'])['items']
            for key, args in commands.items()}


def object_key(obj: dict) -> str:
    meta = obj['metadata']
    return f"{meta.get('namespace', '')}/{meta['name']}"


def targets(data: dict) -> dict:
    found = {}
    for unit in data['units']:
        if unit['metadata'].get('deletionTimestamp'):
            continue
        path = (unit.get('spec') or {}).get('path', '')
        match = APP_PATH.fullmatch(path)
        if match:
            app, env = match.groups()
            key, title = f'{app}-{env}/{app}', f'{app}/{env}'
        elif unit['metadata']['name'] == 'platform-console' and path == './platform/console':
            key = 'console/console'
            title = 'console'
        else:
            continue
        found[key] = {'title': title, 'unit': unit['metadata']['name'], 'namespace': key.split('/')[0],
                      'name': key.split('/')[1], 'console': title == 'console', 'unit_object': unit}
    return found


def enqueue(state: dict, key: str, title: str, message: str, *, high: bool, click: str, now: float) -> None:
    if key in state['seen'] or any(n['key'] == key for n in state['pending']):
        return
    state['pending'].append({'key': key, 'title': title, 'message': message, 'priority': 4 if high else 3,
                             'tags': ['warning' if high else 'white_check_mark'], 'click': click, 'queued': now})


def link(base: str, entry: dict, *, removed: bool = False) -> str:
    if entry['console']:
        return f'{base}/platform#units'
    return f'{base}/activity' if removed else f"{base}/apps/{entry['title']}"


def incident(state: dict, record: dict, fault: str | None, *, title: str, detail: str,
             grace: int, now: float, click: str) -> None:
    # None is unknown: do not advance recovery or reset a fault's first-observed time.
    if fault is None:
        record.pop('healthy_since', None)
        return
    if fault:
        record.pop('healthy_since', None)
        record.setdefault('since', now)
        record['fault'] = fault
        if now - record['since'] >= grace and now - record.get('last_notice', -REMINDER) >= REMINDER:
            minutes = int((now - record['since']) // 60)
            enqueue(state, f"incident:{title}:{record['since']}:{now}", f'{title} {fault}',
                    f'{detail}. Observed for {minutes} minutes.', high=True, click=click, now=now)
            record['last_notice'] = now
            record['notified'] = True
    elif record.get('notified'):
        record.setdefault('healthy_since', now)
        if now - record['healthy_since'] >= RECOVERY:
            enqueue(state, f"recovery:{title}:{record['since']}", f'{title} recovered',
                    f'Healthy/current for 2 minutes; incident lasted {int((now - record["since"]) // 60)} minutes.',
                    high=False, click=click, now=now)
            record.clear()
    else:
        record.clear()


def health(entry: dict, release: dict | None, data: dict) -> tuple[str | None, str]:
    if release is None:
        if (entry['unit_object'].get('spec') or {}).get('suspend'):
            return None, 'No release yet and deployment reconciliation is suspended'
        return 'deployment stalled', 'HelmRelease has not been created'
    if release['metadata'].get('deletionTimestamp'):
        return None, 'Uninstall is in progress'
    spec = release.get('spec') or {}
    desired_replicas = spec.get('values', {}).get('controllers', {}).get('main', {}).get('replicas', 1)
    # Deliberate stop clears/silences health incidents; it is not a recovery.
    if desired_replicas == 0:
        return 'stopped', ''
    selector = 'app.kubernetes.io/instance'

    def owned(obj):
        return (obj['metadata'].get('namespace') == entry['namespace']
                and (obj['metadata'].get('labels') or {}).get(selector) == entry['name']
                and (obj['metadata'].get('labels') or {}).get('app.kubernetes.io/controller', 'main') == 'main')
    workloads = [w for w in data['workloads'] if owned(w)]
    pods = [p for p in data['pods'] if owned(p) and not p['metadata'].get('deletionTimestamp')]
    replicas = ops.replicas({'items': workloads})
    if not replicas or any(r.wanted is None for r in replicas):
        return 'unhealthy', 'Desired workload is missing or replica status is unavailable'
    if any(r.ready < r.wanted for r in replicas):
        counts = ', '.join(f'{r.workload} {r.ready}/{r.wanted} Ready' for r in replicas)
        reasons = sorted({s.get('state', {}).get('waiting', {}).get('reason', '')
                          for p in pods for s in p.get('status', {}).get('containerStatuses', [])} - {''})
        return 'unhealthy', counts + (f'; {", ".join(reasons)}' if reasons else '')
    if spec.get('suspend') or (entry['unit_object'].get('spec') or {}).get('suspend'):
        return 'suspended', 'Healthy workload; deployment reconciliation is suspended'
    status = release.get('status') or {}
    conditions = status.get('conditions', [])
    if (any(c.get('type') == 'Remediated' and c.get('status') == 'True'
            and c.get('reason') == 'RollbackSucceeded'
            and c.get('observedGeneration', release['metadata'].get('generation')) == release['metadata'].get('generation')
            for c in conditions)
            and not any(c.get('type') == 'Reconciling' and c.get('status') == 'True' for c in conditions)):
        return '', 'Previous working version is healthy after rollback; requested upgrade failed'
    generation = release['metadata'].get('generation', 0)
    ready = next((c for c in status.get('conditions', []) if c.get('type') == 'Ready'), {})
    if ready.get('status') != 'True' or ready.get('observedGeneration', -1) != generation:
        return 'deployment stalled', f'HelmRelease is not current/Ready ({ready.get("reason", "Unknown")})'
    # Ignore repository-wide Git revision changes; only a pending unit or a mismatching image stalls.
    unit_ready = ready_condition(entry['unit_object'])[0]
    image = ops.desired_image(release)
    running = ops.running_images({'items': pods})
    if '@' in image and (not running or any(not im.endswith('@' + image.split('@', 1)[1]) for im in running)):
        # An automatic rollback deliberately restores the old healthy image. The rollback event tells the operator.
        if ready.get('reason') != 'RollbackSucceeded':
            return 'deployment stalled', 'Running image differs from the requested digest'
    if unit_ready != 'True':
        return 'deployment stalled', 'Flux unit is blocked or has not finished applying the request'
    return '', 'Workload is Ready and deployment is current'


def evaluate(previous: dict | None, data: dict, now: float, base: str) -> dict:
    state = copy.deepcopy(previous) if previous is not None else empty_state(now)
    state['seen'] = {k: v for k, v in state['seen'].items() if v > now}
    current = targets(data)
    releases = {object_key(r): r for r in data['releases']}
    for key, entry in current.items():
        record = state['instances'].setdefault(key, {'incident': {}})
        record.update({k: v for k, v in entry.items() if k != 'unit_object'})
        record['last_active'] = now
        release = releases.get(key)
        if release:
            if record.get('uid') and record['uid'] != release['metadata'].get('uid', ''):
                record['incident'].clear()
                record.pop('uninstalled', None)
                record.pop('failed_actions', None)
            record['uid'] = release['metadata'].get('uid', '')
            record['generation'] = release['metadata'].get('generation', 0)
            record['retained'] = bool(release.get('spec', {}).get('values', {}).get('persistence', {}).get('data'))
        fault, detail = health(entry, release, data)
        if fault == 'stopped' or (fault == 'suspended' and record['incident'].get('fault') != 'unhealthy'):
            record['incident'].clear()
        else:
            incident(state, record['incident'], '' if fault == 'suspended' else fault,
                     title=entry['title'], detail=detail,
                     grace=CONSOLE_GRACE if entry['console'] else GRACE, now=now, click=link(base, entry))
    # Keep identity after removal so a later Helm uninstall event still has an app/environment title.
    for key, entry in list(state['instances'].items()):
        if key not in current:
            entry['incident'].clear()
            if key not in releases and entry.get('uninstalled') and now - entry['last_active'] > HISTORY:
                del state['instances'][key]

    events = sorted(data['events'], key=lambda e: timestamp(e.get('lastTimestamp') or e.get('eventTime')
                                                          or e['metadata'].get('creationTimestamp')))
    units = {v['unit']: k for k, v in state['instances'].items()}
    for event in events:
        obj = event.get('involvedObject') or {}
        key = f"{obj.get('namespace', '')}/{obj.get('name', '')}"
        kind, reason = obj.get('kind'), event.get('reason', '')
        if kind == 'Kustomization' and obj.get('namespace') == 'flux-system':
            key = units.get(obj.get('name'), '')
        elif kind != 'HelmRelease':
            continue
        entry = state['instances'].get(key)
        if not entry or (kind == 'HelmRelease' and entry.get('uid') and obj.get('uid') != entry['uid']):
            continue
        when = timestamp(event.get('lastTimestamp') or event.get('eventTime')
                         or event['metadata'].get('creationTimestamp'))
        if when <= state['started'] or now - when > HISTORY:
            continue
        action = SUCCESS.get(reason) if kind == 'HelmRelease' else None
        if action is None and reason not in FAILURES:
            continue
        if action == 'deployed' and key in current:
            fault, _detail = health(current[key], releases.get(key), data)
            if fault not in {'', 'suspended'}:
                continue  # a custom release may disable Helm's wait; still require readiness evidence
        controller = event.get('reportingComponent') or (event.get('source') or {}).get('component', '')
        expected = 'helm-controller' if kind == 'HelmRelease' else 'kustomize-controller'
        if controller != expected:
            continue
        uid = obj.get('uid') or key
        # Errors retry as the same action; successes have a distinct Kubernetes event occurrence.
        attempt = (event['metadata'].get('annotations') or {}).get('helm.toolkit.fluxcd.io/token',
                                                                 event['metadata']['uid'])
        notice_key = (f"failure:{uid}:{attempt}:{reason}" if not action else
                      f"event:{event['metadata']['uid']}:{event.get('count', 1)}:{when}")
        if not action and notice_key in entry.get('failed_actions', []):
            continue
        if action == 'uninstalled' and entry.get('uninstalled') == uid:
            continue
        title = f"{entry['title']} {action or ('uninstall failed' if reason == 'UninstallFailed' else 'deployment failed')}"
        message = f'Flux {reason} for {key}.'
        chart = (event['metadata'].get('annotations') or {}).get('helm.toolkit.fluxcd.io/revision')
        if chart:
            message += f' Chart revision: {chart}.'
        release = releases.get(key)
        if release and reason in {'InstallSucceeded', 'UpgradeSucceeded'}:
            condition = next((c for c in release.get('status', {}).get('conditions', [])
                              if c.get('type') == 'Ready'), {})
            if condition.get('message') == event.get('message'):
                message += f' Requested image: {ops.desired_image(release)}.'
        if action == 'uninstalled' and entry.get('retained'):
            message += ' Storage is retained; data has not been deleted.'
        if action == 'rolled back':
            message += ' The requested upgrade failed; Helm restored the previous release.'
            state['pending'] = [p for p in state['pending'] if p['title'] != f"{entry['title']} deployment failed"]
        enqueue(state, notice_key, title, message, high=not action or action == 'rolled back',
                click=link(base, entry, removed=action == 'uninstalled'), now=now)
        if action == 'uninstalled':
            entry['uninstalled'] = uid
        if not action or action == 'rolled back':
            # One combined incident instead of another immediate unhealthy alarm for the failed action.
            record = entry['incident']
            record.setdefault('since', now)
            record.update(notified=True, last_notice=now)
            if not action:
                entry['failed_actions'] = [*entry.get('failed_actions', []), notice_key][-16:]
    for key, entry in state['instances'].items():
        # Namespace deletion can remove its Events before the next scan. A previously
        # observed managed release disappearing after its unit is removed is also a
        # completion proof: Helm's finalizer normally holds the release until uninstall.
        remaining = any(w['metadata'].get('namespace') == entry['namespace']
                        and (w['metadata'].get('labels') or {}).get('app.kubernetes.io/instance') == entry['name']
                        for w in data['workloads'])
        if (key not in current and key not in releases and not remaining
                and entry.get('uid') and not entry.get('uninstalled')):
            message = f'Managed HelmRelease {key} has finished removal.'
            if entry.get('retained'):
                message += ' Storage is retained; data has not been deleted.'
            enqueue(state, f"removed:{entry['uid']}", f"{entry['title']} uninstalled", message,
                    high=False, click=link(base, entry, removed=True), now=now)
            entry['uninstalled'] = entry['uid']
    state['last_success'] = now
    incident(state, state['monitor'], '', title='notification monitor', detail='',
             grace=GRACE, now=now, click=f'{base}/platform')
    return state


