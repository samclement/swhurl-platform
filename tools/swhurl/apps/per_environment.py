"""What may differ between an app's environments, and what a promotion does with each difference.

An app's staging and production manifests are the same except for the settings and files listed
here. Each row says where the value comes from and what a promotion does with staging's value:

    origin   declared     a person or a command sets it in each environment (replicas, the apps it calls)
             derived      the platform works it out from the instance and its neighbours (make app-sync)
             generated    made for this environment or read for the image it runs (a key, what the image offers)
    first    carry        production is created with staging's value
             strip        production is created without it
             regenerate   production is created with its own value
    later    stays        a later promotion leaves production's value alone
             image        it moves with the image
             offers       it moves with the documents the image offers (edit.staging_offers)
             held         it moves with them too, but a person reviews the change

The env-drift policy rule reads it (what is not compared); promotion renders production from the
app model's records (apps/promotion.py, edit.promote), which the first and later columns describe.
A setting that staging gets before production (anything read for the image it runs) needs a row
here: without one, env-drift fails for every app that has a production instance.

Plain data, and only modules the offline policy check may import (no ruamel.yaml).
"""
from __future__ import annotations

from dataclasses import dataclass

from swhurl.apps import contract, egress, events, helmrelease, offers
from swhurl.apps.calling import (
    API_VARIABLE,
    CALLERS_VARIABLE,
    DESCRIPTION_VARIABLE,
    ENVIRONMENT_VARIABLE,
    TOKENS_VOLUME,
)
from swhurl.apps.model.records import RECORD_FILES

DECLARED, DERIVED, GENERATED = 'declared', 'derived', 'generated'
CARRY, STRIP, REGENERATE = 'carry', 'strip', 'regenerate'
STAYS, WITH_IMAGE, WITH_OFFERS, HELD = 'stays', 'image', 'offers', 'held'
VARIABLE, ANNOTATION = 'variable', 'annotation'  # where a promotion finds it: the container's env, the release's annotations
PRODUCTION = 'prod'


@dataclass(frozen=True)
class Setting:
    """One key of an instance's manifests, wherever it appears in them."""
    key: str
    origin: str
    first: str
    note: str
    later: str = STAYS
    where: str = ''
    prefix: bool = False  # every key that starts with this
    names_only: bool = False  # only where it holds a list of names; a list of mappings is compared entry by entry
    with_exposure: bool = False  # only between environments whose exposure differs


@dataclass(frozen=True)
class File:
    """One file of an instance's folder, and its line in the folder's kustomization.yaml."""
    name: str
    origin: str
    first: str
    note: str


