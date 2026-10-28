"""Mutations of real caller files must fail the shared consumer contract."""
import importlib.util
import io
import json
from pathlib import Path
import shutil
import tempfile
import unittest
from unittest import mock

import yaml

COMMON = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location('consumer_contract', COMMON / 'consumer_contract.py')
CONTRACT = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(CONTRACT)
PIN = 'a' * 40


class ConsumerContractTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / 'scripts').mkdir()
        (self.root / '.github/workflows').mkdir(parents=True)
        shutil.copyfile(COMMON / 'bootstrap.py', self.root / 'scripts/common_ci.py')

    def fixture(self, profile='service', module='github.com/fin-assist/user'):
        (self.root / 'go.mod').write_text('module ' + module + '\n')
        (self.root / '.finassist-ci.json').write_text(json.dumps({'revision': PIN, 'profile': profile}))
        kinds = {'go', 'lint', 'api' if profile == 'library' else 'image'}
        if module != 'github.com/fin-assist/bank-bff':
            kinds.add('migrations')
        jobs = {}
        for kind in kinds:
            call = {'uses': CONTRACT.SOURCE + 'shared-' + kind + '.yml@' + PIN,
                    'with': {'revision': PIN}}
            if kind == 'go':
                call['with']['profile'] = profile
            if kind != 'migrations':
                call['secrets'] = {'GO_MODULES_TOKEN': CONTRACT.PUSH_TOKEN}
            jobs[kind + '-check'] = call
            jobs[kind] = {'needs': kind + '-check',
                'if': "always() && github.event_name == 'pull_request'" if kind == 'migrations' else 'always()',
                'env': {'RESULT': '${{ needs.' + kind + '-check.result }}'},
                'runs-on': 'ubuntu-latest', 'timeout-minutes': 2,
                'steps': [{'run': 'test "$RESULT" = success'}]}
        return {'permissions': {'contents': 'read'}, 'jobs': jobs}

    def result(self, document):
        (self.root / '.github/workflows/ci.yml').write_text(yaml.safe_dump(document))
        stream = io.StringIO()
        return unittest.TextTestRunner(stream=stream).run(CONTRACT.suite(self.root, COMMON))

    def test_service_library_template_and_exact_bff_profiles_pass(self):
        for profile, module in [('service', 'github.com/fin-assist/user'),
                                ('library', 'github.com/fin-assist/go-library-template'),
                                ('library', 'github.com/fin-assist/go-postgres'),
                                ('service', 'github.com/fin-assist/bank-bff')]:
            with self.subTest(profile=profile, module=module):
                result = self.result(self.fixture(profile, module))
                self.assertTrue(result.wasSuccessful())
                self.assertEqual(result.testsRun, 3)

    def test_missing_call_wrong_uses_input_or_profile_fail(self):
        for change in ('missing', 'uses-pin', 'uses-path', 'input-pin', 'go-profile', 'secrets'):
            with self.subTest(change=change):
                doc = self.fixture()
                call = doc['jobs']['go-check']
                if change == 'missing': del doc['jobs']['lint-check']
                elif change == 'uses-pin': call['uses'] = call['uses'].replace(PIN, 'b' * 40)
                elif change == 'uses-path': call['uses'] = call['uses'].replace('shared-go', 'shared-api')
                elif change == 'input-pin': call['with']['revision'] = 'b' * 40
                elif change == 'go-profile': call['with']['profile'] = 'library'
                else: call['secrets'] = 'inherit'
                self.assertFalse(self.result(doc).wasSuccessful())

    def test_facade_name_needs_expression_condition_and_bypass_mutations_fail(self):
        for change in ('name', 'needs', 'result', 'if', 'run', 'skip-step', 'ignore-failure'):
            with self.subTest(change=change):
                doc = self.fixture()
                gate = doc['jobs']['go']
                if change == 'name': gate['name'] = 'unrequired-check'
                elif change == 'needs': gate['needs'] = 'lint-check'
                elif change == 'result': gate['env']['RESULT'] = '${{ needs.lint-check.result }}'
                elif change == 'if': gate['if'] = 'success()'
                elif change == 'run': gate['steps'][0]['run'] = 'true'
                elif change == 'skip-step': gate['steps'][0]['if'] = 'false'
                else: gate['continue-on-error'] = True
                self.assertFalse(self.result(doc).wasSuccessful())

    def test_bff_exemption_does_not_apply_to_other_module(self):
        doc = self.fixture('service', 'github.com/fin-assist/user')
        del doc['jobs']['migrations']
        del doc['jobs']['migrations-check']
        self.assertFalse(self.result(doc).wasSuccessful())

    def test_modified_local_bootstrap_fails(self):
        doc = self.fixture('library', 'github.com/fin-assist/go-postgres')
        (self.root / 'scripts/common_ci.py').write_text('# changed local entrypoint\n')
        self.assertFalse(self.result(doc).wasSuccessful())

    def test_gate_really_executes_rejection_cases(self):
        doc = self.fixture()
        # A faulty shell that returns success for every outcome must be rejected too.
        with mock.patch.object(CONTRACT.subprocess, 'run', return_value=mock.Mock(returncode=0)):
            self.assertFalse(self.result(doc).wasSuccessful())


if __name__ == '__main__':
    unittest.main()
