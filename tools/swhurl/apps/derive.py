"""What follows from an app instance's declarations, written back into Git.

An instance declares little by hand: its exposure, the apps it calls (SWHURL_SERVICE_<APP>_URL)
and the events it publishes and receives. Everything else is derived here, from the instance
itself or from the other instances of its environment, and rewritten whenever a command changes a
declaration (edit.py) or on make app-sync:

    identity            SWHURL_APP, SWHURL_ENV; for a web app the token issuer and signing keys
    calls               the caller's token files and the callee's address; the callee's allowed
                        callers and NetworkPolicy
    events              the NATS address and Secret reference, the stream and consumers, the unit's
                        dependency, and the NATS user list (platform/nats/users.yaml)
    isolation           the quota and NetworkPolicy files and their kustomization entries

The strict accessors at the top read a HelmRelease for editing in place (comments kept) and refuse
shapes they do not understand; reading only is helmrelease.py.
"""
from __future__ import annotations

from pathlib import Path

from swhurl.apps import events, helmrelease
from swhurl.apps.calling import (
    CALLERS_VARIABLE,
    TOKENS_VOLUME,
    allowed_callers,
    callee_address,
    callee_variable,
    declared_calls,
    identity_variables,
    signing_keys,
    tokens_volume,
)
from swhurl.apps.contract import (
    ENVIRONMENTS,
    EXPOSURE,
)
from swhurl.apps.new import (
    NAME_RE,
    dump,
    isolation_files,
    watch_namespace,
)
from swhurl.apps.yaml_file import EditError, YamlFile, mapping


def instance_dir(root: Path, app: str, env: str) -> Path:
    if not NAME_RE.match(app) or env not in ENVIRONMENTS:
        raise EditError(f'no instance {app}/{env}: APP must be a DNS label and ENV one of {", ".join(ENVIRONMENTS)}')
    path = root / 'apps' / app / env
    if not (path / 'helmrelease.yaml').is_file():
        raise EditError(f'no instance {app}/{env} (missing {path.relative_to(root)}/helmrelease.yaml)')
    return path

def load_release(instance: Path) -> dict:
    return YamlFile(instance / 'helmrelease.yaml').data

def values(release: dict) -> dict:
    return mapping(mapping(release, 'spec'), 'values')

def controller(release: dict) -> dict:
    return mapping(mapping(values(release), 'controllers'), 'main')

def container(release: dict) -> dict:
    return mapping(mapping(controller(release), 'containers'), 'main')

def isolation_edits(instance: Path, app: str, env: str, exposure: str, release: dict, *,
                    listed: tuple[str, ...] = (), unlisted: tuple[str, ...] = ()) -> list[tuple[Path, str]]:
    """The instance's quota and NetworkPolicy for ``exposure``, and its kustomization listing them
    (and ``listed``, and no longer ``unlisted``: the event files, which sync_env writes or deletes)."""
    port = service_port(values(release))
    callers = allowed_callers(helmrelease.variables(release))
    files = isolation_files(app, f'{app}-{env}', exposure, port, callers)
    edits = [(instance / name, text) for name, text in files.items()]
    kustomization = YamlFile(instance / 'kustomization.yaml')
    resources = kustomization.data.get('resources')
    if not isinstance(resources, list):
        raise EditError('kustomization.yaml: expected a resources list')
    missing = [name for name in (*files, *listed) if name not in resources]
    stale = [name for name in unlisted if name in resources]
    resources.extend(missing)
    for name in stale:
        resources.remove(name)
    if missing or stale:
        edits.append((kustomization.path, kustomization.render()))
    return edits

RELOAD = 'secret.reloader.stakater.com/reload'

