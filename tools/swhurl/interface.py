"""Names the platform provides to apps, not capability selection or rendering.

Standard library only: importing this catalogue reads no configuration. ENTRIES maps each name
(or documented family) to its surface, meaning and availability; docs/apps.md is checked against it.
Optional reader overrides are inputs, not environment variables emitted by the renderers.
Recovery and test labels use label(), but are not app capabilities.
"""
import re

LABEL_DOMAIN = 'platform.swhurl.com'


def label(name: str) -> str:
    """``platform.swhurl.com/<name>``."""
    return f'{LABEL_DOMAIN}/{name}'


APP_VARIABLE = 'SWHURL_APP'
ENVIRONMENT_VARIABLE = 'SWHURL_ENV'
ISSUER_VARIABLE = 'SWHURL_AUTH_ISSUER'
KEYS_VARIABLE = 'SWHURL_AUTH_KEYS'
CALLERS_VARIABLE = 'SWHURL_ALLOWED_CALLERS'
DESCRIPTION_VARIABLE = 'SWHURL_DESCRIPTION'
NOTIFY_VARIABLE = 'SWHURL_NOTIFY_URL'
NOTIFY_TOKEN_VARIABLE = 'SWHURL_NOTIFY_TOKEN_FILE'
SERVICE_VARIABLE = 'SWHURL_SERVICE_<APP>_URL'
SERVICE_VARIABLE_RE = re.compile(r'^SWHURL_SERVICE_([A-Z0-9_]+)_URL$')
PUBLISH_VARIABLE = 'SWHURL_EVENTS_PUBLISH'
SUBSCRIBE_VARIABLE = 'SWHURL_EVENTS_SUBSCRIBE'
URL_VARIABLE = 'SWHURL_EVENTS_URL'
NKEY_VARIABLE = 'SWHURL_EVENTS_NKEY'
SEED_VARIABLE = 'SWHURL_EVENTS_SEED'
HTTPS_VARIABLE, HTTP_VARIABLE, NO_PROXY_VARIABLE = 'HTTPS_PROXY', 'HTTP_PROXY', 'NO_PROXY'
NODE_VARIABLE, JAVA_VARIABLE = 'NODE_USE_ENV_PROXY', 'JAVA_TOOL_OPTIONS'
OTLP_HOST_IP = 'HOST_IP'
OTLP_ENDPOINT_VARIABLE = 'OTEL_EXPORTER_OTLP_ENDPOINT'
OTLP_PROTOCOL_VARIABLE = 'OTEL_EXPORTER_OTLP_PROTOCOL'
OTLP_SERVICE_VARIABLE = 'OTEL_SERVICE_NAME'
DATABASE_PATH_VARIABLE = 'DATABASE_PATH'
DATA_MOUNT = '/data'
SQLITE_PATH = f'{DATA_MOUNT}/app.db'
TOKENS_MOUNT = '/var/run/swhurl/tokens'
MANAGED = label('managed')
APP = label('app')
ENVIRONMENT = label('environment')
EXPOSURE = label('exposure')
TEAM = label('team')
EXCEPTIONS = label('policy-exceptions')
SECRET_NAMES = label('secrets')
EGRESS_ANNOTATION = label('egress')
OFFERS_ANNOTATION = label('offers')
API_ANNOTATION = label('api-description')
ALERTS_ANNOTATION = label('alerts')
RENDER_ANNOTATION = label('render')
RELOAD = 'secret.reloader.stakater.com/reload'
PRUNE = 'kustomize.toolkit.fluxcd.io/prune'
# env beats envFrom; retain the ordering in secret-name refusal messages.
RESERVED_VARIABLE_NAMES = (OTLP_HOST_IP, DATABASE_PATH_VARIABLE, HTTP_VARIABLE, HTTPS_VARIABLE,
                           NO_PROXY_VARIABLE, NODE_VARIABLE, JAVA_VARIABLE)
RESERVED_VARIABLE_PREFIXES = ('OTEL_', 'SWHURL_', 'KUBERNETES_')


def callee_variable(app: str) -> str:
    """orders-api -> SWHURL_SERVICE_ORDERS_API_URL."""
    return SERVICE_VARIABLE.replace('<APP>', app.upper().replace('-', '_'))


