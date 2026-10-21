"""Calls between apps: a caller gets each callee's address and a token only that callee accepts; a
callee learns who may call it and admits them in its NetworkPolicy.

A pair is connected only when both sides agree: the caller's ``calls`` names the callee and the
callee's ``allows`` names the caller or the caller's team (docs/apps.md#calling-another-app). A call
without a grant, a grant without a call and a call to an app absent from the environment render
nothing for that pair; none of them is an error.
"""
from __future__ import annotations

from swhurl.apps.calling import CALLERS_VARIABLE, TOKENS_VOLUME, callee_address, callee_variable, tokens_volume
from swhurl.apps.model.draft import Draft, Entry, RenderError, World, environment


def admits(callee: Entry, caller: str, team: str) -> bool:
    """Whether ``callee``'s grants name the app ``caller`` or its team."""
    return caller in callee.settings.grants.allows or f'team:{team}' in callee.settings.grants.allows


def connected(app: str, entry: Entry, others: dict[str, Entry]) -> list[str]:
    """The apps ``app`` calls that are present in its environment and allow it, in its order."""
    return [callee for callee in entry.settings.grants.calls
            if callee != app and callee in others and admits(others[callee], app, entry.app.team)]


def apply(draft: Draft, entry: Entry, world: World) -> None:
    app, env = draft.instance.app, draft.instance.env
    others = environment(world, env)
    callees = connected(app, entry, others)
    for callee in callees:
        found = others[callee]
        if found.release.port is None:
            raise RenderError(f'{app}/{env} calls {callee}, a {found.release.kind}: it has no address to call')
        draft.env[callee_variable(callee)] = callee_address(callee, found.release.port)
    draft.values['persistence'][TOKENS_VOLUME] = tokens_volume(callees)
    draft.callers = sorted(other for other, each in others.items() if app in connected(other, each, others))


def admit(draft: Draft) -> None:
    """Tell a callee who may call it (after its events, where the files have always had it)."""
    if draft.callers:
        draft.env[CALLERS_VARIABLE] = ','.join(draft.callers)