def event_edits(root: Path, instance: Path, app: str, env: str, release: dict, publishers: dict[str, list[str]],
                ) -> tuple[list[tuple[Path, str]], tuple[str, ...], tuple[str, ...], dict | None]:
    """What follows from the events an instance declares (SWHURL_EVENTS_PUBLISH / _SUBSCRIBE):
    ``(file edits, files to list, files to delete and unlist, its NATS user or None)``.

    In the release (edited in place): the NATS address, the reference to the Secret holding the
    app's seed, and Reloader restarting the app when that Secret changes. Beside it: the stream and
    consumers (events.yaml), and the unit waiting for platform-nats and decrypting.
    ``publishers`` maps each app of this environment to the events it publishes.
    """
    main, workload = container(release), controller(release)
    variables = mapping(main, 'env', create=True)
    publishes, subscribes = events.published(variables), events.subscribed(variables)
    for publisher, event in subscribes:
        if event not in publishers.get(publisher, []):
            raise EditError(f'{app}/{env} subscribes to {publisher}.{event}, which {publisher}/{env} does not publish')
    secret = events.secret_name(app)
    reference = {'secretRef': {'name': secret}}
    refs = main.get('envFrom')
    annotations = workload.get('annotations') or {}
    reloads = [name for name in str(annotations.get(RELOAD, '')).split(',') if name]
    unit_path = root / 'clusters/home' / f'app-{app}-{env}.yaml'
    if not (publishes or subscribes) and not unit_path.is_file():
        unit_path = None  # an instance without its unit (a test tree) has nothing to undo there
    unit = YamlFile(unit_path) if unit_path else None
    unit_spec = mapping(unit.data, 'spec') if unit else {}
    dependencies = unit_spec.get('dependsOn') or []
    edits: list[tuple[Path, str]] = []
    if publishes or subscribes:
        if not variables.get(events.NKEY_VARIABLE) or not (instance / events.SECRET_FILE).is_file():
            raise EditError(f'{app}/{env} declares events but has no NATS key: set them with make app-events, which creates it')
        if variables.get(events.URL_VARIABLE) != events.URL:
            variables[events.URL_VARIABLE] = events.URL
        if refs is None:
            main['envFrom'] = [reference]
        elif reference not in refs:
            refs.append(reference)
        if secret not in reloads:
            mapping(workload, 'annotations', create=True)[RELOAD] = ','.join([*reloads, secret])
        if not any(d.get('name') == events.UNIT for d in dependencies):
            unit_spec.setdefault('dependsOn', dependencies).append({'name': events.UNIT})
        if 'decryption' not in unit_spec:
            unit_spec['decryption'] = {'provider': 'sops', 'secretRef': {'name': 'sops-age'}}
        edits.append((instance / events.EVENTS_FILE, dump(events.manifests(app, env, publishes, subscribes))))
        user = events.user(str(variables[events.NKEY_VARIABLE]), env, app, publishes, subscribes)
        listed, unlisted = (events.SECRET_FILE, events.EVENTS_FILE), ()
    else:
        for key in (events.URL_VARIABLE, events.NKEY_VARIABLE):
            variables.pop(key, None)
        if refs is not None and reference in refs:
            refs.remove(reference)
            if not refs:
                del main['envFrom']
        if secret in reloads:
            reloads.remove(secret)
            if reloads:
                annotations[RELOAD] = ','.join(reloads)
            else:
                del annotations[RELOAD]
                if not annotations:
                    del workload['annotations']
        for index in reversed(range(len(dependencies))):
            if dependencies[index].get('name') == events.UNIT:
                del dependencies[index]
        if 'decryption' in unit_spec and not (instance / 'secret.sops.yaml').is_file():
            del unit_spec['decryption']
        user, listed, unlisted = None, (), (events.SECRET_FILE, events.EVENTS_FILE)
    if unit:
        edits.append((unit.path, unit.render()))
    return edits, listed, unlisted, user

def users_edit(root: Path) -> list[tuple[Path, str]]:
    """The NATS user list as a file edit, from the instances as they are on disk."""
    found = events.users_file(root)
    return [found] if found else []

def service_port(app_values: dict) -> int | None:
    """The app's HTTP port, or None for a worker."""
    if 'service' not in app_values:
        return None
    port = mapping(mapping(mapping(mapping(app_values, 'service'), 'main'), 'ports'), 'http').get('port')
    if not isinstance(port, int):
        raise EditError('service.main.ports.http.port: expected a port number; edit custom services by hand')
    return port

def instances_in(root: Path, env: str) -> list[str]:
    return sorted(p.parent.parent.name for p in (root / 'apps').glob(f'*/{env}/helmrelease.yaml'))

