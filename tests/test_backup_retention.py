"""Check the backup retention set: newest per backup day and per ISO week."""
import datetime as dt
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class BackupRetentionTests(unittest.TestCase):
    def run_prune(self, stamps, *args):
        with tempfile.TemporaryDirectory() as tmp:
            directory = Path(tmp)
            for stamp in stamps:
                name = f'clickstack-mongodb-{stamp:%Y%m%dT%H%M%SZ}'
                (directory / f'{name}.archive.gz.age').write_text('x')
                (directory / f'{name}.json').write_text('{}')
            (directory / 'unrelated.txt').write_text('keep me')
            result = subprocess.run(['python3', 'scripts/prune-backups.py', str(directory), *args],
                                    cwd=ROOT, capture_output=True, text=True, check=True)
            self.assertTrue((directory / 'unrelated.txt').exists())
            archives = sorted(p.name for p in directory.glob('*.age'))
            metadata = sorted(p.name for p in directory.glob('*.json'))
            self.assertEqual([a.replace('.archive.gz.age', '') for a in archives],
                             [m.replace('.json', '') for m in metadata], 'Metadata must follow its archive')
            return archives, result.stdout

    def test_keeps_seven_days_and_four_weeks(self):
        start = dt.datetime(2026, 9, 27, 3, 0)
        stamps = [start - dt.timedelta(days=d) for d in range(60)]
        stamps.append(start - dt.timedelta(hours=1))  # second backup on the newest day
        kept, _ = self.run_prune(stamps)
        days = {k[19:27] for k in kept}
        self.assertIn('20260927', days)
        self.assertIn('20260921', days)  # 7th most recent day
        self.assertNotIn('20260827', days)  # older than 4 weeks
        self.assertEqual(len([k for k in kept if k[19:27] == '20260927']), 1, 'Only the newest per day')
        self.assertLessEqual(len(kept), 7 + 4)

    def test_pause_in_backups_never_prunes_last_copies(self):
        old = [dt.datetime(2026, 1, 1) + dt.timedelta(days=d) for d in range(3)]
        kept, _ = self.run_prune(old)
        self.assertEqual(len(kept), 3)

    def test_dry_run_removes_nothing(self):
        stamps = [dt.datetime(2026, 9, 27) - dt.timedelta(days=d) for d in range(40)]
        kept, out = self.run_prune(stamps, '--dry-run')
        self.assertEqual(len(kept), 40)
        self.assertIn('Would prune', out)


if __name__ == '__main__':
    unittest.main()
