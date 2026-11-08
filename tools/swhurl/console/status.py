"""Pinned repository inputs and comparison facts into one Go status invocation per request."""
from __future__ import annotations

import json
import re

from swhurl.apps.instance import Instance
from swhurl.apps.state import State
from swhurl.console import cluster, observation, v2
from swhurl.console.actions import ActionError
from swhurl.console.github import changed_since
from swhurl.console.reading import Files
from swhurl.run import Runner

COMMIT = re.compile(r'(?:main@sha1:)?([0-9a-f]{40})\Z')


def comparisons(runner: Runner, files: Files, units: list[dict], revision: str) -> dict:
    pairs = set()
    for unit in units:
        literal = (unit.get('status') or {}).get('lastAppliedRevision', '')
        found = COMMIT.fullmatch(literal) if isinstance(literal, str) else None
        if found and found[1] != revision:
            pairs.add(found[1])
    compared = []
    for applied in sorted(pairs):
        paths, status = [], 'unavailable'
        if files.api:
            try:
                contained, changed = changed_since(runner, files.github, applied, revision)
                if contained and changed is not None:
                    paths, status = changed, 'complete'
            except (ActionError, ValueError, TypeError):
                pass
        elif files.local_root:
            result = runner.run(['git', 'merge-base', '--is-ancestor', applied, revision], cwd=files.local_root, check=False)
            if result.returncode == 0:
                result = runner.run(['git', 'diff', '--name-only', '--no-renames', '-z', applied, revision],
                                    cwd=files.local_root, check=False)
                if result.returncode == 0:
                    paths, status = [path for path in result.stdout.split('\0') if path], 'complete'
        compared.append({'from': applied, 'to': revision, 'status': status, 'paths': paths})
    return {'kind': 'v2-changes', 'version': 1, 'comparisons': compared}


def read(runner: Runner, files: Files, units: list[dict], *, in_cluster: bool,
         app: str = '', reviewed_digest: str = '') -> dict:
    """No live answer survives the Files scope. A submitted review uses single status, never a Python judge."""
    def invoke():
        revision, root = files.inputs()
        changes = root / 'changes.json'
        changes.write_text(json.dumps(comparisons(runner, files, units, revision)))
        args = ['status', '--records', v2.RECORDS, '--profile', v2.PROFILE, '--calls', v2.CALLS,
                '--egress', v2.EGRESS, '--revision', revision, '--changes', str(changes), '--repo-root', str(root),
                '--source', 'console', '--json']
        args += ['--app', app, '--env', 'staging', '--reviewed-digest', reviewed_digest] if app else ['--all']
        args += v2.credential(runner, root, in_cluster=in_cluster)
        result = v2.run(runner, root, args)
        decoded = observation.decode(result.stdout, revision, single=bool(app))
        if app and decoded['target']['namespace'] != f'{app}-staging':
            raise ActionError('Go status unavailable: wrong submitted-review target')
        return decoded
    return files.once('@status' if not app else '@promotion', invoke)


REMOVED = {'unit_and_release': 'removed from Git; live cleanup in progress',
           'unit': 'removed from Git; live cleanup in progress, its unit is still there',
           'release': 'removed from Git; live cleanup in progress, its HelmRelease is still there'}


def rows(document: dict) -> list[cluster.AppRow]:
    """The Go document as the pages show it: one row per instance it reports, and one per instance only the cluster
    still has, so an app removed from Git stays visible until its namespace goes."""
    found, teams = {}, {}
    for key, entry in document['instances'].items():
        instance = Instance.named(*key)
        if instance is None or entry['report']['target']['namespace'] != instance.namespace:
            raise ActionError('Go status unavailable: invalid instance identity')
        teams[instance.app] = entry['inventory']['team']
        found[key] = cluster.AppRow(instance, observation.state(entry['report']),
                                    entry['report']['live']['unit']['suspended'], entry['inventory'],
                                    entry['report']['currentness']['appliedRevision'], entry['report'], teams)
    for app, env, reason in document['removing']:
        instance = Instance.named(app, env)
        if instance is not None:
            found.setdefault((app, env), cluster.AppRow(instance, State('unknown', REMOVED[reason])))
    return sorted(found.values(), key=lambda row: (row.instance.app, row.instance.env))
