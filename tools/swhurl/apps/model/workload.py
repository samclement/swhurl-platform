"""The workload: namespace, HelmRelease, container, quota, NetworkPolicy, kustomization and Flux unit,
by kind (web, worker, job), and staging's image automation. Every other capability adds to what this starts."""
from __future__ import annotations

from swhurl.apps.calling import identity_variables
from swhurl.apps.contract import (
    APP,
    AUTO_DEPLOY_ENV,
    AUTO_DEPLOY_TAG_PATTERN,
    CHART,
    CHART_REPOSITORY,
    CHART_VERSION,
    ENVIRONMENT,
    EXCEPTIONS,
    EXPOSURE,
    FAIL_AFTER,
    IMAGE_AUTOMATION_FILE,
    JOB_DEADLINE_SECONDS,
    JOB_SCHEDULE,
    JOB_START_DEADLINE_SECONDS,
    JOB_TIME_ZONE,
    MANAGED,
    NETWORK_POLICY_FILE,
    QUOTA_FILE,
    STARTUP_PERIOD,
    STARTUP_SECONDS,
    TEAM,
    image_policy_name,
    network_policy,
    quantity,
    resource_quota,
)
from swhurl.apps.instance import Instance
from swhurl.apps.model.draft import Draft, Entry, Platform
from swhurl.apps.model.records import Resources

DEFAULT_RESOURCES = Resources('10m', '32Mi', '128Mi')
"""An image that declares no minimum gets these."""
BASE_UNIT = 'infra-base'
IMAGE_AUTOMATION_UNIT = 'platform-image-automation'


def resources(entry: Entry) -> dict:
    """Each of the image's minimum (or the default) and the instance's explicit choice, whichever is larger."""
    def pick(key: str) -> str:
        base = getattr(entry.release.resources, key) or getattr(DEFAULT_RESOURCES, key)
        chosen = getattr(entry.settings.capacity, key)
        return chosen if chosen and quantity(chosen) > quantity(base) else base
    return {'requests': {'cpu': pick('cpu'), 'memory': pick('memory')}, 'limits': {'memory': pick('memory_limit')}}


def image_automation(app: str, repository: str) -> list[dict]:
    """App-scoped Flux image resources: the policy whose report starts the Deploy workflow."""
    labels = {MANAGED: 'true', APP: app}
    return [
        {'apiVersion': 'image.toolkit.fluxcd.io/v1', 'kind': 'ImageRepository',
         'metadata': {'name': app, 'namespace': 'flux-system', 'labels': labels},
         # The app repository's image webhook starts the scan (apps/hooks.py); the interval is the
         # fallback for a missed delivery.
         'spec': {'image': repository, 'interval': '1h'}},
        {'apiVersion': 'image.toolkit.fluxcd.io/v1', 'kind': 'ImagePolicy',
         'metadata': {'name': image_policy_name(app), 'namespace': 'flux-system', 'labels': labels},
         'spec': {'imageRepositoryRef': {'name': app},
                  'filterTags': {'pattern': AUTO_DEPLOY_TAG_PATTERN, 'extract': '$run'},
                  'policy': {'numerical': {'order': 'asc'}},
                  # Tags are immutable, so re-reading the current tag's digest hourly is plenty; a new
                  # tag's digest is read when the repository scan finds it. Always requires an interval.
                  'digestReflectionPolicy': 'Always', 'interval': '1h'}},
    ]