SETTINGS = (
    Setting('namespace', DERIVED, REGENERATE, 'the instance itself: <app>-<env>'),
    Setting(contract.ENVIRONMENT, DERIVED, REGENERATE, 'the label that names the environment'),
    Setting(ENVIRONMENT_VARIABLE, DERIVED, REGENERATE, 'the app is told which environment it is', where=VARIABLE),
    Setting('host', DECLARED, REGENERATE, 'the generated production address, or --host for a public app'),
    Setting('hosts', DECLARED, REGENERATE, 'the same host under TLS', names_only=True),
    Setting('cert-manager.io/cluster-issuer', DECLARED, CARRY, 'which certificate authority issues the route'),
    Setting('tag', DECLARED, CARRY, 'staging follows new images; production gets the promoted one', later=WITH_IMAGE),
    Setting('digest', DECLARED, CARRY, 'as the tag', later=WITH_IMAGE),
    Setting('platform.swhurl.com/render', GENERATED, REGENERATE,
            'hash of this instance and environment: recomputed by render, never copied', where=ANNOTATION),
    Setting(DESCRIPTION_VARIABLE, GENERATED, CARRY, "what the app says it does: read from the image's swhurl.yaml",
            later=WITH_IMAGE, where=VARIABLE),
    Setting('probes', GENERATED, CARRY, "the image's health path (its port must still match: the Service is compared)",
            later=WITH_IMAGE),
    *(Setting(key, GENERATED, CARRY, 'the user the image runs as', later=WITH_IMAGE)
      for key in ('runAsUser', 'runAsGroup', 'fsGroup')),
    Setting('OTEL_', GENERATED, CARRY, 'telemetry: the image has an OpenTelemetry SDK', later=WITH_IMAGE,
            where=VARIABLE, prefix=True),
    Setting(contract.OTLP_HOST_IP, GENERATED, CARRY, 'the node the collector runs on, for telemetry', later=WITH_IMAGE,
            where=VARIABLE),
    Setting('replicas', DECLARED, CARRY, 'make app-scale'),
    Setting('schedule', DECLARED, CARRY, "when a job runs (make app-schedule): production starts with staging's"),
    Setting('resources', DECLARED, CARRY, 'make app-scale'),
    Setting('SWHURL_SERVICE_', DECLARED, CARRY, 'the apps it calls (make app-calls); each needs a production instance first',
            where=VARIABLE, prefix=True),
    Setting(TOKENS_VOLUME, DERIVED, CARRY, 'one token per declared call: follows the calls'),
    Setting(CALLERS_VARIABLE, DERIVED, STRIP, "who may call it: make app-sync works it out from production's callers",
            where=VARIABLE),
    Setting(events.PUBLISH_VARIABLE, DECLARED, STRIP, "what it publishes: set again from the image's event document, when "
            'it has one', later=HELD, where=VARIABLE),
    Setting(events.SUBSCRIBE_VARIABLE, DECLARED, STRIP, 'what it receives: declared in production (make app-events ENV=prod)',
            where=VARIABLE),
    Setting(events.URL_VARIABLE, DERIVED, STRIP, "follows from the instance's NATS key", where=VARIABLE),
    Setting(events.NKEY_VARIABLE, GENERATED, STRIP, "the environment's own NATS key", where=VARIABLE),
    Setting(egress.HTTPS_VARIABLE, GENERATED, STRIP, "the environment's own port on the egress proxy (make app-sync)",
            where=VARIABLE),
    Setting(egress.HTTP_VARIABLE, GENERATED, STRIP, 'as HTTPS_PROXY', where=VARIABLE),
    Setting(egress.JAVA_VARIABLE, GENERATED, STRIP, 'the same port for the JVM', where=VARIABLE),
    Setting(egress.ANNOTATION, DECLARED, STRIP, 'the hosts outside the cluster it may reach: granted in production '
            'separately (make app-egress ENV=prod), so a promotion never widens it', where=ANNOTATION),
    Setting(contract.RELOAD, DERIVED, CARRY, "restart on a Secret change; staging's events Secret is taken out of it"),
    Setting(offers.ANNOTATION, GENERATED, STRIP, "the commit the image's documents were read at: carried with them",
            later=WITH_OFFERS, where=ANNOTATION),
    Setting(API_VARIABLE, GENERATED, CARRY, 'where the image serves its API description', later=WITH_OFFERS, where=VARIABLE),
    # Exposure may differ (a signed-in staging preview of a public app). The routes of two environments
    # are compared only when their exposure is the same; each is still checked against its own.
    Setting('ingress', DECLARED, CARRY, 'the route (make app-expose)', with_exposure=True),
    Setting(contract.EXPOSURE, DECLARED, CARRY, 'the label that names the exposure', with_exposure=True),
)

FILES = (
    File(contract.IMAGE_AUTOMATION_FILE, DECLARED, STRIP, 'automatic deploys: staging only'),
    File(contract.NETWORK_POLICY_FILE, DERIVED, REGENERATE, "follows from the environment's exposure and callers"),
    File(events.EVENTS_FILE, DERIVED, STRIP, 'the stream and consumers that follow from declaring events'),
    File(events.SECRET_FILE, GENERATED, STRIP, "the environment's own NATS key; the release's reference to it goes too"),
    File('secret.sops.yaml', GENERATED, REGENERATE, 'the same keys with placeholder values: credentials are never copied'),
)
ENCRYPTED = '.sops.yaml'  # no encrypted file is compared, whatever its name
NOT_CARRIED = frozenset(file.name for file in FILES if file.first == STRIP)
"""Files, and lines in kustomization.yaml, that staging may have and a new production does not."""


def may_differ(key: object, value: object = None, *, exposure_differs: bool = False) -> bool:
    """Whether ``key`` (holding ``value``) is a setting environments may differ in."""
    for setting in SETTINGS:
        if not (str(key).startswith(setting.key) if setting.prefix else key == setting.key):
            continue
        if setting.with_exposure and not exposure_differs:
            continue
        if setting.names_only and not all(isinstance(item, str) for item in value or []):
            continue
        return True
    return False


def compared(name: str) -> bool:
    """Whether a file of an instance's folder is compared between environments: not its records, which
    say what each environment is rendered from (the rendered files are compared)."""
    return not name.endswith(ENCRYPTED) and name not in {file.name for file in FILES} | RECORD_FILES


def without_reference(release: dict, secret: str) -> None:
    """Take a Secret out of a release: its envFrom entry and its name in the Reloader annotation."""
    main, workload = helmrelease.container(release), helmrelease.workload(release)
    reference = {'secretRef': {'name': secret}}
    if reference in (main.get('envFrom') or []):
        main['envFrom'].remove(reference)
        if not main['envFrom']:
            del main['envFrom']
    annotations = workload.get('annotations') or {}
    if contract.RELOAD in annotations:
        reloads = [name for name in str(annotations[contract.RELOAD]).split(',') if name and name != secret]
        if reloads:
            annotations[contract.RELOAD] = ','.join(reloads)
        else:
            del annotations[contract.RELOAD]
            if not annotations:
                del workload['annotations']
