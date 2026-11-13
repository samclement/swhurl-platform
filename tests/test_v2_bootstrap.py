"""Pinned downloads are an explicit step, never an offline check's network fallback."""
import unittest
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[1]



class V2BootstrapTests(unittest.TestCase):
    def test_each_v2_ci_job_bootstraps_before_any_go_using_check(self):
        count = 0
        for name in ('validate', 'render', 'deploy-v2'):
            workflow = yaml.safe_load((ROOT / f'.github/workflows/{name}.yml').read_text())
            for job in workflow['jobs'].values():
                steps = job['steps']
                setups = [i for i, step in enumerate(steps)
                          if step.get('with', {}).get('go-version-file') == 'v2/go.mod']
                if not setups:
                    continue
                self.assertEqual(len(setups), 1)
                setup = setups[0]
                self.assertEqual(steps[setup]['with']['cache-dependency-path'], 'v2/go.sum')
                self.assertEqual(steps[setup + 1]['run'], 'make v2-deps')
                for i, step in enumerate(steps):
                    run = step.get('run', '')
                    for command in ('make test', 'make check-v2', 'make check-apps',
                                    'make check-templates', 'make -s v2-render', 'go build'):
                        if command in run:
                            self.assertGreater(i, setup + 1, f'{name}: {command} precedes bootstrap')
                count += 1
        self.assertEqual(count, 7)

    def test_image_bootstrap_is_separate_from_its_offline_build(self):
        image = (ROOT / 'images/console/Dockerfile').read_text()
        module_copy = image.index('COPY v2/go.mod v2/go.sum ./')
        download = image.index('go mod download go.yaml.in/yaml/v3')
        source_copy = image.index('COPY v2/ ./')
        build = image.index('GOPROXY=off GOSUMDB=off CGO_ENABLED=0 go build')
        self.assertLess(module_copy, download)
        self.assertLess(download, source_copy)
        self.assertLess(source_copy, build)
        self.assertIn('GOPROXY=https://proxy.golang.org GOSUMDB=sum.golang.org go mod download', image)
