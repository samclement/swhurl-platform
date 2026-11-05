"""Names the platform provides to apps, not capability selection or rendering.

Standard library only: importing this catalogue reads no file; entries() reads v2/contract/names.json on first use.
entries() maps each name (or documented family) to its surface, meaning and availability; docs/apps.md is checked
against it.
Optional reader overrides are inputs, not environment variables emitted by the renderers.
Recovery and test labels use label(), but are not app capabilities. unlisted() is the one test of a name
against the catalogue here, applied by check-repo to what v2 emits; check-templates applies swhurl-v2 names to what
each app type reads.
"""
import functools
import json
import re
from pathlib import Path

LABEL_DOMAIN = 'platform.swhurl.com'


def label(name: str) -> str:
    """``platform.swhurl.com/<name>``."""
    return f'{LABEL_DOMAIN}/{name}'


APP_VARIABLE = 'SWHURL_APP'
ENVIRONMENT_VARIABLE = 'SWHURL_ENV'
ISSUER_VARIABLE = 'SWHURL_AUTH_ISSUER'
KEYS_VARIABLE = 'SWHURL_AUTH_KEYS'
CALLERS_VARIABLE = 'SWHURL_ALLOWED_CALLERS'
ISSUER_FILE_VARIABLE = 'SWHURL_AUTH_ISSUER_FILE'
KEYS_FILE_VARIABLE = 'SWHURL_AUTH_KEYS_FILE'
CALLERS_FILE_VARIABLE = 'SWHURL_ALLOWED_CALLERS_FILE'
DESCRIPTION_VARIABLE = 'SWHURL_DESCRIPTION'
NOTIFY_VARIABLE = 'SWHURL_NOTIFY_URL'
NOTIFY_TOKEN_VARIABLE = 'SWHURL_NOTIFY_TOKEN_FILE'
TOKENS_DIR_VARIABLE = 'SWHURL_TOKENS_DIR'
EVENTS_FILE_VARIABLE = 'SWHURL_EVENTS_FILE'
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


# A platform name anywhere in a file: an app reads it, or the platform writes it.
NAME_RE = re.compile(r'SWHURL_[A-Z0-9_]+|' + re.escape(LABEL_DOMAIN) + r'/[a-z0-9-]+')


def unlisted(names) -> set[str]:
    """The names the catalogue does not list. A family member counts as listed, and so does the family's
    prefix alone (``SWHURL_SERVICE_``), which a reader builds the rest of at run time."""
    prefix = SERVICE_VARIABLE.partition('<')[0]
    return {name for name in names
            if name not in entries() and name != prefix and not SERVICE_VARIABLE_RE.match(name)}


# The catalogue itself is v2's file, which swhurl-v2 names also reads: one writer for both languages.
CATALOGUE = Path(__file__).resolve().parents[2] / 'v2' / 'contract' / 'names.json'


@functools.cache
def entries() -> dict[str, tuple[str, str, str]]:
    """Each listed name with its surface, meaning and availability, read from CATALOGUE on first use."""
    return {entry['name']: (entry['surface'], entry['meaning'], entry['availability'])
            for entry in json.loads(CATALOGUE.read_text())['names']}
