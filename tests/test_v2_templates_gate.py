"""make check-templates (v2/check-templates.sh): each app type, rendered from its stack's template, must use only
listed names and produce a label v2's creation rules accept; the answers no type has must be refused. Offline: git,
Copier and swhurl-v2 are fakes on PATH, and the stacks are the real catalogue's."""
import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


CATALOGUE = json.loads((ROOT / 'v2/contract/stacks.json').read_text())
FIRST_TEAM = next(iter(json.loads((ROOT / 'v2-records/teams.json').read_text())['teams']))
REFUSAL = "ValueError: Validation error for question 'frontend': kind=worker database=sqlite is not an app type"

# git: the checkout's own root for rev-parse; a clone makes the folder; fetch and checkout are recorded.
GIT = f'''echo "git $*" >> "$CALLS"
case "$1" in
  rev-parse) echo {ROOT} ;;
  clone) mkdir -p "${{@: -1}}/.git" ;;
esac
'''
# copier: renders swhurl.yaml for the answers it is given, as COPIER_MODE says.
COPIER = f'''echo "copier $*" >> "$CALLS"
dest="${{@: -1}}"
answers=" $* "
outside=false
case "$answers" in *" kind=worker "*" database=sqlite "*|*" database=sqlite "*" kind=worker "*) outside=true ;; esac
case "$COPIER_MODE" in
  broken) echo 'no such template' >&2; exit 1 ;;
  refuse-all) echo "{REFUSAL}" >&2; exit 1 ;;
  choice) echo "ValueError: Invalid choice for 'kind': 'web' is not in ['worker']" >&2; exit 1 ;;
esac
if $outside && [ "$COPIER_MODE" != accept-outside ]; then echo "Traceback" >&2; echo "{REFUSAL}" >&2; exit 1; fi
mkdir -p "$dest"
for arg in "$@"; do  # the answers file records every answer but kind in the unasked mode
  case "$arg" in *=*) [ "$COPIER_MODE" = unasked ] && [ "${{arg%%=*}}" = kind ] && continue
    echo "${{arg%%=*}}: ${{arg#*=}}" >> "$dest/.copier-answers.yml" ;; esac
done
[ "$COPIER_MODE" = no-manifest ] && exit 0
echo 'version: 1' > "$dest/swhurl.yaml"
'''
# swhurl-v2: names, the producer and declaration acceptance answer as V2_* say.
V2 = '''echo "swhurl-v2 $*" >> "$CALLS"
case "$1" in
  names) [ -n "${V2_UNLISTED:-}" ] && { echo "$V2_UNLISTED"; exit 1; }; exit 0 ;;
  image-declaration) [ -n "${V2_PRODUCER_DIES:-}" ] && kill -KILL "$PPID"  # the check running it
    [ -n "${V2_PRODUCER_REFUSES:-}" ] && { echo "$V2_PRODUCER_REFUSES" >&2; exit 2; }
    echo 'platform.swhurl.com/declaration={"schemaVersion":2}' ;;
  declaration) test "$(cat "$3")" = 'platform.swhurl.com/declaration={"schemaVersion":2}'
    [ -n "${V2_DECLARATION_REFUSES:-}" ] && { echo "$V2_DECLARATION_REFUSES" >&2; exit 2; }; exit 0 ;;
  *) exit 99 ;;
esac
'''


def type_labels():
    """Each type built with each stack, labelled as the check reports it."""
    for stack in CATALOGUE['stacks']:
        for key, found in CATALOGUE['types'].items():
            if stack in found['stacks']:
                answers = ' '.join(f'{k}={v}' for k, v in sorted(found['answers'].items()))
                yield f'{stack} {answers} ({key})'


