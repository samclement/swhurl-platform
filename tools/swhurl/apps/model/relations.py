"""Pure call/event declarations and their same-environment meaning.

Absence of an app or call consent is pending, not access. Invalid declarations and present
unsupported targets are errors even without consent. Declarations also own removal dependencies.
"""
from __future__ import annotations

from dataclasses import dataclass, replace

from swhurl.apps.instance import Instance
from swhurl.apps.model.draft import Entry, RenderError, World
from swhurl.apps.model.records import NAME_RE, SUBSCRIPTION_RE


@dataclass(frozen=True)
class Relation:
    role: str  # calls or subscribes
    target: str
    event: str = ''
    pending: str = ''  # '' means connected after evaluation; otherwise the reason access is withheld


def declarations(each: Instance, entry: Entry) -> tuple[Relation, ...]:
    """All outgoing declarations, in record order, including those that cannot connect yet."""
    grants = entry.settings.grants
    found = []
    for role, names, pattern in (('calls', grants.calls, NAME_RE),
                                ('subscribes', grants.subscriptions, SUBSCRIPTION_RE)):
        if len(set(names)) != len(names):
            raise RenderError(f'{each}: duplicate {role}')
        for name in names:
            if not pattern.fullmatch(name):
                raise RenderError(f'{each}: malformed {role}: {name}')
            target, _, event = name.partition('.')
            if target == each.app:
                raise RenderError(f'{each}: an app does not declare {role} to itself')
            found.append(Relation(role, target, event))
    consumers = {}
    for relation in found:
        if relation.role == 'subscribes':
            name = f'{relation.target}-{relation.event}'
            if name in consumers:
                raise RenderError(f'{each}: Consumer name {name} collides for {consumers[name]} and '
                                  f'{relation.target}.{relation.event}')
            consumers[name] = f'{relation.target}.{relation.event}'
    return tuple(found)


def evaluate(each: Instance, world: World) -> tuple[Relation, ...]:
    """Resolve declarations against records, never live output. Pending relations grant nothing."""
    entry = world[each]
    found = []
    for relation in declarations(each, entry):
        target = world.get(Instance(relation.target, each.env))
        pending = ''
        if target is None:
            pending = f'waiting for {relation.target}/{each.env} to exist'
        elif relation.role == 'calls':
            if target.release.kind != 'web' or target.release.port is None:
                raise RenderError(f'{each} calls {relation.target}, a {target.release.kind}: it has no address to call')
            if not ({each.app, f'team:{entry.app.team}'} & set(target.settings.grants.allows)):
                pending = f'waiting for {relation.target} to allow it'
        elif relation.event not in target.release.publishes:
            raise RenderError(f'{each} subscribes to {relation.target}.{relation.event}, '
                              f'which {relation.target}/{each.env} does not publish')
        found.append(replace(relation, pending=pending))
    return tuple(found)
