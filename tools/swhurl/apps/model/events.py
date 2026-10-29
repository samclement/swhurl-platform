"""Events on NATS: every instance with a key signs in as itself; a publisher gets its stream, a
subscriber its consumers; the server's user list is generated from every instance."""
from __future__ import annotations

from swhurl import interface
from swhurl.apps import events
from swhurl.apps.model import relations
from swhurl.apps.model.draft import EVENTS_SECRET, Draft, Entry, RenderError, World


def keyed(entry: Entry) -> bool:
    return bool(entry.settings.allocations.events_key) and (EVENTS_SECRET in entry.encrypted or EVENTS_SECRET in entry.pending)


def apply(draft: Draft, entry: Entry, world: World) -> None:
    app, env = draft.instance.app, draft.instance.env
    publishes = entry.release.publishes
    subscribes = [(r.target, r.event) for r in relations.evaluate(draft.instance, world)
                  if r.role == 'subscribes' and not r.pending]
    if not keyed(entry):
        if publishes or entry.settings.grants.subscriptions:
            raise RenderError(f'{app}/{env} declares events but has no NATS key')
        return
    if publishes:
        draft.env[interface.PUBLISH_VARIABLE] = ','.join(publishes)
    if subscribes:
        draft.env[interface.SUBSCRIBE_VARIABLE] = ','.join(f'{publisher}.{event}' for publisher, event in subscribes)
    draft.env[interface.NKEY_VARIABLE] = entry.settings.allocations.events_key
    draft.env[interface.URL_VARIABLE] = events.URL
    draft.env_from(events.secret_name(app))
    draft.reload(events.secret_name(app))
    draft.depend(events.UNIT)
    draft.decrypt()
    draft.list_file(events.SECRET_FILE)
    if publishes or subscribes:
        draft.add_file(events.EVENTS_FILE, events.manifests(app, env, list(publishes), subscribes))
