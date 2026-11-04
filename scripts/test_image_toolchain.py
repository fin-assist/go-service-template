"""Keep the image compiler identical to the compiler selected by Go CI."""
from pathlib import Path
import re
import unittest


ROOT = Path(__file__).resolve().parents[1]


class ImageToolchainTests(unittest.TestCase):
    def test_builder_matches_module_and_refuses_automatic_toolchain_switching(self):
        module = (ROOT / 'go.mod').read_text()
        dockerfile = (ROOT / 'Dockerfile').read_text()
        go_version = re.search(r'^go (\d+\.\d+\.\d+)$', module, re.M)
        self.assertIsNotNone(go_version, 'Declare an exact project compiler in go.mod')
        self.assertRegex(dockerfile, r'(?m)^FROM golang:' + re.escape(go_version[1])
                         + r'-bookworm(?:@sha256:[a-f0-9]{64})? AS build$')
        env = re.search(r'(?m)^ENV (.+)$', dockerfile)
        self.assertIsNotNone(env)
        self.assertIn('GOTOOLCHAIN=local', env[1].split())


if __name__ == '__main__':
    unittest.main()
