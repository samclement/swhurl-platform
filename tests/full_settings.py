"""A Settings with every field away from its default, for the tests that each field is classified on purpose."""
from dataclasses import fields, replace

from swhurl.apps.model.records import Allocations, Choices, Grants, Resources, Settings, Storage

FULL = Settings(Grants(exposure='public', calls=('orders',), allows=('shop', 'team:demo'), subscriptions=('orders.created',),
                       egress=('api.example.com:443',), exceptions='exposure=reviewed'),
                Choices(host='hello.example.com', issuer='letsencrypt-staging', replicas=2,
                        capacity=Resources('50m', '64Mi', '128Mi'), schedule='0 3 * * *'),
                Allocations(egress_port=30101, events_key='U' + 'A' * 55, database=Storage('64Mi', 'local-path-retain')))
PARTS = {'grants': Grants, 'choices': Choices, 'allocations': Allocations}
NAMES = {field.name: part for part, kind in PARTS.items() for field in fields(kind)}
DEFAULT = Settings()


def value(settings: Settings, name: str) -> object:
    return getattr(getattr(settings, NAMES[name]), name)


def varied(base: Settings = DEFAULT):
    """``(name, base with only that field at FULL's value)`` for every Grants, Choices and Allocations field."""
    for name, part in NAMES.items():
        yield name, replace(base, **{part: replace(getattr(base, part), **{name: value(FULL, name)})})
