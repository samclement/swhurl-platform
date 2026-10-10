"""
Prove an app can verify who called it with what Kubernetes already issues:
  - a caller's short-lived token for audience "callee" is accepted by a callee
    that has no API token of its own
  - the callee reads the cluster's public signing keys unauthenticated, and only
    once a ClusterRoleBinding allows it
  - a token for another audience, and a request without a token, are refused
  - the token names the caller's namespace and service account
  - the Kubernetes API refuses the token
  - the kubelet replaces the token file before it expires (about 8 minutes;
    SKIP_ROTATION=true skips this check)
Creates only two namespaces and one ClusterRoleBinding labelled
platform.swhurl.com/call-identity-test=true and removes them. No token is printed.
"""
from __future__ import annotations

import json
import os

from swhurl.livetests import LiveTest, Preflight, run_live_test
from swhurl.platform import label

CALLER_NS = 'call-identity-caller'
CALLEE_NS = 'call-identity-callee'
BINDING = 'call-identity-test-jwks'
LABEL = label('call-identity-test')
IMAGE = 'node:24-bookworm-slim'
SUBJECT = f'system:serviceaccount:{CALLER_NS}:caller'
TOKEN_SECONDS = 600  # the shortest the API server issues

# The callee: verifies a bearer token against the cluster's public keys, then
# reports what it found (never the token).
CALLEE_JS = r"""
import crypto from 'node:crypto';
import fs from 'node:fs';
import http from 'node:http';
import https from 'node:https';

const ISSUER = 'https://kubernetes.default.svc.cluster.local';
const ca = fs.readFileSync('/ca/ca.crt');
const api = (path, token) => new Promise((resolve, reject) => {
  const headers = token ? { authorization: `Bearer ${token}` } : {};
  https.get({ host: 'kubernetes.default.svc', path, ca, headers }, (res) => {
    let body = '';
    res.on('data', (chunk) => { body += chunk; });
    res.on('end', () => resolve({ status: res.statusCode, body }));
  }).on('error', reject);
});
const decode = (part) => JSON.parse(Buffer.from(part, 'base64url').toString());

async function verify(token) {
  const keys = await api('/openid/v1/jwks');
  const out = { accepted: false, keysStatus: keys.status };
  if (keys.status !== 200) return { ...out, reason: 'cannot read signing keys' };
  const [head, payload, signature] = token.split('.');
  if (!signature) return { ...out, reason: 'no token' };
  const header = decode(head);
  const claims = decode(payload);
  const jwk = JSON.parse(keys.body).keys.find((key) => key.kid === header.kid);
  if (!jwk || header.alg !== 'RS256') return { ...out, reason: 'unknown key' };
  const valid = crypto.verify('RSA-SHA256', Buffer.from(`${head}.${payload}`),
    crypto.createPublicKey({ key: jwk, format: 'jwk' }), Buffer.from(signature, 'base64url'));
  if (!valid) return { ...out, reason: 'bad signature' };
  Object.assign(out, { sub: claims.sub, aud: claims.aud, exp: claims.exp });
  out.apiStatus = (await api('/api/v1/namespaces', token)).status;
  if (claims.iss !== ISSUER) return { ...out, reason: 'wrong issuer' };
  if (!claims.aud.includes('callee')) return { ...out, reason: 'wrong audience' };
  if (claims.exp * 1000 < Date.now()) return { ...out, reason: 'expired' };
  return { ...out, accepted: true };
}

http.createServer(async (req, res) => {
  const token = (req.headers.authorization ?? '').replace(/^Bearer /, '');
  let result;
  try { result = await verify(token); } catch (error) { result = { accepted: false, reason: String(error) }; }
  res.writeHead(result.accepted ? 200 : 401, { 'content-type': 'application/json' });
  res.end(JSON.stringify(result));
}).listen(8080, '0.0.0.0');
"""

