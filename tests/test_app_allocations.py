"""Preparation owns allocations; even a late refusal cannot write a seed or any other file."""
import hashlib
import io
import shutil
import subprocess
import sys
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from dataclasses import replace
from pathlib import Path
from unittest import mock

import full_settings
from full_settings import FULL, NAMES

from swhurl import ROOT, yamlio
from swhurl.apps import cli, edit, egress, events, new, promotion, transitions
from swhurl.apps.errors import EditError
from swhurl.apps.instance import Instance
from swhurl.apps.model import render, store, write
from swhurl.apps.model.draft import EVENTS_SECRET, Entry
from swhurl.apps.model.records import Allocations, App, Choices, Grants, Image, Release, Settings, Storage
from swhurl.run import FakeRunner, Result

IMAGE = Image('ghcr.io/example/orders', '2-abcdef0', 'sha256:' + 'a' * 64)


def web(**changes):
    return replace(Release(IMAGE, 'web', 65532, port=8080, health_path='/healthz'), **changes)


class AllocationTests(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        self.each = Instance('orders', 'staging')
        registry = self.root / render.REGISTRY_FILE
        registry.parent.mkdir(parents=True)
        registry.write_text('resources: []\n')
        self.put(self.each)
        write.sync(self.root, runner=FakeRunner())

    def put(self, each, *, release=None, settings=None):
        entry = Entry(App('demo'), release or web(), settings or Settings())
        for path, text in store.documents({each: entry}).items():
            target = self.root / path
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text(text)

    def nats(self):
        path = self.root / events.CONTROLLER_KEY_FILE
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text('UCONTROLLER\n')

    def snapshot(self):
        return {str(p.relative_to(self.root)): p.read_bytes() for p in self.root.rglob('*') if p.is_file()}

    def entry(self, each=None):
        return write.read(self.root)[0][each or self.each]

    def encrypting(self, fail_at=None):
        before, calls = self.snapshot(), []
        def encrypt(args, given):
            self.assertEqual(self.snapshot(), before, 'no writes even between instances/encryptions')
            self.assertEqual(args, ('sops', '--encrypt', '--filename-override', args[3], '--input-type', 'yaml',
                                    '--output-type', 'yaml', '/dev/stdin'))
            self.assertTrue(args[3].startswith('apps/') and args[3].endswith('.sops.yaml'))
            document = yamlio.load(given)
            values = document['stringData']
            calls.append(given)
            if len(calls) == fail_at:
                return Result(args, 1, given, 'SECRET ERROR: ' + given)
            document['stringData'] = {name: 'ENC[' + hashlib.sha256(value.encode()).hexdigest() + ']'
                                      for name, value in values.items()}
            return Result(args, stdout=yamlio.dump(document) + 'sops: {}\n')
        return FakeRunner().on('sops', handler=encrypt)

    def sync(self, rotate=None, runner=None):
        with redirect_stdout(io.StringIO()):
            return write.apply(self.root, write.prepare(self.root, {rotate: write.Rotate()} if rotate else {}, runner=runner))

    def test_sync_bootstraps_all_missing_in_one_plan_and_is_byte_idempotent(self):
        self.put(Instance('orders', 'prod'))
        self.put(Instance('shop', 'staging'))
        self.nats()
        runner = self.encrypting()
        self.sync(runner=runner)
        self.assertEqual([c[3] for c in runner.calls], [f'apps/{name}/{env}/events.sops.yaml' for name, env in
                         (('orders', 'staging'), ('shop', 'staging'), ('orders', 'prod'))])
        world, _ = write.read(self.root)
        self.assertEqual(len({e.settings.allocations.events_key for e in world.values()}), 3)
        before = self.snapshot()
        self.assertEqual(self.sync(runner=FakeRunner()), [])
        self.assertEqual(self.snapshot(), before)

    def test_complete_records_only_sync_needs_no_sops_and_restores_exact_output(self):
        self.nats()
        self.sync(runner=self.encrypting())
        before = self.snapshot()
        for name in render.MANAGED_FILES:
            (self.each.folder(self.root) / name).unlink(missing_ok=True)
        self.each.unit_file(self.root).unlink()
        self.sync(runner=FakeRunner())  # unexpected calls (including SOPS) fail the test
        self.assertEqual(self.snapshot(), before)

    def test_ports_keep_existing_values_then_take_lowest_free_staging_before_production(self):
        self.put(self.each, settings=Settings(allocations=Allocations(30102)))
        self.put(Instance('alpha', 'prod'))
        self.put(Instance('zebra', 'staging'))
        shutil.copytree(ROOT / egress.DIRECTORY, self.root / egress.DIRECTORY, dirs_exist_ok=True)
        self.sync(runner=FakeRunner())
        ports = {str(i): e.settings.allocations.egress_port for i, e in write.read(self.root)[0].items()}
        self.assertEqual(ports, {'alpha/prod': 30103, 'orders/staging': 30102, 'zebra/staging': 30101})
        self.assertEqual(self.sync(runner=FakeRunner()), [])

    def test_every_operation_gives_a_missing_identity_its_key_once_nats_exists(self):
        self.put(Instance('shop', 'staging'))
        write.apply(self.root, write.prepare(self.root, {}, runner=FakeRunner()))
        self.assertFalse(list(self.root.rglob('events.sops.yaml')), 'without NATS or events, nobody needs a key')
        self.nats()
        runner = self.encrypting()
        write.change(self.root, self.each, lambda entry: replace(entry, release=replace(entry.release, description='new')),
                     runner=runner)
        self.assertEqual(len(runner.calls), 2)
        self.assertTrue(self.entry().settings.allocations.events_key)
        self.assertTrue(self.entry(Instance('shop', 'staging')).settings.allocations.events_key)
        before = self.snapshot()
        write.change(self.root, self.each, lambda entry: entry, runner=FakeRunner())  # complete pairs need no SOPS
        self.assertEqual(self.snapshot(), before)

    def test_new_app_allocates_its_own_key_not_an_existing_apps_missing_key(self):
        self.nats()
        args = new.parse_args(['fresh', '--root', str(self.root), '--team', 'demo', '--image',
                               'ghcr.io/example/fresh:2-abcdef0', '--health-path', '/healthz', '--no-policy-check'])
        new.generate(args, self.root, runner=self.encrypting())
        self.assertTrue(self.entry(Instance('fresh', 'staging')).settings.allocations.events_key)
        self.assertTrue(self.entry().settings.allocations.events_key, 'the same rule gives the existing app its key')
        self.assertNotEqual(self.entry().settings.allocations.events_key,
                            self.entry(Instance('fresh', 'staging')).settings.allocations.events_key)

    def test_new_app_encryption_and_late_routes_failures_leave_complete_tree_unchanged(self):
        self.nats()
        self.sync(runner=self.encrypting())  # a healthy tree: only the new app needs a key
        for failure in ('encryption', 'routes'):
            if failure == 'routes':
                (self.root / 'platform/notify').mkdir()
                (self.root / 'platform/notify/channel-phone.sops.yaml').write_text('metadata: [')
            args = new.parse_args(['fresh', '--root', str(self.root), '--team', 'demo', '--image',
                                   'ghcr.io/example/fresh:2-abcdef0', '--health-path', '/healthz', '--no-policy-check'])
            before, runner = self.snapshot(), self.encrypting(fail_at=1 if failure == 'encryption' else None)
            with self.subTest(failure=failure), self.assertRaises(new.GenerationError):
                new.generate(args, self.root, runner=runner)
            self.assertEqual(len(runner.calls), 1, 'failure reached allocation or later planning')
            self.assertEqual(self.snapshot(), before)
            self.assertFalse((self.root / 'apps/fresh').exists())

    def test_release_events_and_subscriptions_allocate_without_nats_platform(self):
        publisher = self.entry()
        write.apply(self.root, write.prepare(self.root, {self.each: write.Update(
            lambda _: replace(publisher, release=web(publishes=('created',))))}, runner=self.encrypting()))
        self.put(Instance('shop', 'staging'))
        write.apply(self.root, write.prepare(self.root, {}))
        with redirect_stdout(io.StringIO()):
            edit.set_events(self.root, 'shop', 'staging', subscribe=['orders.created'], runner=self.encrypting())
        self.assertTrue(self.entry(Instance('shop', 'staging')).settings.allocations.events_key)

    def test_rotation_replaces_only_target_identity_and_derived_files_and_drops_old_user(self):
        self.put(self.each, release=web(publishes=('created',)), settings=Settings(grants=Grants(
            allows=('shop',), egress=('example.com:443',))))
        shop = Instance('shop', 'staging')
        self.put(shop, settings=Settings(grants=Grants(calls=('orders',), subscriptions=('orders.created',))))
        self.nats()
        # Calling requires public signing-key input; no private key or external command.
        (self.root / 'apps/signing-keys.json').write_text('{"keys": []}\n')
        self.sync(runner=self.encrypting())
        bucket = self.root / 'flags-values.fixture'
        bucket.write_text('false\n')
        before, old, other = self.snapshot(), self.entry(), self.entry(shop)
        runner = self.encrypting()
        with mock.patch.object(runner, 'run', wraps=runner.run) as run:
            changed = self.sync(rotate=self.each, runner=runner)
        self.assertEqual(len(runner.calls), 1)
        self.assertTrue(run.call_args.kwargs['secret_output'])
        after = self.entry()
        self.assertNotEqual(after.settings.allocations.events_key, old.settings.allocations.events_key)
        self.assertEqual(after.release, old.release)
        self.assertEqual(after.settings.grants, old.settings.grants)
        self.assertEqual(after.settings.choices, old.settings.choices)
        self.assertEqual(self.entry(shop), other)
        self.assertEqual(set(str(p.relative_to(self.root)) for p in changed), {
            'apps/orders/staging/instance.yaml', 'apps/orders/staging/events.sops.yaml',
            'apps/orders/staging/helmrelease.yaml'})
        self.assertEqual((self.root / 'apps/orders/staging/events.yaml').read_bytes(), before['apps/orders/staging/events.yaml'])
        self.assertEqual(bucket.read_text(), 'false\n')

    def test_rotation_passes_the_guard_and_needs_something_that_uses_the_identity(self):
        self.nats()
        self.sync(runner=self.encrypting())
        self.put(Instance('shop', 'staging'))  # the one rule: rotating also gives a missing identity its key
        runner = self.encrypting()
        self.sync(rotate=self.each, runner=runner)
        self.assertEqual(len(runner.calls), 2)
        self.assertTrue(self.entry(Instance('shop', 'staging')).settings.allocations.events_key)
        release = self.each.folder(self.root) / 'helmrelease.yaml'
        release.write_text(release.read_text() + '# a hand edit\n')
        before = self.snapshot()
        with self.assertRaisesRegex(EditError, 'files differ from what the platform renders'):
            self.sync(rotate=self.each, runner=FakeRunner())
        self.assertEqual(self.snapshot(), before)
        self.sync(runner=FakeRunner())
        (self.root / events.CONTROLLER_KEY_FILE).unlink()
        before = self.snapshot()
        with self.assertRaisesRegex(EditError, 'nothing needs a NATS identity'):
            self.sync(rotate=self.each, runner=self.encrypting())
        self.assertEqual(self.snapshot(), before)

    def test_rotation_requires_existing_target_and_complete_identity(self):
        for target, message in ((Instance('missing', 'staging'), 'no instance'), (self.each, 'existing complete')):
            with self.subTest(target=target):
                before = self.snapshot()
                with self.assertRaisesRegex(EditError, message):
                    self.sync(rotate=target, runner=FakeRunner())
                self.assertEqual(self.snapshot(), before)

    def test_partial_pairs_refuse_bootstrap_and_rotation_and_public_deletion_is_not_rotation(self):
        self.nats()
        self.sync(runner=self.encrypting())
        complete = self.snapshot()
        for absent in ('public', 'seed'):
            for rotate in (None, self.each):
                for name, data in complete.items():
                    (self.root / name).write_bytes(data)
                if absent == 'public':
                    path = self.each.folder(self.root) / 'instance.yaml'
                    doc = yamlio.load(path.read_text())
                    del doc['allocations']['eventsKey']
                    path.write_text(yamlio.dump(doc))
                else:
                    (self.each.folder(self.root) / events.SECRET_FILE).unlink()
                before = self.snapshot()
                with self.subTest(absent=absent, rotate=rotate), self.assertRaisesRegex(EditError, 'inconsistent NATS identity'):
                    self.sync(rotate=rotate, runner=FakeRunner())
                self.assertEqual(self.snapshot(), before)

    def test_cli_rotation_accepts_one_target_and_rejects_malformed_or_missing_targets(self):
        for value in ('orders', 'orders/staging/extra', 'Orders/staging', 'orders/dev', ''):
            with self.subTest(value=value), redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
                cli.main_sync(['--root', str(self.root), '--rotate-nkey', value])
        self.nats()
        self.sync(runner=self.encrypting())
        runner = self.encrypting()
        with mock.patch.object(write, 'Runner', return_value=runner), redirect_stdout(io.StringIO()):
            self.assertEqual(cli.main_sync(['--root', str(self.root), '--rotate-nkey', str(self.each)]), 0)
        with redirect_stderr(io.StringIO()):
            self.assertEqual(cli.main_sync(['--root', str(self.root), '--rotate-nkey', 'missing/prod']), 2)

    def test_encryption_failure_on_second_bootstrap_leaves_all_files_unchanged_and_errors_redacted(self):
        self.put(Instance('shop', 'staging'))
        self.nats()
        before = self.snapshot()
        with mock.patch.object(write.nkeys, 'generate', return_value=('SECRET-SEED', 'UPUBLIC')):
            with self.assertRaisesRegex(EditError, 'SOPS-encrypt') as error:
                self.sync(runner=self.encrypting(fail_at=2))
        self.assertNotIn('SECRET-SEED', str(error.exception))
        self.assertNotIn('SECRET ERROR', str(error.exception))
        self.assertEqual(self.snapshot(), before)

    def test_unencrypted_response_refuses_without_writes(self):
        self.nats()
        before = self.snapshot()
        with self.assertRaisesRegex(EditError, 'did not encrypt'):
            self.sync(runner=FakeRunner().on('sops', handler=lambda args, given: Result(args, stdout=given + 'sops: ENC[x]')))
        self.assertEqual(self.snapshot(), before)

    def test_rotation_encryption_render_and_late_routes_failures_preserve_existing_seed_and_complete_tree(self):
        self.nats()
        self.sync(runner=self.encrypting())
        for failure in ('encryption', 'render', 'routes'):
            notify = self.root / 'platform/notify'
            if failure == 'routes':
                notify.mkdir(parents=True)
                (self.root / 'apps/teams.yaml').write_text('teams: [')
            before = self.snapshot()
            runner = self.encrypting(fail_at=1 if failure == 'encryption' else None)
            rendered = []

            def late(*args, real=render.instance, rendered=rendered, **kwargs):  # the guard renders first; refuse after allocation
                rendered.append(args)
                if len(rendered) > 1:
                    raise write.RenderError('late render refusal')
                return real(*args, **kwargs)
            context = mock.patch.object(render, 'instance', side_effect=late) \
                if failure == 'render' else mock.patch.object(render, 'instance', wraps=render.instance)
            with self.subTest(failure=failure), context, self.assertRaises(EditError):
                self.sync(rotate=self.each, runner=runner)
            self.assertEqual(len(runner.calls), 1)
            self.assertEqual(self.snapshot(), before)

    def test_multi_instance_bootstrap_late_route_refusal_is_zero_write(self):
        self.put(Instance('shop', 'staging'))
        self.nats()
        (self.root / 'platform/notify').mkdir()
        (self.root / 'apps/teams.yaml').write_text('teams: [')
        before, runner = self.snapshot(), self.encrypting()
        with self.assertRaisesRegex(EditError, 'notification route'):
            self.sync(runner=runner)
        self.assertEqual(len(runner.calls), 2)
        self.assertEqual(self.snapshot(), before)

    def test_pending_ciphertext_does_not_falsify_disk_existence_and_never_contains_seed(self):
        self.nats()
        before = self.snapshot()
        with mock.patch.object(write.nkeys, 'generate', return_value=('SECRET-SEED', 'UPUBLIC')):
            prepared = write.prepare(self.root, {}, runner=self.encrypting())
        entry = prepared.world[self.each]
        self.assertNotIn(EVENTS_SECRET, entry.encrypted)
        self.assertIn(EVENTS_SECRET, entry.pending)
        self.assertNotIn('SECRET-SEED', ''.join(prepared.writes.values()))
        self.assertEqual(self.snapshot(), before)
        write.apply(self.root, prepared)
        self.assertIn(EVENTS_SECRET, self.entry().encrypted)
        self.assertFalse(self.entry().pending)

    def test_storage_size_is_explicit_and_existing_class_and_uid_guards_survive(self):
        old = self.entry()
        updated, _ = transitions.take_release(old, web(database='sqlite'), 'orders', '128Mi', 'swhurl.yaml')
        self.assertIsNone(updated.settings.allocations.database, 'the transition does not allocate')
        before = self.snapshot()
        with self.assertRaisesRegex(EditError, 'explicit size'):
            write.prepare(self.root, {self.each: write.Update(lambda _: updated)})
        self.assertEqual(self.snapshot(), before)
        write.apply(self.root, write.prepare(self.root, {self.each: write.Update(lambda _: updated, '128Mi')}))
        entry = self.entry()
        self.assertEqual(entry.settings.allocations.database, Storage('128Mi', 'local-path-retain'))
        self.assertIsNone(entry.settings.choices.replicas)
        custom = replace(entry, settings=replace(entry.settings, allocations=replace(entry.settings.allocations,
                                                                                 database=Storage('128Mi', 'custom-retain'))))
        self.put(self.each, release=custom.release, settings=custom.settings)
        write.apply(self.root, write.prepare(self.root, {}))
        for release, size, message in ((web(database='sqlite', uid=1000), '128Mi', 'files belong to uid'),
                                       (web(), '128Mi', 'push never deletes data'),
                                       (web(database='sqlite'), '256Mi', 'volume keeps its size')):
            with self.subTest(message=message), self.assertRaisesRegex(EditError, message):
                transitions.take_release(self.entry(), release, 'orders', size, 'swhurl.yaml')
        self.sync(runner=FakeRunner())
        self.assertEqual(self.entry().settings.allocations.database.storage_class, 'custom-retain')

    def test_first_production_allocates_independent_same_size_storage_and_key_and_later_keeps_settings(self):
        self.put(self.each, release=web(database='sqlite'), settings=Settings(grants=Grants(allows=('shop',)),
                 choices=Choices(replicas=0), allocations=Allocations(database=Storage('128Mi', 'custom-retain'))))
        write.apply(self.root, write.prepare(self.root, {}))
        self.nats()
        with redirect_stdout(io.StringIO()):
            edit.promote(self.root, 'orders', runner=self.encrypting())
        prod = Instance('orders', 'prod')
        production, staging = self.entry(prod), self.entry()
        self.assertEqual(production.settings.allocations.database, Storage('128Mi', 'local-path-retain'))
        self.assertEqual(staging.settings.allocations.database, Storage('128Mi', 'custom-retain'))
        self.assertTrue(production.settings.allocations.events_key)
        self.assertEqual(production.settings.choices.replicas, 0, 'first production copies the scaled-zero choice')
        self.assertTrue(staging.settings.allocations.events_key, 'the same rule gives staging its missing key')
        self.assertNotEqual(production.settings.allocations.events_key, staging.settings.allocations.events_key,
                            'first production has its own key')
        self.assertEqual(production.settings.grants.allows, staging.settings.grants.allows)
        changed = replace(staging.release, image=replace(IMAGE, tag='3-bcdef01', digest='sha256:' + 'b' * 64))
        write.change(self.root, self.each, lambda entry: replace(
            entry, release=changed, settings=replace(entry.settings, choices=Choices(replicas=1),
                                                   grants=Grants(allows=('different',)))), runner=FakeRunner())
        with redirect_stdout(io.StringIO()):
            edit.promote(self.root, 'orders', runner=FakeRunner())
        self.assertEqual(self.entry(prod).settings, production.settings)
        self.assertEqual(self.entry(prod).release, changed)

    def test_first_production_copies_or_drops_every_settings_field_on_purpose(self):
        copied = {'exposure', 'calls', 'allows', 'exceptions', 'issuer', 'replicas', 'capacity', 'schedule'}
        own = {'host', 'subscriptions', 'egress', 'egress_port', 'events_key', 'database'}  # production's, never staging's
        self.assertEqual(copied | own, set(NAMES), 'say whether first production copies a new Settings field')
        staging = replace(FULL, grants=replace(FULL.grants, exposure='authenticated-web'))  # a public one needs its own --host
        made = promotion.first_production(self.root, {self.each: Entry(App('demo'), web(), staging)}, 'orders').entry.settings
        for name in NAMES:
            with self.subTest(field=name):
                self.assertEqual(full_settings.value(made, name), full_settings.value(staging if name in copied else Settings(), name))

    def test_short_app_secret_does_not_false_match_a_character_in_ciphertext(self):
        with redirect_stdout(io.StringIO()):
            edit.set_secret(self.root, 'orders', 'staging', 'API_TOKEN', 'a', runner=self.encrypting())
        self.assertTrue((self.each.folder(self.root) / 'secret.sops.yaml').is_file())

    def test_new_app_secret_uses_planned_ciphertext_and_route_failure_writes_nothing(self):
        (self.root / 'platform/notify').mkdir(parents=True)
        (self.root / 'apps/teams.yaml').write_text('teams: [')
        before = self.snapshot()
        with self.assertRaisesRegex(EditError, 'notification route'):
            edit.set_secret(self.root, 'orders', 'staging', 'API_TOKEN', 'SECRET-VALUE', runner=self.encrypting())
        self.assertEqual(self.snapshot(), before)
        self.assertFalse((self.each.folder(self.root) / 'secret.sops.yaml').exists())

    def test_first_production_placeholder_then_identity_failure_leaves_no_files(self):
        self.put(self.each, release=web(secrets=('API_TOKEN',)))
        write.apply(self.root, write.prepare(self.root, {}))
        self.nats()
        before, runner = self.snapshot(), self.encrypting(fail_at=2)
        with redirect_stdout(io.StringIO()), self.assertRaisesRegex(EditError, 'SOPS-encrypt'):
            edit.promote(self.root, 'orders', runner=runner)
        self.assertEqual(len(runner.calls), 2, 'placeholder encrypted before identity failure')
        self.assertEqual(self.snapshot(), before)
        self.assertFalse((self.root / 'apps/orders/prod').exists())

    def test_stdlib_records_and_offline_policy_import_boundaries(self):
        result = subprocess.run([sys.executable, '-S', '-c',
            'import sys; sys.path.insert(0, "tools"); from swhurl.apps.model import records; '
            'assert not any(n in sys.modules for n in ("yaml", "ruamel", "swhurl.apps.model.write")); '
            'assert records.Settings.parse({"version": 1}).grants == records.Grants()'],
            cwd=ROOT, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        result = subprocess.run([sys.executable, '-c',
            'from swhurl.apps import policy; import sys; '
            'assert not any(n in sys.modules for n in ("ruamel", "swhurl.apps.model.write", "swhurl.apps.edit"))'],
            cwd=ROOT, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