ENTRIES = {
    APP_VARIABLE: ('env', 'App name', 'Every instance'),
    ENVIRONMENT_VARIABLE: ('env', 'Environment name', 'Every instance'),
    NOTIFY_VARIABLE: ('env', 'Notify service address', 'Every instance'),
    NOTIFY_TOKEN_VARIABLE: ('env override', f'Notify token file override; defaults to {TOKENS_MOUNT}/notify/token', 'Optional input to the Python notify reader; not injected'),
    ISSUER_VARIABLE: ('env', 'Token issuer', 'Web instances'),
    KEYS_VARIABLE: ('env', 'Public token-signing keys', 'Web instances'),
    CALLERS_VARIABLE: ('env', 'Accepted callers, comma-separated', 'At least one connected caller'),
    DESCRIPTION_VARIABLE: ('env', 'Image description', 'Nonempty release description'),
    SERVICE_VARIABLE: ('env family', 'Callee URL; uppercase app name with hyphens changed to underscores', 'Mutually granted call to an existing web instance'),
    PUBLISH_VARIABLE: ('env', 'Published events, comma-separated', 'Keyed instance with publications'),
    SUBSCRIBE_VARIABLE: ('env', 'Subscriptions, comma-separated', 'Keyed instance with subscriptions'),
    URL_VARIABLE: ('env', 'NATS address', 'Complete NATS identity'),
    NKEY_VARIABLE: ('env', 'Public NATS key', 'Complete NATS identity'),
    SEED_VARIABLE: ('Secret env', 'Private NATS seed via envFrom', 'Complete NATS identity'),
    OTLP_HOST_IP: ('env', 'Node IP from status.hostIP', 'Telemetry or egress proxy'),
    OTLP_ENDPOINT_VARIABLE: ('env', 'Node-local OTLP HTTP endpoint', 'Telemetry enabled'),
    OTLP_PROTOCOL_VARIABLE: ('env', 'OTLP transport protocol', 'Telemetry enabled'),
    OTLP_SERVICE_VARIABLE: ('env', 'Telemetry service name', 'Telemetry enabled'),
    HTTP_VARIABLE: ('env', 'HTTP proxy URL', 'Platform has egress proxy'),
    HTTPS_VARIABLE: ('env', 'HTTPS proxy URL', 'Platform has egress proxy'),
    NO_PROXY_VARIABLE: ('env', 'Addresses bypassing the proxy', 'Platform has egress proxy'),
    NODE_VARIABLE: ('env', 'Enable Node environment proxy', 'Platform has egress proxy'),
    JAVA_VARIABLE: ('env', 'JVM proxy system properties', 'Platform has egress proxy'),
    DATABASE_PATH_VARIABLE: ('env', 'SQLite database filename', 'SQLite database'),
    DATA_MOUNT: ('path', 'Retained data mount', 'SQLite database'),
    SQLITE_PATH: ('path', 'SQLite database file', 'SQLite database'),
    f'{TOKENS_MOUNT}/<audience>/token': ('path family', 'Audience-bound token; notify or connected callee', 'Every instance has notify; calls add audiences'),
    MANAGED: ('label', 'Platform-managed marker', 'Namespace and image automation'),
    APP: ('label', 'App name', 'Namespace and image automation'),
    ENVIRONMENT: ('label', 'Environment name', 'Namespace'),
    EXPOSURE: ('label', 'Exposure grant', 'Namespace'),
    TEAM: ('label', 'Owning team', 'Namespace'),
    EXCEPTIONS: ('annotation', 'Reviewed policy exceptions', 'HelmRelease when nonempty'),
    SECRET_NAMES: ('annotation', 'Required secret names, comma-separated', 'HelmRelease when declared'),
    EGRESS_ANNOTATION: ('annotation', 'Outbound host:port grants', 'HelmRelease when nonempty and proxy exists'),
    OFFERS_ANNOTATION: ('annotation', 'Commit supplying offered documents', 'HelmRelease when recorded'),
    API_ANNOTATION: ('annotation', 'Served API description path', 'Web HelmRelease with API path'),
    ALERTS_ANNOTATION: ('annotation', 'Alert overrides; absent means defaults', 'Namespace when configured'),
    RENDER_ANNOTATION: ('annotation', 'Fingerprint of intended generated output', 'Every HelmRelease'),
    RELOAD: ('annotation', 'Secrets whose change restarts the controller', 'Controller with referenced app or events Secret'),
    PRUNE: ('annotation', 'Retain namespace and its data after removal', 'SQLite Namespace'),
}