# The caller: sends the token file for one audience ("none" sends no token) and
# prints the callee's answer.
CALLER_JS = rf"""
import fs from 'node:fs';

const audience = process.argv[2];
const headers = audience === 'none' ? {{}}
  : {{ authorization: `Bearer ${{fs.readFileSync(`/tokens/${{audience}}/token`, 'utf8').trim()}}` }};
const res = await fetch('http://callee.{CALLEE_NS}.svc:8080/', {{ headers, signal: AbortSignal.timeout(10000) }});
console.log(await res.text());
"""


def namespace(name: str) -> dict:
    return {'apiVersion': 'v1', 'kind': 'Namespace', 'metadata': {'name': name, 'labels': {LABEL: 'true'}}}


def script(name: str, source: str) -> dict:
    return {'apiVersion': 'v1', 'kind': 'ConfigMap', 'metadata': {'name': 'script', 'labels': {LABEL: 'true'}},
            'data': {name: source}}


def deployment(name: str, command: list[str], volumes: list[dict], mounts: list[dict],
               service_account: str | None = None) -> dict:
    spec = {'automountServiceAccountToken': False,
            'securityContext': {'runAsNonRoot': True, 'runAsUser': 65532},
            'containers': [{'name': 'c', 'image': IMAGE, 'command': command,
                            'volumeMounts': [{'name': 'script', 'mountPath': '/app'}, *mounts],
                            'resources': {'requests': {'cpu': '5m', 'memory': '48Mi'}, 'limits': {'memory': '128Mi'}}}],
            'volumes': [{'name': 'script', 'configMap': {'name': 'script'}}, *volumes]}
    if service_account:
        spec['serviceAccountName'] = service_account
    return {'apiVersion': 'apps/v1', 'kind': 'Deployment', 'metadata': {'name': name, 'labels': {LABEL: 'true'}},
            'spec': {'replicas': 1, 'selector': {'matchLabels': {'app': name}},
                     'template': {'metadata': {'labels': {'app': name, LABEL: 'true'}}, 'spec': spec}}}


def callee() -> list[dict]:
    return [script('callee.mjs', CALLEE_JS),
            deployment('callee', ['node', '/app/callee.mjs'],
                       [{'name': 'ca', 'configMap': {'name': 'kube-root-ca.crt'}}],
                       [{'name': 'ca', 'mountPath': '/ca'}]),
            {'apiVersion': 'v1', 'kind': 'Service', 'metadata': {'name': 'callee', 'labels': {LABEL: 'true'}},
             'spec': {'selector': {'app': 'callee'}, 'ports': [{'port': 8080}]}}]


def caller() -> list[dict]:
    tokens = [{'serviceAccountToken': {'audience': audience, 'expirationSeconds': TOKEN_SECONDS,
                                       'path': f'{audience}/token'}} for audience in ('callee', 'other')]
    return [script('caller.mjs', CALLER_JS),
            {'apiVersion': 'v1', 'kind': 'ServiceAccount', 'metadata': {'name': 'caller', 'labels': {LABEL: 'true'}},
             'automountServiceAccountToken': False},
            deployment('caller', ['sleep', '3600000'], [{'name': 'tokens', 'projected': {'sources': tokens}}],
                       [{'name': 'tokens', 'mountPath': '/tokens', 'readOnly': True}], service_account='caller')]


def binding() -> dict:
    """Lets anyone who can reach the API server read its public signing keys."""
    return {'apiVersion': 'rbac.authorization.k8s.io/v1', 'kind': 'ClusterRoleBinding',
            'metadata': {'name': BINDING, 'labels': {LABEL: 'true'}},
            'roleRef': {'apiGroup': 'rbac.authorization.k8s.io', 'kind': 'ClusterRole',
                        'name': 'system:service-account-issuer-discovery'},
            'subjects': [{'apiGroup': 'rbac.authorization.k8s.io', 'kind': 'Group', 'name': 'system:unauthenticated'}]}


def call(t: LiveTest, audience: str) -> dict:
    """The callee's answer to a call carrying the caller's token for ``audience``."""
    result = t.runner.run(['kubectl', '-n', CALLER_NS, 'exec', 'deploy/caller', '--', 'node', '/app/caller.mjs',
                           audience], check=False)
    try:
        return json.loads(result.stdout)
    except ValueError:
        return {'accepted': False, 'reason': (result.stderr or result.stdout).strip()[:200]}


