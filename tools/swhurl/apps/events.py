"""Events between apps on NATS JetStream (docs/apps.md#events): names, permissions and manifests.

An app publishes events under its own name and subscribes to other apps' events. Everything here
is rendered from Release.publishes and Settings.subscriptions into the instance variables:

    SWHURL_EVENTS_PUBLISH: created,cancelled            events this app publishes
    SWHURL_EVENTS_SUBSCRIBE: orders.created             <app>.<event> pairs it receives

On NATS, staging and production share one server and are kept apart by name and permission:

    subject   <env>.<app>.<event>          only <app> of <env> may publish it
    stream    <env>-<app>                  keeps the app's events for RETENTION, at most MAX_BYTES
    consumer  <subscriber>_<event>         on the publisher's stream: remembers what a subscriber has seen

Every instance signs in to NATS with its own nkey (apps/nkeys.py), whether or not it declares
events: the public key is a variable on its HelmRelease, and the server's user list is v2's output
(platform/nats/users.json, rendered from a snapshot by `swhurl-v2 users`); the seed is in the app's own
encrypted Secret. The same identity reads the instance's feature flag overrides (docs/apps.md#feature-flags):

    bucket    flags-<env>                  NATS key-value store, one per environment (platform/flags)
    key       <env>.<app>.<flag>           only <app> of <env> may watch it; only the console writes

The stream controller (NACK) creates streams and consumers from the manifests in each instance
(events.yaml), so a merged change needs no command.
"""
from __future__ import annotations

from pathlib import Path

from swhurl import interface
from swhurl.apps.instance import Instance

NAMESPACE = 'messaging'
UNIT = 'platform-nats'
URL = f'nats://nats.{NAMESPACE}.svc.cluster.local:4222'
CONTROLLER_KEY_FILE = Path('platform/nats/controller-nkey.pub')
"""The stream controller's public nkey; its seed is platform/nats/secret-controller.sops.yaml."""
CONSOLE_KEY_FILE = Path('platform/nats/console-nkey.pub')
"""The console's public nkey: it writes flag overrides. Its seed is platform/console/nats.sops.yaml."""
CONSOLE_INBOX = '_INBOX.console'

EVENTS_FILE = 'events.yaml'
SECRET_FILE = 'events.sops.yaml'

RETENTION = '168h'  # seven days: an event stream is not a system of record, and is not backed up
MAX_BYTES = 64 * 2**20  # per stream; the oldest events go first
MAX_DELIVER = 5  # a subscriber that keeps failing an event stops receiving it
ACK_WAIT = '30s'


def secret_name(app: str) -> str:
    return f'{app}-events'


def published(variables: dict) -> list[str]:
    return [name for name in str((variables or {}).get(interface.PUBLISH_VARIABLE, '')).split(',') if name]


def subscribed(variables: dict) -> list[tuple[str, str]]:
    """``(publishing app, event)`` pairs."""
    pairs = [name.partition('.') for name in str((variables or {}).get(interface.SUBSCRIBE_VARIABLE, '')).split(',') if name]
    return [(app, event) for app, _, event in pairs]


def stream(env: str, app: str) -> str:
    return f'{env}-{app}'


def subject(env: str, app: str, event: str) -> str:
    return f'{env}.{app}.{event}'


def consumer(subscriber: str, event: str) -> str:
    return f'{subscriber}_{event}'


def inbox(env: str, app: str) -> str:
    """Replies to an app's requests arrive under its own prefix, so no app can read another's."""
    return f'_INBOX.{env}.{app}'


def flags_bucket(env: str) -> str:
    return f'flags-{env}'


def has_nats(root: Path) -> bool:
    """Whether ``root`` has a NATS unit; the scratch roots of tests and template checks do not."""
    return (root / CONTROLLER_KEY_FILE).is_file()


def manifests(app: str, env: str, publishes: list[str], subscribes: list[tuple[str, str]]) -> list[dict]:
    """The instance's stream (if it publishes) and one durable consumer per subscription."""
    namespace = Instance(app, env).namespace
    docs = []
    if publishes:
        docs.append({'apiVersion': 'jetstream.nats.io/v1beta2', 'kind': 'Stream',
                     'metadata': {'name': stream(env, app), 'namespace': namespace},
                     'spec': {'name': stream(env, app), 'subjects': [subject(env, app, '>')], 'storage': 'file',
                              'preventDelete': True,  # stopping publication must not erase retained messages
                              'replicas': 1, 'retention': 'limits', 'discard': 'old', 'maxAge': RETENTION,
                              'maxBytes': MAX_BYTES}})
    for publisher, event in subscribes:
        name = consumer(app, event)
        docs.append({'apiVersion': 'jetstream.nats.io/v1beta2', 'kind': 'Consumer',
                     'metadata': {'name': f'{publisher}-{event}', 'namespace': namespace},
                     'spec': {'streamName': stream(env, publisher), 'durableName': name,
                              'filterSubject': subject(env, publisher, event), 'ackPolicy': 'explicit',
                              'deliverPolicy': 'all', 'ackWait': ACK_WAIT, 'maxDeliver': MAX_DELIVER}})
    return docs
