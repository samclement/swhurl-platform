"""The image's declaration label: what a build states about itself, from swhurl.yaml and its events document.

The document is v2's contract (`kernel.Declaration`), so these tests assert the whole document per app
type, the fields it must leave out, and every way the files can fail to produce one.
"""
import io
import json
import os
import subprocess
import sys
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path

from swhurl.apps import declaration, image_declaration

LABEL = 'platform.swhurl.com/declaration'

EVENTS = """asyncapi: 3.0.0
info:
  title: app
  version: 0.1.0
channels:
  visited:
    address: visited
    messages:
      visited:
        $ref: '#/components/messages/visited'
operations:
  publishVisited:
    action: send
    channel:
      $ref: '#/channels/visited'
components:
  messages:
    visited:
      payload:
        type: object
"""

# The api app type, as the TypeScript template renders it (and as swhurl-try-10 states it).
WEB = """version: 1
stack: typescript
kind: web
description: an app
port: 8080
healthPath: /healthz
uid: 65532
telemetry: otlp
database: sqlite
provides:
  api: /openapi.json
  apiFile: openapi.json
  eventsFile: asyncapi.yaml
flags:
  shout:
    type: boolean
    default: false
"""

# The worker app type, as the Kotlin template renders it: a nested events document and the JVM's minimum.
WORKER = """version: 1
stack: kotlin
kind: worker
uid: 65532
telemetry: otlp
resources:
  cpu: 100m
  memory: 192Mi
  memoryLimit: 384Mi
provides:
  eventsFile: src/main/resources/asyncapi.yaml
"""

JOB = """version: 1
stack: typescript
kind: job
uid: 65532
telemetry: otlp
provides:
  eventsFile: asyncapi.yaml
"""

# swhurl-try-11's own file: no description, no events document, and a field the file drops.
NO_EVENTS = """version: 1
stack: typescript
kind: web
port: 8080
healthPath: /healthz
uid: 65532
telemetry: otlp
autoDeploy: true
provides:
  api: /openapi.json
  apiFile: openapi.json
"""

QUIET = """version: 1
stack: typescript
kind: worker
uid: 1000
telemetry: none
"""

SECRETS = """version: 1
stack: typescript
kind: web
port: 8080
healthPath: /healthz
uid: 65532
secrets:
  - SOME_TOKEN
"""

CHANNELS_ONLY = """asyncapi: 3.0.0
info:
  title: app
  version: 0.1.0
channels:
  visited:
    address: visited
"""


def checkout(swhurl: str, events: str | None = None, name: str = 'asyncapi.yaml') -> tempfile.TemporaryDirectory:
    directory = tempfile.TemporaryDirectory()
    Path(directory.name, 'swhurl.yaml').write_text(swhurl)
    if events is not None:
        Path(directory.name, name).parent.mkdir(parents=True, exist_ok=True)
        Path(directory.name, name).write_text(events)
    return directory


def run(directory: str) -> tuple[int, str, str]:
    """The command as the build runs it: from the app's checkout, printing one line."""
    out, err = io.StringIO(), io.StringIO()
    with redirect_stdout(out), redirect_stderr(err):
        code = image_declaration.main(['--app-dir', directory])
    return code, out.getvalue(), err.getvalue()


def label(swhurl: str, events: str | None = None, name: str = 'asyncapi.yaml') -> tuple[str, str]:
    """The printed label's key and document, from a checkout made of the two files."""
    with checkout(swhurl, events, name) as directory:
        code, out, err = run(directory)
    assert code == 0, err
    key, _, value = out.rstrip('\n').partition('=')
    return key, value


def document(swhurl: str, events: str | None = None, name: str = 'asyncapi.yaml') -> dict:
    key, value = label(swhurl, events, name)
    assert key == LABEL
    return json.loads(value)


class DocumentTests(unittest.TestCase):
    """One document per app type, and the facts an image does not have left out."""

    def test_a_web_image_states_its_runtime_telemetry_database_and_publications(self):
        self.assertEqual(document(WEB, EVENTS), {
            'schemaVersion': 1,
            'runtime': {'kind': 'web', 'uid': 65532, 'port': 8080, 'healthPath': '/healthz'},
            'telemetry': True,
            'database': 'sqlite',
            'publishes': ['visited'],
        })

    def test_a_worker_image_states_no_port_and_its_minimum_memory(self):
        self.assertEqual(document(WORKER, EVENTS, 'src/main/resources/asyncapi.yaml'), {
            'schemaVersion': 1,
            'runtime': {'kind': 'worker', 'uid': 65532},
            'telemetry': True,
            'publishes': ['visited'],
            'memoryMiB': 192,
        })

    def test_a_job_image_states_its_own_kind_rather_than_a_kind_v2_models(self):
        # v2 refuses `runtime.kind` it does not model; the image must not pretend to be a worker.
        self.assertEqual(document(JOB, EVENTS), {
            'schemaVersion': 1,
            'runtime': {'kind': 'job', 'uid': 65532},
            'telemetry': True,
            'publishes': ['visited'],
        })

    def test_an_image_that_reports_nowhere_and_publishes_nothing_states_neither(self):
        self.assertEqual(document(QUIET), {'schemaVersion': 1, 'runtime': {'kind': 'worker', 'uid': 1000}})

    def test_a_file_with_no_events_document_states_no_publications(self):
        self.assertEqual(document(NO_EVENTS), {
            'schemaVersion': 1,
            'runtime': {'kind': 'web', 'uid': 65532, 'port': 8080, 'healthPath': '/healthz'},
            'telemetry': True,
        })

    def test_an_events_document_with_no_send_operation_states_no_publications(self):
        self.assertNotIn('publishes', document(NO_EVENTS.replace('provides:', 'provides:\n  eventsFile: asyncapi.yaml'),
                                               CHANNELS_ONLY))

    def test_secret_names_are_stated_so_v2_holds_the_image(self):
        # v2 refuses a non-empty `secrets` list: the image is held, not run without its values.
        self.assertEqual(document(SECRETS)['secrets'], ['SOME_TOKEN'])

    def test_publications_keep_the_documents_order(self):
        worked = EVENTS.replace(
            "        $ref: '#/components/messages/visited'\n",
            "        $ref: '#/components/messages/visited'\n  worked:\n    address: worked\n"
            "    messages:\n      worked:\n        $ref: '#/components/messages/visited'\n").replace(
            "      $ref: '#/channels/visited'",
            "      $ref: '#/channels/visited'\n  publishWorked:\n    action: send\n    channel:\n      $ref: '#/channels/worked'")
        self.assertEqual(document(WORKER, worked, 'src/main/resources/asyncapi.yaml')['publishes'],
                         ['visited', 'worked'])