def cleanup(t: LiveTest) -> None:
    if t.label(t.get('get', 'clusterrolebinding', BINDING), LABEL) == 'true':
        t.quietly('delete', 'clusterrolebinding', BINDING, '--ignore-not-found')
    for name in (CALLER_NS, CALLEE_NS):
        t.delete_namespace_if_labelled(name, LABEL, wait=True)
    t.report.info('Cleaned up call-identity test resources')


def body(t: LiveTest, rotation: bool) -> None:
    if any(t.get('get', 'namespace', name) for name in (CALLER_NS, CALLEE_NS)) or t.get('get', 'clusterrolebinding',
                                                                                         BINDING):
        raise Preflight('Test resources already exist; clean up first')
    t.cleanup.callback(cleanup, t)

    t.step('Deploy a caller and a callee')
    t.apply(namespace(CALLER_NS), namespace(CALLEE_NS))
    t.apply(*callee(), namespace=CALLEE_NS)
    t.apply(*caller(), namespace=CALLER_NS)
    for ns, name in ((CALLEE_NS, 'callee'), (CALLER_NS, 'caller')):
        t.kubectl('-n', ns, 'rollout', 'status', f'deploy/{name}', '--timeout=5m')
    t.check(not t.succeeds('-n', CALLEE_NS, 'exec', 'deploy/callee', '--', 'test', '-e',
                           '/var/run/secrets/kubernetes.io/serviceaccount/token'),
            'callee has no API token', 'callee has an API token mounted')

    t.step('Signing keys')
    answer = call(t, 'callee')
    t.check(answer.get('keysStatus') in (401, 403) and not answer.get('accepted'),
            f"without the binding the callee cannot read the keys (HTTP {answer.get('keysStatus')})",
            f'expected the keys to be refused before the binding: {answer}')
    t.apply(binding())

    def accepted() -> bool:
        nonlocal answer
        answer = call(t, 'callee')
        return answer.get('accepted') is True
    t.check(t.poll(accepted, attempts=20, interval=3),
            'with the binding the callee reads the keys unauthenticated and accepts the token',
            f'token not accepted: {answer}')

    t.step('What the token says, and what it cannot do')
    t.check(answer.get('sub') == SUBJECT, f'token names the caller: {SUBJECT}', f"unexpected subject: {answer.get('sub')}")
    t.check(answer.get('aud') == ['callee'], 'token is for audience callee only', f"unexpected audience: {answer.get('aud')}")
    t.check(answer.get('apiStatus') == 401, 'the Kubernetes API refuses the token (HTTP 401)',
            f"the Kubernetes API answered {answer.get('apiStatus')} to the token")
    other = call(t, 'other')
    t.check(other.get('reason') == 'wrong audience', 'a token for another audience is refused',
            f'token for another audience: {other}')
    none = call(t, 'none')
    t.check(none.get('reason') == 'no token', 'a request without a token is refused', f'request without a token: {none}')

    if not rotation:
        t.report.info('Skipped the rotation check (SKIP_ROTATION=true)')
        return
    t.step('Rotation')
    first = answer.get('exp')
    t.report.info(f'Waiting for the kubelet to replace the {TOKEN_SECONDS}-second token (about 8 minutes)')

    def rotated() -> bool:
        nonlocal answer
        answer = call(t, 'callee')
        return answer.get('accepted') is True and answer.get('exp') != first
    t.check(t.poll(rotated, attempts=40, interval=15), 'token file replaced before expiry; the new token is accepted',
            f'token not replaced within 10 minutes: {answer}')


def main(argv: list[str] | None = None, runner=None, report=None, sleep=None) -> int:
    rotation = os.environ.get('SKIP_ROTATION') != 'true'
    kwargs = {'sleep': sleep} if sleep else {}
    return run_live_test(__doc__, lambda t: body(t, rotation), passed='Call identity test passed.',
                         runner=runner, report=report, **kwargs)
