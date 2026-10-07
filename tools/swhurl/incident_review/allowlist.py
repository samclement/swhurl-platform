"""Load and strictly validate the checked-in incident review allowlist."""
from __future__ import annotations

import re
from dataclasses import dataclass
from functools import cache
from pathlib import Path
from typing import Any

import yaml

from .errors import PolicyError

PATH = Path(__file__).with_name('allowlist.yaml')
SIGNALS = ('error-logs', 'error-spans', 'unexpected-service', 'missing-telemetry')
SEVERITIES = ('low', 'default', 'high')
ENVIRONMENTS = ('staging', 'prod')
FORBIDDEN_PARTS = ('.github/', 'apps/', 'clusters/', 'platform/', 'infra/')
INT_DEFAULTS = ('cooldown_hours', 'max_analyses_per_day', 'baseline_hours', 'rate_ratio', 'rate_min_count',
                'incident_retention_days', 'run_cost_cents', 'monthly_refuse_cents')
LABEL = re.compile(r'[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?')
REPOSITORY = re.compile(r'[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9._-]{1,100}')
PATCH_PATH = re.compile(r'[A-Za-z0-9_][A-Za-z0-9_.-]*(?:/[A-Za-z0-9_][A-Za-z0-9_.-]*)*/?')


@dataclass(frozen=True)
class App:
    app: str
    repository: str
    environments: tuple[str, ...]
    expected_service: str
    signals: tuple[str, ...]
    severity: dict[str, str]
    patch_paths: tuple[str, ...]
    checks: tuple[str, ...]

    def namespace(self, env: str) -> str:
        return f'{self.app}-{env}'


@dataclass(frozen=True)
class Allowlist:
    model: str
    defaults: dict[str, Any]
    apps: tuple[App, ...]
    alert_rules: tuple[dict[str, str], ...]

    def app(self, name: str) -> App:
        for item in self.apps:
            if item.app == name:
                return item
        raise PolicyError('app is not allowlisted')

    def repository(self, repository: str) -> App:
        for item in self.apps:
            if item.repository == repository:
                return item
        raise PolicyError('repository is not allowlisted')

    def covered(self, app: str, signal: str) -> bool:
        return any(rule['app'] == app and rule['signal'] == signal for rule in self.alert_rules)


def _keys(value: Any, expected: set[str], what: str) -> dict:
    if not isinstance(value, dict) or set(value) != expected:
        raise PolicyError(f'allowlist {what} must have exactly the keys {sorted(expected)}')
    return value


def _strings(value: Any, what: str, *, allowed: tuple[str, ...] | None = None, empty: bool = False) -> tuple[str, ...]:
    if (not isinstance(value, list) or (not value and not empty) or len(set(map(str, value))) != len(value)
            or any(not isinstance(item, str) or not item or len(item) > 200 for item in value)):
        raise PolicyError(f'allowlist {what} must be a list of distinct non-empty strings')
    if allowed is not None and not set(value) <= set(allowed):
        raise PolicyError(f'allowlist {what} contains a value that is not one of {list(allowed)}')
    return tuple(value)


def _positive_int(value: Any, what: str) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value < 1:
        raise PolicyError(f'allowlist {what} must be a positive integer')
    return value


def parse(doc: Any) -> Allowlist:
    """Validate a decoded allowlist document; anything unknown is refused."""
    doc = _keys(doc, {'version', 'model', 'defaults', 'apps', 'alert_rules'}, 'file')
    if doc['version'] != 1 or isinstance(doc['version'], bool):
        raise PolicyError('allowlist version must be 1')
    if not isinstance(doc['model'], str) or len(doc['model']) > 100:
        raise PolicyError('allowlist model must be a string')
    defaults = _keys(doc['defaults'], {'confidence_min', *INT_DEFAULTS}, 'defaults')
    confidence = defaults['confidence_min']
    if isinstance(confidence, bool) or not isinstance(confidence, (int, float)) or not 0 < confidence <= 1:
        raise PolicyError('allowlist confidence_min must be above 0 and at most 1')
    for name in INT_DEFAULTS:
        _positive_int(defaults[name], name)
    if not isinstance(doc['apps'], list) or not doc['apps']:
        raise PolicyError('allowlist apps must be a non-empty list')
    apps = []
    for raw in doc['apps']:
        raw = _keys(raw, {'app', 'repository', 'environments', 'expected_service', 'signals', 'severity',
                          'patch_paths', 'checks'}, 'app')
        for name in ('app', 'expected_service'):
            if not isinstance(raw[name], str) or not LABEL.fullmatch(raw[name]):
                raise PolicyError(f'allowlist {name} must be a DNS label')
        if not isinstance(raw['repository'], str) or not REPOSITORY.fullmatch(raw['repository']):
            raise PolicyError('allowlist repository must be owner/name')
        signals = _strings(raw['signals'], 'signals', allowed=SIGNALS)
        severity = raw['severity']
        if (not isinstance(severity, dict) or set(severity) != set(signals)
                or not set(severity.values()) <= set(SEVERITIES)):
            raise PolicyError('allowlist severity must name low, default or high for exactly the listed signals')
        paths = _strings(raw['patch_paths'], 'patch_paths')
        for path in paths:
            if (not PATCH_PATH.fullmatch(path) or '..' in path.split('/')
                    or any(path.startswith(part) for part in FORBIDDEN_PARTS)):
                raise PolicyError('allowlist patch_paths must be safe relative paths outside platform directories')
        apps.append(App(raw['app'], raw['repository'], _strings(raw['environments'], 'environments',
                                                                 allowed=ENVIRONMENTS),
                        raw['expected_service'], signals, dict(severity), paths,
                        _strings(raw['checks'], 'checks', empty=True)))
    for field in ('app', 'repository'):
        if len({getattr(item, field) for item in apps}) != len(apps):
            raise PolicyError(f'allowlist lists the same {field} twice')
    if not isinstance(doc['alert_rules'], list):
        raise PolicyError('allowlist alert_rules must be a list')
    known = {(item.app, signal) for item in apps for signal in item.signals}
    rules = []
    for raw in doc['alert_rules']:
        raw = _keys(raw, {'app', 'signal', 'rule'}, 'alert rule')
        if (raw['app'], raw['signal']) not in known or not isinstance(raw['rule'], str) or not raw['rule'].strip():
            raise PolicyError('allowlist alert rule must name an allowlisted app and signal and a rule')
        rules.append(dict(raw))
    return Allowlist(doc['model'], dict(defaults), tuple(apps), tuple(rules))


def load(path: Path = PATH) -> Allowlist:
    try:
        return parse(yaml.safe_load(path.read_text()))
    except (OSError, yaml.YAMLError):
        raise PolicyError('allowlist cannot be read') from None


@cache
def current() -> Allowlist:
    """The checked-in allowlist, read once per process."""
    return load()
