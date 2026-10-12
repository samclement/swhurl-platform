"""Reading Kubernetes objects: what every area asks of one the same way."""
from __future__ import annotations

import datetime as dt

from swhurl.run import Runner


def ready_condition(obj: dict) -> tuple[str, str]:
    for condition in (obj.get('status') or {}).get('conditions') or []:
        if condition.get('type') == 'Ready':
            return condition.get('status', 'Unknown'), condition.get('message', '')
    return 'Unknown', 'no Ready condition'


def cronjob_run(runner: Runner, namespace: str, name: str, now: dt.datetime | None = None, *, minutes: int = 5) -> str:
    """Whether a CronJob is ``suspended``, ``stale`` (no successful Job in the last ``minutes``) or ``fresh``.
    Raises CommandError, KeyError, ValueError or TypeError when it cannot be read."""
    now = now or dt.datetime.now(dt.UTC)
    job = runner.json(['kubectl', '-n', namespace, 'get', 'cronjob', name, '-o', 'json'])
    last = (job.get('status') or {}).get('lastSuccessfulTime', '')
    taken = dt.datetime.fromisoformat(last.replace('Z', '+00:00')) if last else None
    if job['spec'].get('suspend'):
        return 'suspended'
    return 'stale' if taken is None or now - taken > dt.timedelta(minutes=minutes) else 'fresh'

