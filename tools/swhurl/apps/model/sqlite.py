"""SQLite: a retained volume holding the database file, one writer at a time, and a namespace that
outlives the unit so the claim does (destroy-data deletes it on purpose)."""
from __future__ import annotations

from swhurl import interface
from swhurl.apps.model.draft import Draft, Entry, RenderError


def apply(draft: Draft, entry: Entry) -> None:
    if entry.release.database != 'sqlite':
        return
    if entry.release.kind != 'web':
        raise RenderError(f'{draft.instance}: a {entry.release.kind} keeps no database')
    storage = entry.settings.allocations.database
    if storage is None:
        raise RenderError(f'{draft.instance} declares sqlite but has no database allocation')
    draft.env[interface.DATABASE_PATH_VARIABLE] = interface.SQLITE_PATH
    draft.values['persistence']['data'] = {'type': 'persistentVolumeClaim', 'storageClass': storage.storage_class,
                                           'accessMode': 'ReadWriteOnce', 'size': storage.size, 'retain': True,
                                           'globalMounts': [{'path': interface.DATA_MOUNT}]}
    # One writer: stop the old pod before starting the new one; never run two (rule single-writer).
    draft.controller.setdefault('replicas', 1)
    draft.controller['strategy'] = 'Recreate'
    draft.annotate(draft.namespace, interface.PRUNE, 'disabled')
