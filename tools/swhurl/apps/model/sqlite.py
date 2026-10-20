"""SQLite: a retained volume holding the database file, one writer at a time, and a namespace that
outlives the unit so the claim does (destroy-data deletes it on purpose)."""
from __future__ import annotations

from swhurl.apps.contract import DATA_MOUNT, DATABASE_PATH_VARIABLE, SQLITE_PATH
from swhurl.apps.model.draft import Draft, Entry, RenderError

PRUNE = 'kustomize.toolkit.fluxcd.io/prune'


def apply(draft: Draft, entry: Entry) -> None:
    if entry.release.database != 'sqlite':
        return
    if entry.release.kind != 'web':
        raise RenderError(f'{draft.instance}: a {entry.release.kind} keeps no database')
    storage = entry.settings.allocations.database
    if storage is None:
        raise RenderError(f'{draft.instance} declares sqlite but has no database allocation')
    draft.env[DATABASE_PATH_VARIABLE] = SQLITE_PATH
    draft.values['persistence']['data'] = {'type': 'persistentVolumeClaim', 'storageClass': storage.storage_class,
                                           'accessMode': 'ReadWriteOnce', 'size': storage.size, 'retain': True,
                                           'globalMounts': [{'path': DATA_MOUNT}]}
    # One writer: stop the old pod before starting the new one; never run two (rule single-writer).
    draft.controller.setdefault('replicas', 1)
    draft.controller['strategy'] = 'Recreate'
    draft.annotate(draft.namespace, PRUNE, 'disabled')
