"""Calls between apps: a caller gets each callee's address and a token only that callee accepts; a
callee learns who may call it and admits them in its NetworkPolicy."""
from __future__ import annotations

from swhurl.apps.calling import CALLERS_VARIABLE, TOKENS_VOLUME, callee_address, callee_variable, tokens_volume
from swhurl.apps.model.draft import Draft, Entry, RenderError, World, environment


def apply(draft: Draft, entry: Entry, world: World) -> None:
    app, env = draft.instance.app, draft.instance.env
    others = environment(world, env)
    for callee in entry.settings.calls:
        found = others.get(callee)
        if found is None:
            raise RenderError(f'{app}/{env} calls {callee}, which has no {env} instance')
        if found.release.port is None:
            raise RenderError(f'{app}/{env} calls {callee}, a {found.release.kind}: it has no address to call')
        draft.env[callee_variable(callee)] = callee_address(callee, found.release.port)
    draft.values['persistence'][TOKENS_VOLUME] = tokens_volume(list(entry.settings.calls))
    draft.callers = sorted(other for other, each in others.items() if app in each.settings.calls)
    if draft.callers:
        draft.env[CALLERS_VARIABLE] = ','.join(draft.callers)