def start(instance: Instance, entry: Entry, platform: Platform) -> Draft:
    app, env, release = instance.app, instance.env, entry.release
    web = release.kind == 'web'
    container: dict = {'image': release.image.document(), 'resources': resources(entry),
                       'securityContext': {'allowPrivilegeEscalation': False, 'readOnlyRootFilesystem': True,
                                           'capabilities': {'drop': ['ALL']}}}
    if release.command:
        container['command'] = list(release.command)
    container['env'] = identity_variables(app, env, web, platform.signing_keys)
    if web:
        def probe() -> dict:
            return {'enabled': True, 'custom': True, 'spec': {'httpGet': {'path': release.health_path, 'port': release.port}}}
        # Liveness waits until the app first answers, for up to STARTUP_SECONDS.
        startup = probe()
        startup['spec'] |= {'periodSeconds': STARTUP_PERIOD, 'failureThreshold': STARTUP_SECONDS // STARTUP_PERIOD}
        container['probes'] = {'readiness': probe(), 'liveness': probe(), 'startup': startup}
    controller: dict = {'containers': {'main': container}}
    if release.kind == 'job':
        # One pod per run, never two at once; a failed run is not retried (the next scheduled one runs).
        controller['type'] = 'cronjob'
        controller['cronjob'] = {'schedule': entry.settings.schedule or JOB_SCHEDULE, 'timeZone': JOB_TIME_ZONE,
                                 'concurrencyPolicy': 'Forbid', 'startingDeadlineSeconds': JOB_START_DEADLINE_SECONDS,
                                 'activeDeadlineSeconds': JOB_DEADLINE_SECONDS, 'backoffLimit': 0,
                                 'successfulJobsHistory': 1, 'failedJobsHistory': 1}
    if entry.settings.replicas is not None:
        controller['replicas'] = entry.settings.replicas
    values: dict = {
        'defaultPodOptions': {'automountServiceAccountToken': False,
                              'securityContext': {'runAsNonRoot': True, 'runAsUser': release.uid, 'runAsGroup': release.uid,
                                                  'fsGroup': release.uid, 'seccompProfile': {'type': 'RuntimeDefault'}}},
        'controllers': {'main': controller},
        'persistence': {'tmp': {'type': 'emptyDir', 'globalMounts': [{'path': '/tmp'}]}},
    }
    if web:
        values['service'] = {'main': {'controller': 'main', 'ports': {'http': {'port': release.port}}}}
    labels = {MANAGED: 'true', APP: app, ENVIRONMENT: env, EXPOSURE: entry.settings.exposure, TEAM: entry.app.team}
    namespace = {'apiVersion': 'v1', 'kind': 'Namespace', 'metadata': {'name': instance.namespace, 'labels': labels}}
    helm_release = {
        'apiVersion': 'helm.toolkit.fluxcd.io/v2', 'kind': 'HelmRelease',
        'metadata': {'name': app, 'namespace': instance.namespace},
        'spec': {'interval': '30m', 'timeout': FAIL_AFTER,
                 'chart': {'spec': {'chart': CHART, 'version': CHART_VERSION,
                                    'sourceRef': {'kind': 'HelmRepository', 'name': CHART_REPOSITORY,
                                                  'namespace': 'flux-system'},
                                    'interval': '30m'}},
                 'install': {'remediation': {'retries': 1}}, 'upgrade': {'remediation': {'retries': 1}},
                 'values': values},
    }
    unit = {'apiVersion': 'kustomize.toolkit.fluxcd.io/v1', 'kind': 'Kustomization',
            'metadata': {'name': instance.unit, 'namespace': 'flux-system'},
            'spec': {'dependsOn': [{'name': BASE_UNIT}], 'interval': '10m',
                     'sourceRef': {'kind': 'GitRepository', 'name': 'swhurl-platform'},
                     'path': f'./{(platform.base / app / env).as_posix()}',
                     # App unit: default MirrorPrune, so removing it from Git uninstalls the instance.
                     'prune': True, 'wait': True, 'timeout': FAIL_AFTER}}
    return Draft(instance, namespace, helm_release, unit, ['namespace.yaml', 'helmrelease.yaml', QUOTA_FILE, NETWORK_POLICY_FILE])


def automation(draft: Draft, entry: Entry, platform: Platform) -> None:
    """Staging follows the newest image the app publishes (the Deploy workflow writes it)."""
    if platform.auto_deploy and draft.instance.env == AUTO_DEPLOY_ENV:
        draft.add_file(IMAGE_AUTOMATION_FILE, image_automation(draft.instance.app, entry.release.image.repository))
        draft.depend(IMAGE_AUTOMATION_UNIT)


def finish(draft: Draft, entry: Entry) -> dict[str, list[dict]]:
    """The instance's files by name, beside its unit (``unit``)."""
    if entry.settings.exceptions:
        draft.annotate(draft.release, EXCEPTIONS, entry.settings.exceptions)
    app, namespace, port = draft.instance.app, draft.instance.namespace, entry.release.port
    files = {'namespace.yaml': [draft.namespace], 'helmrelease.yaml': [draft.release],
             QUOTA_FILE: [resource_quota(app, namespace)],
             NETWORK_POLICY_FILE: [network_policy(app, namespace, port if draft.routed else None, port, draft.callers)],
             **draft.files,
             'kustomization.yaml': [{'apiVersion': 'kustomize.config.k8s.io/v1beta1', 'kind': 'Kustomization',
                                     'resources': draft.resources}]}
    return files
