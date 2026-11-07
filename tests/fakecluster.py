"""``swhurl-v2 verify``'s report as the console's tests need it: the document and a healthy cluster scope's checks.

Go decides every platform check (``v2/observe``), so here its report is only an answer. The console runs the cluster
scope (its six checks) and shows what it reports.
"""

CONSOLE_TAG = 'b63d2aff9dfd4cf9df0307e382038dcbd15e29ed'


def verify_report(*checks):
    """swhurl-v2 verify's document: ``checks`` are ``(name, section, [(level, message), ...])``."""
    return {'kind': 'v2-platform-checks', 'version': 1, 'source': 'verify-platform', 'observedAt': '2026-11-06T12:00:00Z',
            'passed': all(level != 'bad' for _, _, entries in checks for level, _ in entries),
            'checks': [{'name': name, 'section': section, 'entries': [{'level': level, 'message': message} for level, message in entries]}
                       for name, section, entries in checks]}


HEALTHY_CHECKS = (
    ('flux-units', 'Flux Kustomizations', [('ok', 'homelab-a'), ('ok', 'homelab-b')]),
    ('ingress', 'Ingress', [('ok', 'Traefik redirects HTTP to HTTPS')]),
    ('alerts', 'Alerts', [('ok', 'alert failures sends to ntfy-platform')]),
    ('notify-service', 'Notify service', [('ok', f'the notify service is running {CONSOLE_TAG}, as the console')]),
    ('dashboards', 'App dashboards and alerts', [('ok', 'dashboard and alert sync succeeded in the last 5 minutes')]),
    ('notifications', 'Notifications', [('ok', 'notification checker succeeded in the last 5 minutes')]),
)
"""What swhurl-v2 verify (its cluster scope, the console's) reports for a healthy cluster."""