def sync_env(root: Path, env: str) -> list[Path]:
    """Rewrite what the platform derives in every instance of one environment; return the files changed.

    Derived from each instance itself: who it is (SWHURL_APP, SWHURL_ENV), and for a web app the token
    issuer and signing keys. Derived from what callers declare (their SWHURL_SERVICE_<APP>_URL
    variables): the caller's token files and the address's port; the callee's allowed callers and
    its NetworkPolicy. A declared call to an app with no instance in this environment is an error.
    """
    releases = {app: YamlFile(instance_dir(root, app, env) / 'helmrelease.yaml') for app in instances_in(root, env)}
    ports = {app: service_port(values(doc.data)) for app, doc in releases.items()}
    calls = {app: declared_calls(helmrelease.variables(doc.data)) for app, doc in releases.items()}
    for app, callees in calls.items():
        for callee in callees:
            if callee not in releases:
                raise EditError(f'{app}/{env} calls {callee}, which has no {env} instance')
            if ports[callee] is None:
                raise EditError(f'{app}/{env} calls {callee}, a worker: it has no address to call')
    keys = signing_keys(root)
    publishers = {app: events.published(helmrelease.variables(doc.data)) for app, doc in releases.items()}
    edits: list[tuple[Path, str]] = []
    deletions: list[Path] = []
    for app, doc in releases.items():
        instance = doc.path.parent
        app_values, main = values(doc.data), container(doc.data)
        variables = mapping(main, 'env', create=True)
        for index, (key, value) in enumerate(identity_variables(app, env, ports[app] is not None, keys).items()):
            if key not in variables:
                variables.insert(index, key, value)
            elif variables[key] != value:
                variables[key] = value
        for callee in calls[app]:
            if variables[callee_variable(callee)] != callee_address(callee, ports[callee]):
                variables[callee_variable(callee)] = callee_address(callee, ports[callee])
        callers = sorted(caller for caller, callees in calls.items() if app in callees)
        if callers and variables.get(CALLERS_VARIABLE) != ','.join(callers):
            variables[CALLERS_VARIABLE] = ','.join(callers)
        elif not callers:
            variables.pop(CALLERS_VARIABLE, None)
        persistence = mapping(app_values, 'persistence', create=True)
        if calls[app] and persistence.get(TOKENS_VOLUME) != tokens_volume(calls[app]):
            persistence[TOKENS_VOLUME] = tokens_volume(calls[app])
        elif not calls[app]:
            persistence.pop(TOKENS_VOLUME, None)
        exposure = mapping(mapping(YamlFile(instance / 'namespace.yaml').data, 'metadata'), 'labels').get(EXPOSURE)
        event_files, listed, unlisted, user = event_edits(root, instance, app, env, doc.data, publishers)
        if user:
            watch_namespace(root, f'{app}-{env}')
        edits.append((doc.path, doc.render()))
        edits += event_files
        edits += isolation_edits(instance, app, env, exposure, doc.data, listed=listed, unlisted=unlisted)
        deletions += [instance / name for name in unlisted if (instance / name).exists()]
    changed = [(path, text) for path, text in edits if not path.exists() or path.read_text() != text]
    for path, text in changed:
        path.write_text(text)
    for path in deletions:
        path.unlink()
    users = [(path, text) for path, text in users_edit(root) if not path.exists() or path.read_text() != text]
    for path, text in users:  # after the instances are written: it reads every environment from disk
        path.write_text(text)
    return [path for path, _ in changed] + deletions + [path for path, _ in users]

def sync(root: Path) -> Path | None:
    changed = [path for env in ENVIRONMENTS for path in sync_env(root, env)]
    for path in changed:
        print(f'[OK] rewrote {path.relative_to(root)}')
    if not changed:
        print('[OK] every instance already has what the platform derives')
    return None

def subscribers_of(root: Path, app: str, env: str) -> list[str]:
    """The other instances of ``env`` that subscribe to an event of ``app``."""
    found = []
    for other in instances_in(root, env):
        release = YamlFile(root / 'apps' / other / env / 'helmrelease.yaml').data
        if other != app and any(publisher == app for publisher, _ in events.subscribed(helmrelease.variables(release))):
            found.append(other)
    return found

def callers_of(root: Path, app: str, env: str) -> list[str]:
    """The other instances of ``env`` that declare a call to ``app``."""
    found = []
    for other in instances_in(root, env):
        release = YamlFile(root / 'apps' / other / env / 'helmrelease.yaml').data
        if other != app and app in declared_calls(helmrelease.variables(release)):
            found.append(other)
    return found
