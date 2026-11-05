"""Shared Helm acquisition: pinned cache, cold pulls and parallel readers."""
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from swhurl import charts
from swhurl.run import CommandError, FakeRunner, Result


class ChartTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.cache = Path(self.temporary.name) / 'cache'
        self.target = self.cache / 'app-template-5.2.1'
        self.patch = mock.patch.object(charts, 'CACHE', self.cache)
        self.patch.start()
        self.addCleanup(self.patch.stop)

    def acquire(self, runner):
        return charts.chart_dir('app-template', '5.2.1', 'https://charts.example.test', runner)

    def test_cached_chart_does_not_call_runner(self):
        self.target.mkdir(parents=True)
        (self.target / 'Chart.yaml').write_text('name: app-template\n')
        runner = FakeRunner()
        self.assertEqual(self.acquire(runner), self.target)
        self.assertEqual(runner.calls, [])

    def test_cold_pull_is_pinned_and_reused(self):
        def pull(args, _):
            unpacked = Path(args[-1]) / 'app-template'
            unpacked.mkdir()
            (unpacked / 'Chart.yaml').write_text('name: app-template\n')
            return Result(args)
        runner = FakeRunner().on('helm', 'pull', handler=pull)
        self.assertEqual(self.acquire(runner), self.target)
        self.assertEqual(self.acquire(runner), self.target)
        self.assertEqual(len(runner.calls), 1)
        self.assertEqual(runner.calls[0][:-1], ('helm', 'pull', 'app-template', '--repo',
                         'https://charts.example.test', '--version', '5.2.1', '--untar', '--untardir'))
        self.assertEqual(list(self.cache.iterdir()), [self.target])

    def test_failed_pull_does_not_create_a_cached_chart(self):
        runner = FakeRunner().on('helm', 'pull', returncode=1, stderr='pull refused')
        with self.assertRaisesRegex(CommandError, 'pull refused'):
            self.acquire(runner)
        self.assertEqual(list(self.cache.iterdir()), [])

    def test_parallel_pull_accepts_only_a_complete_existing_target(self):
        for complete in (True, False):
            with self.subTest(complete=complete):
                def pull(args, _, complete=complete):
                    unpacked = Path(args[-1]) / 'app-template'
                    unpacked.mkdir()
                    (unpacked / 'Chart.yaml').write_text('name: app-template\n')
                    self.target.mkdir(exist_ok=True)
                    (self.target / ('Chart.yaml' if complete else 'partial')).write_text('other pull')
                    return Result(args)
                runner = FakeRunner().on('helm', 'pull', handler=pull)
                if complete:
                    self.assertEqual(self.acquire(runner), self.target)
                    (self.target / 'Chart.yaml').unlink()
                else:
                    with self.assertRaises(OSError):
                        self.acquire(runner)