class TemplatesGateTests(unittest.TestCase):
    def setUp(self):
        self.root = Path(tempfile.mkdtemp())
        self.addCleanup(subprocess.run, ['rm', '-rf', str(self.root)])
        self.calls = self.root / 'calls'
        for name, content in (('git', GIT), ('copier', COPIER), ('swhurl-v2', V2),
                              ('python3', 'echo unexpected-python >&2; exit 88\n'),
                              ('go', 'echo "go $*" >> "$CALLS"; while [ "$1" != -o ]; do shift; done\n'
                                     'cp "$(dirname "$0")/swhurl-v2" "$2"\n')):
            path = self.root / name
            path.write_text('#!/bin/bash\nset -eu\n' + content)
            path.chmod(0o700)

    def check(self, *args, binary=True, **env):
        """Run the script; returns its exit status, stdout and the commands it ran."""
        self.calls.write_text('')
        environment = dict(os.environ, PATH=f'{self.root}:{os.environ["PATH"]}', CALLS=str(self.calls),
                           COPIER_MODE=env.pop('COPIER_MODE', 'ok'), **env)
        environment.pop('SWHURL_V2', None)
        if binary:
            environment['SWHURL_V2'] = str(self.root / 'swhurl-v2')
        result = subprocess.run(['bash', str(ROOT / 'v2/check-templates.sh'), *args], cwd=self.root,
                                env=environment, capture_output=True, text=True)
        self.assertNotIn('unexpected-python', result.stderr)
        return result.returncode, result.stdout, self.calls.read_text().splitlines()

    def test_each_type_produces_a_label_the_creation_rules_accept(self):
        code, out, calls = self.check()
        self.assertEqual(code, 0, out)
        for label in type_labels():
            self.assertIn(f'[OK] {label}: the label applies to a new instance', out)
        for stack, found in CATALOGUE['stacks'].items():
            self.assertIn(f'git clone --quiet --depth 1 https://github.com/{found["repository"]}.git', '\n'.join(calls))
            self.assertIn(f'fetch --quiet --depth 1 origin {found["revision"]}', '\n'.join(calls))
            self.assertIn(f'[OK] {stack} database=sqlite kind=worker: the template refuses it '
                          '(kind=worker database=sqlite is not an app type)', out)
        renders = [c for c in calls if c.startswith('copier copy')]
        self.assertEqual(len(renders), len(list(type_labels())) + len(CATALOGUE['stacks']),
                         'each type and, per stack, the answers no type has')
        self.assertTrue(all('--vcs-ref' not in c and '--data app_name=contract-check' in c for c in renders),
                        'renders the pinned checkout as a plain folder')
        per_app = {}  # the checks run in parallel: their order holds within each rendered app
        for call in (c.split() for c in calls if c.startswith('swhurl-v2')):
            path = call[call.index('--app-dir' if '--app-dir' in call else '--label') + 1]
            per_app.setdefault(str(Path(path).parent), []).append(call[1])
        self.assertEqual(list(per_app.values()),
                         [['names', 'image-declaration', 'declaration']] * len(list(type_labels())))
        declaration = next(c for c in calls if c.startswith('swhurl-v2 declaration'))
        self.assertIn(f'--app contract-check --team {FIRST_TEAM} --exposure private', declaration)
        self.assertNotIn('--out', declaration, 'the check writes nothing: the report is stdout only')
        names = next(c for c in calls if c.startswith('swhurl-v2 names'))
        self.assertIn('--catalogue v2/contract/names.json --app-dir ', names)

    def test_the_template_must_refuse_answers_no_type_has(self):
        code, out, _ = self.check(COPIER_MODE='accept-outside')
        self.assertEqual(code, 1)
        self.assertIn('[BAD] typescript database=sqlite kind=worker: no app type has these answers, but the template '
                      'accepts them', out)
        code, out, _ = self.check(COPIER_MODE='broken')
        self.assertEqual(code, 1)
        self.assertIn('no app type has these answers, but the template failed otherwise: copier copy exited 1: '
                      'no such template', out)
        self.assertIn('(api): copier copy exited 1: no such template', out, 'any other copier failure fails')

    def test_a_type_the_template_refuses_or_no_longer_offers_fails(self):
        code, out, calls = self.check(COPIER_MODE='refuse-all')
        self.assertEqual(code, 1)
        self.assertIn('(api): the template refuses it (kind=worker database=sqlite is not an app type)', out)
        self.assertFalse(any(c.startswith('swhurl-v2') for c in calls), 'nothing rendered, nothing checked')
        code, out, _ = self.check(COPIER_MODE='choice')
        self.assertEqual(code, 1)
        self.assertIn("(api): the template refuses it (Invalid choice for 'kind': 'web' is not in ['worker'])", out)

    def test_a_producer_or_creation_refusal_fails(self):
        code, out, calls = self.check(V2_PRODUCER_REFUSES='swhurl.yaml: secrets names a reserved variable')
        self.assertEqual(code, 1)
        self.assertIn('the image-declaration producer refused the app: swhurl.yaml: secrets names a reserved variable',
                      out)
        self.assertFalse(any(c.startswith('swhurl-v2 declaration') for c in calls))
        code, out, _ = self.check(V2_DECLARATION_REFUSES='invalid: runtime: no')
        self.assertEqual(code, 1)
        self.assertIn('swhurl-v2 declaration refused the label: invalid: runtime: no', out)
        code, out, _ = self.check(
            V2_DECLARATION_REFUSES='unsupported: runtime.kind: only web and worker releases are supported')
        self.assertEqual(code, 0, 'a kind v2 does not model yet is a reported gap, not a template failure')
        self.assertIn('[WARN]', out)
        self.assertIn('v2 does not model it yet (unsupported: runtime.kind', out)

    def test_a_name_the_catalogue_does_not_list_fails(self):
        code, out, _ = self.check(V2_UNLISTED='SWHURL_UNLISTED (swhurl.yaml)\nplatform.swhurl.com/x (src/a.ts)')
        self.assertEqual(code, 1)
        self.assertIn('v2/contract/names.json does not list SWHURL_UNLISTED (swhurl.yaml), platform.swhurl.com/x '
                      '(src/a.ts)', out)
        self.assertIn('the label applies to a new instance', out, 'the declaration is still checked')

    def test_an_answer_the_template_does_not_ask_fails(self):
        """Copier ignores an answer to a question the template no longer asks; the answers file shows it."""
        code, out, calls = self.check(COPIER_MODE='unasked')
        self.assertEqual(code, 1)
        self.assertIn('(api): the template does not record kind=web: it no longer asks kind, or answers it otherwise', out)
        self.assertFalse(any(c.startswith('swhurl-v2') for c in calls), 'a type that does not fit is not checked')

    def test_a_check_that_stops_without_a_result_fails(self):
        code, out, _ = self.check(V2_PRODUCER_DIES='1')
        self.assertEqual(code, 1)
        self.assertIn('(api): the check stopped before it reported a result', out)
        self.assertNotIn('[OK] typescript api=openapi', out)

    def test_a_missing_manifest_fails(self):
        code, out, _ = self.check(COPIER_MODE='no-manifest')
        self.assertEqual(code, 1)
        self.assertIn('(api): the template rendered no swhurl.yaml', out)

    def test_a_missing_binary_is_built_once_for_the_run(self):
        code, out, calls = self.check(binary=False)
        self.assertEqual(code, 0, out)
        self.assertIn('[INFO] SWHURL_V2 names no binary: building swhurl-v2 from v2/', out)
        self.assertEqual(len([c for c in calls if c.startswith('go build')]), 1)

    def test_arguments_are_refused_before_anything_runs(self):
        for argv in (['--stack', 'kotlin'], ['--help']):
            code, _, calls = self.check(*argv)
            self.assertEqual(code, 2)
            self.assertEqual(calls, [])
