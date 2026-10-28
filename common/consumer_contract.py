"""Shared contract tests for the actual consumer workflow and copied bootstrap."""
from pathlib import Path
import json
import os
import re
import subprocess
import unittest

import yaml


SOURCE = 'fin-assist/go-service-template/.github/workflows/'
PUSH_TOKEN = "${{ github.event_name == 'push' && secrets.GO_MODULES_TOKEN || '' }}"


def suite(root, common):
    """Build consumer-local cases without exposing an auto-discovered global TestCase."""
    root, common = Path(root), Path(common)

    class ConsumerContract(unittest.TestCase):
        def context(self):
            manifest = json.loads((root / '.finassist-ci.json').read_text())
            self.assertRegex(manifest['revision'], r'^[0-9a-f]{40}$')
            self.assertIn(manifest['profile'], ('service', 'library'))
            modules = re.findall(r'(?m)^module\s+(\S+)\s*$', (root / 'go.mod').read_text())
            self.assertEqual(len(modules), 1)
            kinds = {'go', 'lint', 'api' if manifest['profile'] == 'library' else 'image'}
            if modules[0] != 'github.com/fin-assist/bank-bff':
                kinds.add('migrations')
            workflow = yaml.safe_load((root / '.github/workflows/ci.yml').read_text())
            self.assertEqual(workflow['permissions'], {'contents': 'read'})
            self.assertEqual(set(workflow['jobs']), kinds | {kind + '-check' for kind in kinds})
            return manifest, workflow['jobs'], kinds

        def test_all_calls_use_the_manifest_source_and_profile(self):
            manifest, jobs, kinds = self.context()
            for kind in kinds:
                with self.subTest(call=kind):
                    call = jobs[kind + '-check']
                    self.assertEqual(call['uses'], SOURCE + 'shared-' + kind + '.yml@' + manifest['revision'])
                    self.assertEqual(call['with']['revision'], manifest['revision'])
                    self.assertNotIn('permissions', call)
                    if kind == 'go':
                        self.assertEqual(call['with']['profile'], manifest['profile'])
                    expected_secrets = {} if kind == 'migrations' else {'GO_MODULES_TOKEN': PUSH_TOKEN}
                    self.assertEqual(call.get('secrets', {}), expected_secrets)

        def test_required_facades_reject_every_non_success_result(self):
            _, jobs, kinds = self.context()
            for kind in kinds:
                with self.subTest(gate=kind):
                    gate = jobs[kind]
                    self.assertEqual(gate.get('name', kind), kind)
                    self.assertEqual(gate['needs'], kind + '-check')
                    condition = "always() && github.event_name == 'pull_request'" if kind == 'migrations' else 'always()'
                    self.assertEqual(gate['if'], condition)
                    self.assertEqual(gate['env']['RESULT'], '${{ needs.' + kind + '-check.result }}')
                    self.assertFalse(gate.get('continue-on-error', False))
                    self.assertEqual(gate['runs-on'], 'ubuntu-latest')
                    self.assertEqual(gate['timeout-minutes'], 2)
                    self.assertEqual(len(gate['steps']), 1)
                    step = gate['steps'][0]
                    self.assertNotIn('if', step)
                    self.assertFalse(step.get('continue-on-error', False))
                    self.assertEqual(step['run'], 'test "$RESULT" = success')
                    for outcome in ['success', 'failure', 'skipped', 'cancelled', '']:
                        result = subprocess.run(['bash', '-euo', 'pipefail', '-c', step['run']],
                            env={**os.environ, 'RESULT': outcome}, capture_output=True, text=True, timeout=5)
                        self.assertEqual(result.returncode == 0, outcome == 'success', outcome)

        def test_copied_bootstrap_matches_the_pinned_source(self):
            self.assertEqual((root / 'scripts/common_ci.py').read_bytes(), (common / 'bootstrap.py').read_bytes())

    return unittest.defaultTestLoader.loadTestsFromTestCase(ConsumerContract)
