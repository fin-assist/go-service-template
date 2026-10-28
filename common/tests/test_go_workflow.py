"""Execution and trust-boundary checks for reusable Go CI entrypoints."""
import os
import json
from pathlib import Path
import subprocess
import tempfile
import unittest

import yaml


ROOT = Path(__file__).resolve().parents[2]
WORKFLOWS = ROOT / '.github/workflows'


def document(name):
    return yaml.safe_load((WORKFLOWS / name).read_text())


def steps(name):
    return document(name)['jobs']['check']['steps']


def named(name, title):
    return next(step for step in steps(name) if step.get('name') == title)


def run(script, **environment):
    return subprocess.run(['bash', '-euo', 'pipefail', '-c', script],
                          env={**os.environ, **environment}, capture_output=True,
                          text=True, timeout=5)


class SharedGoWorkflowTests(unittest.TestCase):
    def test_entrypoints_are_read_only_bounded_reusable_jobs(self):
        for name in ['shared-go.yml', 'shared-api.yml']:
            with self.subTest(workflow=name):
                workflow = document(name)
                trigger = workflow.get('on', workflow.get(True))
                call = trigger['workflow_call']
                self.assertTrue(call['inputs']['revision']['required'])
                self.assertEqual(call['inputs']['revision']['type'], 'string')
                self.assertFalse(call['secrets']['GO_MODULES_TOKEN']['required'])
                self.assertEqual(workflow['permissions'], {'contents': 'read'})
                self.assertEqual(workflow['jobs']['check']['name'], 'check')
                self.assertGreater(workflow['jobs']['check']['timeout-minutes'], 0)
                for step in steps(name):
                    self.assertNotIn('${{', step.get('run', ''))
                    if step.get('uses', '').startswith('actions/checkout@'):
                        self.assertIs(step['with']['persist-credentials'], False)
                    if 'TOKEN' in step.get('env', {}):
                        self.assertEqual(step['env']['TOKEN'],
                            "${{ github.event_name == 'push' && secrets.GO_MODULES_TOKEN || '' }}")

    def test_revision_and_profile_validation_rejects_floating_or_hostile_inputs(self):
        validation = named('shared-go.yml', 'Validate shared profile and revision')['run']
        for profile in ['service', 'library']:
            self.assertEqual(run(validation, SHARED_REVISION='a'*40,
                                 SHARED_PROFILE=profile).returncode, 0)
        for revision in ['', 'main', 'v1', 'a'*39, 'a'*41, 'G'*40, 'a'*39+';', '$(exit 0)']:
            with self.subTest(revision=revision):
                self.assertNotEqual(run(validation, SHARED_REVISION=revision,
                                        SHARED_PROFILE='service').returncode, 0)
        for profile in ['', 'contracts', 'service; true', '$(exit 0)']:
            with self.subTest(profile=profile):
                self.assertNotEqual(run(validation, SHARED_REVISION='a'*40,
                                        SHARED_PROFILE=profile).returncode, 0)

    def test_api_also_refuses_non_sha_revision(self):
        validation = named('shared-api.yml', 'Validate shared revision')['run']
        self.assertEqual(run(validation, SHARED_REVISION='0'*40).returncode, 0)
        for revision in ['', 'main', 'a'*39, 'a'*41, '$(exit 0)']:
            self.assertNotEqual(run(validation, SHARED_REVISION=revision).returncode, 0)

    def test_api_checker_receives_its_required_pinned_analyzer(self):
        analyzer = document('shared-api.yml')['env']['GORELEASE']
        self.assertRegex(analyzer, r'^golang.org/x/exp/cmd/gorelease@v[0-9].+')

    def test_manifest_and_call_cannot_drift_apart(self):
        validation = named('shared-go.yml', 'Match the local source and profile')['run']
        with tempfile.TemporaryDirectory() as directory:
            manifest = Path(directory) / '.finassist-ci.json'
            for profile, revision, accepted in [('service', 'a'*40, True),
                                                ('library', 'a'*40, False),
                                                ('service', 'b'*40, False)]:
                manifest.write_text(json.dumps({'revision': revision, 'profile': profile}))
                result = subprocess.run(['bash', '-euo', 'pipefail', '-c', validation],
                    cwd=directory, env={**os.environ, 'SHARED_REVISION': 'a'*40,
                                       'SHARED_PROFILE': 'service'},
                    capture_output=True, text=True, timeout=5)
                self.assertEqual(result.returncode == 0, accepted, result.stderr)

    def test_every_collector_and_independent_gate_is_required(self):
        gate = named('shared-go.yml', 'Require all three suites and thresholds')
        self.assertEqual(gate['if'], 'always()')
        outcomes = {key: 'success' for key in gate['env']}
        self.assertEqual(set(outcomes), {'COLLECT_UNIT', 'CHECK_UNIT', 'COLLECT_INTEGRATION',
                                        'CHECK_INTEGRATION', 'COLLECT_API', 'CHECK_API'})
        self.assertEqual(run(gate['run'], **outcomes).returncode, 0)
        for key in outcomes:
            for rejected in ['', 'failure', 'skipped', 'cancelled']:
                with self.subTest(outcome=key, rejected=rejected):
                    self.assertNotEqual(run(gate['run'], **{**outcomes, key: rejected}).returncode, 0)
        indexed = {step['id']: step for step in steps('shared-go.yml') if 'id' in step}
        for suite in ['unit', 'integration', 'api']:
            collect, check = indexed['collect_'+suite], indexed['check_'+suite]
            self.assertEqual(collect['run'], 'make collect-'+suite)
            self.assertIs(collect['continue-on-error'], True)
            self.assertEqual(check['run'], 'make check-'+suite)
            self.assertIs(check['continue-on-error'], True)
            self.assertEqual(check['if'], f"always() && steps.collect_{suite}.outcome == 'success'")

    def test_coverage_reports_and_library_na_evidence_are_preserved(self):
        summary = named('shared-go.yml', 'Coverage summary')
        self.assertEqual(summary['if'], 'always()')
        self.assertIn('Only the original empty go-library-template reports N/A after successful suites',
                      summary['run'])
        upload = next(step for step in steps('shared-go.yml')
                      if step.get('uses', '').startswith('actions/upload-artifact@'))
        self.assertEqual(upload['if'], 'always()')
        self.assertEqual(upload['with']['if-no-files-found'], 'error')
        for evidence in ['unit.out', 'integration.out', 'api.out', '*.jsonl',
                         '*.coverage.txt', '*.report.txt']:
            self.assertIn(evidence, upload['with']['path'].splitlines())

    def test_caller_script_dependencies_precede_tests_and_api_keeps_release_history(self):
        go_steps = steps('shared-go.yml')
        dependency = named('shared-go.yml', 'Script dependencies')
        tests = named('shared-go.yml', 'Test the scripts')
        self.assertLess(go_steps.index(dependency), go_steps.index(tests))
        self.assertIn('import yaml', dependency['run'])
        self.assertTrue(any(step.get('uses', '').startswith('actions/setup-node@')
                            for step in go_steps))
        checkout = next(step for step in steps('shared-api.yml')
                        if step.get('uses', '').startswith('actions/checkout@'))
        self.assertEqual(checkout['with']['fetch-depth'], 0)
        self.assertEqual(named('shared-api.yml', 'API changes')['run'], 'python3 scripts/api_check.py')


if __name__ == '__main__':
    unittest.main()
