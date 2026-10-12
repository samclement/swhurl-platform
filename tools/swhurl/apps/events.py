"""Events between apps on NATS JetStream (docs/apps.md#events): names, permissions and manifests.

An app publishes events under its own name and subscribes to other apps' events. Everything here
is derived from two variables on each instance's HelmRelease, as calls are from their addresses:

    SWHURL_EVENTS_PUBLISH: created,cancelled            events this app publishes
    SWHURL_EVENTS_SUBSCRIBE: orders.created             <app>.<event> pairs it receives

On NATS, staging and production share one server and are kept apart by name and permission:

    subject   <env>.<app>.<event>          only <app> of <env> may publish it
    stream    <env>-<app>                  keeps the app's events for RETENTION, at most MAX_BYTES
    consumer  <subscriber>_<event>         on the publisher's stream: remembers what a subscriber has seen

An app signs in to NATS with its own nkey (apps/nkeys.py): the public key is a variable on its
HelmRelease and in the server's user list (platform/nats/users.yaml, generated here); the seed is
in the app's own encrypted Secret. The stream controller (NACK) creates streams and consumers from
the manifests in each instance (events.yaml), so a merged change needs no command.
"""
from __future__ import annotations

import re
from pathlib import Path

import yaml

from swhurl.apps import helmrelease

NAMESPACE = 'messaging'
UNIT = 'platform-nats'
URL = f'nats://nats.{NAMESPACE}.svc.cluster.local:4222'
USERS_FILE = Path('platform/nats/users.yaml')
CONTROLLER_KEY_FILE = Path('platform/nats/controller-nkey.pub')
"""The stream controller's public nkey; its seed is platform/nats/secret-controller.sops.yaml."""

PUBLISH_VARIABLE = 'SWHURL_EVENTS_PUBLISH'
SUBSCRIBE_VARIABLE = 'SWHURL_EVENTS_SUBSCRIBE'
URL_VARIABLE = 'SWHURL_EVENTS_URL'
NKEY_VARIABLE = 'SWHURL_EVENTS_NKEY'
SEED_VARIABLE = 'SWHURL_EVENTS_SEED'
EVENTS_FILE = 'events.yaml'
SECRET_FILE = 'events.sops.yaml'

RETENTION = '168h'  # seven days: an event stream is not a system of record, and is not backed up
MAX_BYTES = 64 * 2**20  # per stream; the oldest events go first
MAX_DELIVER = 5  # a subscriber that keeps failing an event stops receiving it
ACK_WAIT = '30s'
EVENT_RE = re.compile(r'^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$')
SUBSCRIPTION_RE = re.compile(r'^([a-z]([a-z0-9-]{0,38}[a-z0-9])?)\.([a-z]([a-z0-9-]{0,38}[a-z0-9])?)$')


def secret_name(app: str) -> str:
    return f'{app}-events'


def published(variables: dict) -> list[str]:
    return [name for name in str((variables or {}).get(PUBLISH_VARIABLE, '')).split(',') if name]


def subscribed(variables: dict) -> list[tuple[str, str]]:
    """``(publishing app, event)`` pairs."""
    pairs = [name.partition('.') for name in str((variables or {}).get(SUBSCRIBE_VARIABLE, '')).split(',') if name]
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


def user(nkey: str, env: str, app: str, publishes: list[str], subscribes: list[tuple[str, str]]) -> dict:
    """One NATS user: a publisher may publish anything under its own name, and an app may fetch and
    acknowledge exactly the events it subscribes to.

    The publish permission is the app's whole prefix, not its list of events: a new image can carry a
    new event before the platform has read its description (make app-offers), and an exact list would
    refuse it until then. The app's own helper holds it to the events its asyncapi.yaml describes.
    """
    allow = [subject(env, app, '>')] if publishes else []
    for publisher, event in subscribes:
        on = f'{stream(env, publisher)}.{consumer(app, event)}'
        allow += [f'$JS.API.CONSUMER.INFO.{on}', f'$JS.API.CONSUMER.MSG.NEXT.{on}', f'$JS.ACK.{on}.>']
        # The Java client (the Kotlin stack) reads the stream's description before it binds to its
        # consumer; the JavaScript client does not. Read-only, and of a stream whose events it may read.
        info = f'$JS.API.STREAM.INFO.{stream(env, publisher)}'
        if info not in allow:
            allow.append(info)
    return {'nkey': nkey, 'permissions': {'publish': {'allow': allow}, 'subscribe': {'allow': [f'{inbox(env, app)}.>']}}}


def controller_user(nkey: str) -> dict:
    """The stream controller: the JetStream API only."""
    return {'nkey': nkey, 'permissions': {'publish': {'allow': ['$JS.API.>']}, 'subscribe': {'allow': ['_INBOX.>']}}}


def users_configmap(users: list[dict]) -> str:
    """Helm values for the NATS release (its valuesFrom): the whole user list, generated.

    Helm replaces lists, so the controller's user is in this list too. The label makes Flux
    upgrade the release as soon as the list changes; NATS then reloads its configuration.
    """
    values = yaml.safe_dump({'config': {'merge': {'authorization': {'users': users}}}}, sort_keys=False,
                            default_flow_style=False)
    return ('# Generated by make app-events and make app-sync from the instances under apps/: do not edit.\n'
            '# Public keys and permissions only; each seed is in its app\'s own encrypted Secret.\n'
            'apiVersion: v1\nkind: ConfigMap\nmetadata:\n'
            f'  name: nats-users\n  namespace: {NAMESPACE}\n'
            '  labels:\n    reconcile.fluxcd.io/watch: Enabled\n'
            'data:\n  values.yaml: |\n' + ''.join(f'    {line}\n' for line in values.splitlines()))


def users_file(root: Path) -> tuple[Path, str] | None:
    """The NATS user list for every instance under ``root`` as it is on disk, with its path; None
    where this root has no NATS unit (the scratch roots of tests and template checks)."""
    key_file = root / CONTROLLER_KEY_FILE
    if not key_file.is_file():
        return None
    users = [controller_user(key_file.read_text().strip())]
    for env in ('staging', 'prod'):
        for path in sorted((root / 'apps').glob(f'*/{env}/helmrelease.yaml')):
            variables = helmrelease.variables(yaml.safe_load(path.read_text()))
            if variables.get(NKEY_VARIABLE) and (published(variables) or subscribed(variables)):
                users.append(user(str(variables[NKEY_VARIABLE]), env, path.parent.parent.name, published(variables),
                                  subscribed(variables)))
    return root / USERS_FILE, users_configmap(users)


def manifests(app: str, env: str, publishes: list[str], subscribes: list[tuple[str, str]]) -> list[dict]:
    """The instance's stream (if it publishes) and one durable consumer per subscription."""
    namespace = f'{app}-{env}'
    docs = []
    if publishes:
        docs.append({'apiVersion': 'jetstream.nats.io/v1beta2', 'kind': 'Stream',
                     'metadata': {'name': stream(env, app), 'namespace': namespace},
                     'spec': {'name': stream(env, app), 'subjects': [subject(env, app, '>')], 'storage': 'file',
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
