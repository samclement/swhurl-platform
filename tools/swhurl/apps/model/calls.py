"""Calls between apps: a caller gets each callee's address and a token only that callee accepts; a
callee learns who may call it and admits them in its NetworkPolicy.

A pair is connected only when both sides agree: the caller's ``calls`` names the callee and the
callee's ``allows`` names the caller or the caller's team (docs/apps.md#calling-another-app). A call
to a web app without consent, a grant without a call and a valid call to an absent app render
nothing for that pair. The shared relation evaluator rejects invalid targets even without consent.
"""
from __future__ import annotations

from swhurl import interface
from swhurl.apps.calling import TOKENS_VOLUME, callee_address, tokens_volume
from swhurl.apps.model import relations
from swhurl.apps.model.draft import Draft, Entry, World, environment


def apply(draft: Draft, entry: Entry, world: World) -> None:
    app, env = draft.instance.app, draft.instance.env
    others = environment(world, env)
    callees = [r.target for r in relations.evaluate(draft.instance, world) if r.role == 'calls' and not r.pending]
    for callee in callees:
        found = others[callee]
        draft.env[interface.callee_variable(callee)] = callee_address(callee, found.release.port)
    draft.values['persistence'][TOKENS_VOLUME] = tokens_volume(callees)
    draft.callers = sorted(other.app for other in world if other.env == env
                           and any(r.role == 'calls' and r.target == app and not r.pending
                                   for r in relations.evaluate(other, world)))


def admit(draft: Draft) -> None:
    """Tell a callee who may call it (after its events, where the files have always had it)."""
    if draft.callers:
        draft.env[interface.CALLERS_VARIABLE] = ','.join(draft.callers)