class MemoryTests(unittest.TestCase):
    """`memoryMiB` is the image's minimum, in whole MiB, never understated and never invented."""

    def test_units_are_whole_mebibytes_rounded_up(self):
        self.assertEqual([declaration.mebibytes(q) for q in ('192Mi', '1536Ki', '512Ki', '2Gi')],
                         [192, 2, 1, 2048])

    def test_a_limit_without_a_minimum_states_no_memory(self):
        text = WORKER.replace('  memory: 192Mi\n  memoryLimit', '  memoryLimit')
        self.assertNotIn('memoryMiB', document(text, EVENTS, 'src/main/resources/asyncapi.yaml'))


class LineTests(unittest.TestCase):
    """The build passes the line to a config label: one line, one key, compact JSON, no nulls."""

    def test_the_line_is_the_label_key_and_one_compact_document(self):
        key, value = label(WEB, EVENTS)
        self.assertEqual(key, LABEL)
        self.assertNotIn('\n', value)
        self.assertNotIn('null', value)
        self.assertEqual(value, json.dumps(json.loads(value), separators=(',', ':')))
        with checkout(WEB, EVENTS) as directory:
            self.assertEqual(run(directory)[1].count('\n'), 1)

    def test_the_same_checkout_states_the_same_line(self):
        with checkout(WEB, EVENTS) as directory:
            self.assertEqual(run(directory)[1], run(directory)[1])


class RefusalTests(unittest.TestCase):
    """Nothing to state is a refusal that names the file, never a label with a guessed fact."""

    def assertRefused(self, swhurl: str, events: str | None = None, *, says: str, name: str = 'asyncapi.yaml'):
        with checkout(swhurl, events, name) as directory:
            code, out, err = run(directory)
        self.assertEqual((code, out), (2, ''))
        self.assertIn(says, err)

    def test_an_unknown_field_is_refused(self):
        self.assertRefused(WEB.replace('uid: 65532', 'uid: 65532\ncolour: blue'), EVENTS, says='unknown field')

    def test_a_missing_swhurl_yaml_is_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            code, out, err = run(directory)
        self.assertEqual((code, out), (2, ''))
        self.assertIn('swhurl.yaml', err)

    def test_an_events_document_swhurl_yaml_names_but_does_not_keep_is_refused(self):
        self.assertRefused(WEB, says='which swhurl.yaml names')

    def test_an_events_document_that_is_not_asyncapi_3_is_refused(self):
        self.assertRefused(WORKER, EVENTS.replace('asyncapi: 3.0.0', 'asyncapi: 2.6.0'),
                           says='AsyncAPI 3', name='src/main/resources/asyncapi.yaml')

    def test_a_send_operation_without_a_channel_is_refused(self):
        self.assertRefused(WEB, EVENTS.replace("    channel:\n      $ref: '#/channels/visited'", ''),
                           says='channel must reference')


class ImportTests(unittest.TestCase):
    def test_the_command_reads_no_platform_settings(self):
        """An app's build has only `tools/` from the platform, so nothing this command imports may read the
        cluster's manifests: `swhurl.apps.contract` reads the settings file when it is imported."""
        code = ('import sys, swhurl.apps.image_declaration\n'
                'print("\\n".join(sorted(name for name in sys.modules if name.startswith("swhurl"))))')
        with tempfile.TemporaryDirectory() as directory:
            result = subprocess.run([sys.executable, '-c', code], cwd=directory, capture_output=True, text=True,
                                    env={**os.environ, 'PYTHONPATH': str(Path(__file__).resolve().parents[1] / 'tools')})
        self.assertEqual(result.returncode, 0, result.stderr)
        imported = set(result.stdout.split())
        self.assertNotIn('swhurl.apps.contract', imported)
        self.assertNotIn('swhurl.platform', imported)
