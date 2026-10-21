"""Pure changes to app records: no files, network calls or subprocesses."""
from __future__ import annotations

from dataclasses import replace

from swhurl.apps import declaration
from swhurl.apps.contract import RETAINED_STORAGE_CLASS, over_quota, quantity
from swhurl.apps.errors import EditError
from swhurl.apps.model import workload
from swhurl.apps.model.draft import Entry
from swhurl.apps.model.records import Image, Release, Resources, Storage

RESOURCE_FIELDS = (('cpu', 'requests', 'cpu'), ('memory', 'requests', 'memory'), ('memory_limit', 'limits', 'memory'))
"""Each resource the image may need at least (declaration.RESOURCES) and where it is in the container."""


def changed_list(what: str, current: list[str], replace: list[str] | None, add: list[str], remove: list[str]) -> list[str] | None:
    """A list after a change: replaced whole, or with names added and removed (as app-calls does); None if untouched."""
    if replace is not None:
        if add or remove:
            raise EditError(f'--{what} replaces the list: do not give --add-{what} or --remove-{what} with it')
        return replace
    if not add and not remove:
        return None
    present = [name for name in add if name in current]
    absent = [name for name in remove if name not in current]
    if present or absent:
        raise EditError('; '.join(filter(None, [present and f'already there: {", ".join(present)}',
                                                absent and f'not there to remove: {", ".join(absent)}'])))
    return [name for name in current if name not in remove] + add


def with_needs(entry: Entry, image: Image, declared: declaration.Declaration, app: str) -> tuple[Entry, list[str]]:
    """The records with a new image and what it needs: its port and health path (web), the user it runs as,
    telemetry, a new database's volume, the secret names it reads (a new one arrives without a value),
    resources at its minimum or the instance's capacity, whichever is larger, its description and its stack. Returns them and what changed.
    A need the instance cannot take is refused (EditError)."""
    needs, old = declared.needs, entry.release
    if old.kind != needs.kind:
        raise EditError(f'kind: {needs.kind}, but the instance is a {"web app" if old.kind == "web" else old.kind}: '
                        'web, worker or job is fixed at creation (create a new app for the other kind)')
    web = needs.kind == 'web'
    release = replace(old, image=image, port=needs.port if web else None, health_path=needs.health_path if web else '',
                      uid=needs.uid, telemetry=needs.otlp, database=needs.database or '', secrets=tuple(needs.secrets),
                      description=declared.description, stack=declared.stack,
                      resources=Resources(*(needs.minimum.get(field, '') for field, _, _ in RESOURCE_FIELDS)))
    return take_release(entry, release, app, needs.database_size, 'swhurl.yaml')


def take_release(entry: Entry, release: Release, app: str, database_size: str, said: str) -> tuple[Entry, list[str]]:
    """``entry`` running ``release``, an image and what it needs (a new staging image's, or staging's for a
    promotion), with what that changes in the instance's Settings: a new database gets a retained volume of
    ``database_size`` and one writer. Returns the records and what changed. Refused (EditError, naming
    ``said``, where the need was declared): a database the instance would lose, a volume of another size, and a
    new uid for files already on a volume."""
    old, settings = entry.release, entry.settings
    changes: list[str] = []
    if release.port != old.port:
        changes.append(f'port {old.port} -> {release.port}')
    if release.health_path != old.health_path:
        changes.append(f'health path {old.health_path} -> {release.health_path}')
    if release.uid != old.uid:
        if old.database:
            raise EditError(f'uid {release.uid}: {app} keeps a volume whose files belong to uid {old.uid}, '
                            'and a new image does not change who owns them (keep the uid, or move the data by hand)')
        changes.append(f'uid {old.uid} -> {release.uid}')
    if release.telemetry != old.telemetry:
        changes.append(f'telemetry {"otlp" if release.telemetry else "none"}')
    allocations, replicas = settings.allocations, settings.replicas
    if release.database and not old.database:
        # As app-new makes it: a retained volume, one writer, stop-first updates (rule single-writer).
        allocations = replace(allocations, database=Storage(database_size, RETAINED_STORAGE_CLASS))
        replicas = None
        changes.append(f'database {release.database} on a new {database_size} volume')
    elif old.database and not release.database:
        raise EditError(f'database: none, but {app} keeps its database volume, and a push never deletes data: keep '
                        f'database in {said}, or remove the data on purpose (make destroy-data, docs/operations.md#lifecycle)')
    elif release.database and allocations.database and allocations.database.size != database_size:
        raise EditError(f'databaseSize {database_size}, but the volume was created with {allocations.database.size}, '
                        'and a volume keeps its size: declare that size again')
    if release.secrets != old.secrets:
        changes.append('secret names ' + ', '.join([f'+{n}' for n in release.secrets if n not in old.secrets]
                                                   + [f'-{n}' for n in old.secrets if n not in release.secrets]))
    if release.description != old.description:
        changes.append('description')
    # The instance's own capacity stays: resources render as the larger of it and the image's minimum.
    found = replace(entry, release=release, settings=replace(settings, replicas=replicas, allocations=allocations))
    changes += at_minimum(entry, found)
    return found, changes


def at_minimum(before: Entry, after: Entry) -> list[str]:
    """What the image's minimum (``after``'s release) changes in the instance's resources, which render as
    the larger of it and the instance's capacity. Refused when the instance cannot run it."""
    running, resources = workload.resources(before), workload.resources(after)
    changes = [f'{section}.{key} {running[section][key]} -> {resources[section][key]} (the image\'s minimum)'
               for _, section, key in RESOURCE_FIELDS if resources[section][key] != running[section][key]]
    request, limit = resources['requests']['memory'], resources['limits']['memory']
    if quantity(request) > quantity(limit):
        raise EditError(f'resources: a memory request of {request} is above the memory limit {limit}; declare memoryLimit too')
    count = 1 if after.settings.replicas is None else after.settings.replicas
    if over := over_quota([(count + (not after.release.database), [resources])]):
        minimum = {field: value for field, _, _ in RESOURCE_FIELDS if (value := getattr(after.release.resources, field))}
        raise EditError(f"resources: the image's minimum {minimum} at {count} replica(s) "
                        f'exceeds the namespace quota for {", ".join(over)}')
    return changes
