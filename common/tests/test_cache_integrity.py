"""Real Git metadata regressions must reject unpinned files without executing filters."""
import importlib.util
import marshal
import os
from pathlib import Path
import shlex
import subprocess
import struct
import sys
import tempfile
import unittest
from unittest import mock


COMMON = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("cache_integrity_bootstrap", COMMON / "bootstrap.py")
bootstrap = importlib.util.module_from_spec(SPEC)
with mock.patch.object(sys, "dont_write_bytecode", True):
    SPEC.loader.exec_module(bootstrap)


class CacheIntegrityTests(unittest.TestCase):
    def git(self, root, *arguments):
        environment = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
        environment.update(GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull,
                           GIT_TERMINAL_PROMPT="0")
        result = subprocess.run(["git", *arguments], cwd=root, env=environment,
                                capture_output=True, text=True, check=True, timeout=30)
        return result.stdout.strip()

    def fixture(self, parent):
        cache = parent / "cache"
        cache.mkdir()
        self.git(cache, "init", "--quiet")
        self.git(cache, "config", "user.name", "Cache integrity fixture")
        self.git(cache, "config", "user.email", "fixture@example.invalid")
        self.git(cache, "remote", "add", "origin", bootstrap.SOURCE_URL)
        (cache / "common/configs").mkdir(parents=True)
        (cache / "common/render_lint.py").write_text('print("safe")\n')
        (cache / "common/configs/rules.toml").write_text('pg_version = "18.0"\n')
        (cache / ".gitignore").write_text("__pycache__/\n")
        self.git(cache, "add", ".")
        self.git(cache, "commit", "--quiet", "-m", "Pin the fixture source")
        revision = self.git(cache, "rev-parse", "HEAD")
        self.assertEqual(bootstrap.validate_cache(cache, revision).resolve(), (cache / "common").resolve())
        return cache, revision

    def test_core_worktree_cannot_hide_modified_actual_cache_files(self):
        with tempfile.TemporaryDirectory(prefix="finassist-worktree-") as directory:
            parent = Path(directory)
            cache, revision = self.fixture(parent)
            clean = parent / "external clean checkout"
            clean.mkdir()
            self.git(cache, "--work-tree=" + str(clean), "checkout-index", "--all")
            self.git(cache, "config", "core.worktree", str(clean))
            (cache / "common/render_lint.py").write_text('print("evil")\n')
            # Ordinary Git inspects the external clean checkout, not the code we return.
            self.assertEqual(self.git(cache, "status", "--porcelain", "--untracked-files=all",
                                      "--ignored=matching"), "")
            with self.assertRaises(ValueError):
                bootstrap.validate_cache(cache, revision)

    def test_rejected_same_length_change_does_not_execute_local_clean_filter(self):
        with tempfile.TemporaryDirectory(prefix="finassist-filter-") as directory:
            parent = Path(directory)
            cache, revision = self.fixture(parent)
            sentinel = parent / "FILTER_EXECUTED"
            self.git(cache, "config", "filter.localtest.clean",
                     "touch " + shlex.quote(str(sentinel)) + "; cat")
            (cache / ".git/info/attributes").write_text("common/render_lint.py filter=localtest\n")
            before = (cache / "common/render_lint.py").read_bytes()
            after = b'print("evil")\n'
            self.assertEqual(len(before), len(after))
            (cache / "common/render_lint.py").write_bytes(after)
            self.assertFalse(sentinel.exists())
            # Do not run ordinary status here: that would execute the filter in setup.
            with self.assertRaises(ValueError):
                bootstrap.validate_cache(cache, revision)
            self.assertFalse(sentinel.exists(), "Cache validation executed an untrusted clean filter")

    def test_replaced_file_fifo_is_rejected_without_blocking(self):
        with tempfile.TemporaryDirectory(prefix="finassist-fifo-") as directory:
            cache, revision = self.fixture(Path(directory))
            renderer = cache / "common/render_lint.py"
            renderer.unlink()
            os.mkfifo(renderer)
            script = '''import importlib.util, pathlib, sys
spec = importlib.util.spec_from_file_location("fifo_bootstrap", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
try:
    module.validate_cache(pathlib.Path(sys.argv[2]), sys.argv[3])
except ValueError:
    raise SystemExit(0)
raise SystemExit(1)
'''
            result = subprocess.run([sys.executable, "-B", "-c", script, bootstrap.__file__, str(cache), revision],
                                    capture_output=True, text=True, timeout=5)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_unreadable_extra_directory_cannot_hide_executable_bytecode(self):
        with tempfile.TemporaryDirectory(prefix="finassist-unreadable-") as directory:
            parent = Path(directory)
            cache, revision = self.fixture(parent)
            renderer = cache / "common/render_lint.py"
            hidden = renderer.parent / "__pycache__"
            hidden.mkdir()
            sentinel = parent / "UNPINNED_BYTECODE_EXECUTED"
            bytecode = Path(importlib.util.cache_from_source(str(renderer)))
            info = renderer.stat()
            payload = compile("from pathlib import Path\nPath(" + repr(str(sentinel)) +
                              ").write_text('executed')\n", str(renderer), "exec")
            bytecode.write_bytes(importlib.util.MAGIC_NUMBER +
                                 struct.pack('<III', 0, int(info.st_mtime), info.st_size) +
                                 marshal.dumps(payload))
            hidden.chmod(0o311)
            try:
                # Execute/search permission permits opening a known pyc while listing fails.
                with self.assertRaises(PermissionError):
                    list(hidden.iterdir())
                self.assertEqual(bytecode.read_bytes()[:4], importlib.util.MAGIC_NUMBER)
                rejected = False
                with mock.patch.object(sys, "dont_write_bytecode", True):
                    try:
                        bootstrap.validate_cache(cache, revision)
                        spec = importlib.util.spec_from_file_location("unreadable_renderer", renderer)
                        module = importlib.util.module_from_spec(spec)
                        spec.loader.exec_module(module)
                    except ValueError:
                        rejected = True
                self.assertTrue(rejected, "Incomplete inventory was accepted")
                self.assertFalse(sentinel.exists(), "Unpinned bytecode executed")
            finally:
                hidden.chmod(0o700)


if __name__ == "__main__":
    unittest.main()
