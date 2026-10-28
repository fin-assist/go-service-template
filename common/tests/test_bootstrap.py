"""Immutable bootstrap behaviour against real local git fixtures and rejected manifests."""
from pathlib import Path
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

sys.dont_write_bytecode = True
COMMON = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(COMMON))
import bootstrap


class ManifestTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.config = {"revision": "a" * 40, "profile": "library"}

    def write(self, value):
        (self.root / bootstrap.CONFIG_NAME).write_text(json.dumps(value))

    def test_defaults_and_explicit_overrides(self):
        self.write(self.config)
        actual = bootstrap.read_config(self.root)
        self.assertFalse(actual["gosec_g115"])
        self.assertFalse(actual["squawk_smallint"])
        self.write(dict(self.config, gosec_g115=True, squawk_smallint=False))
        self.assertTrue(bootstrap.read_config(self.root)["gosec_g115"])

    def test_rejects_unknown_fields_bad_sha_profile_and_non_boolean_overrides(self):
        invalid = [None, [], {}, dict(self.config, source="https://evil.test"),
                   dict(self.config, revision="main"), dict(self.config, revision="../" + "a" * 40),
                   dict(self.config, revision="$(echo x)"), dict(self.config, revision="A" * 40),
                   dict(self.config, revision=1), dict(self.config, profile="android"),
                   dict(self.config, gosec_g115=1), dict(self.config, squawk_smallint="false")]
        for value in invalid:
            with self.subTest(value=value):
                self.write(value)
                with self.assertRaises(ValueError):
                    bootstrap.read_config(self.root)

    def test_rejects_duplicate_keys_and_oversized_input(self):
        for content in ['{"revision":"' + 'a' * 40 + '","profile":"library","profile":"service"}', " " * 4097]:
            (self.root / bootstrap.CONFIG_NAME).write_text(content)
            with self.assertRaises(ValueError):
                bootstrap.read_config(self.root)

    def test_missing_manifest_fails(self):
        with self.assertRaises(OSError):
            bootstrap.read_config(self.root)


class CacheTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.cache_root = self.root / ".finassist-ci"
        self.cache_root.mkdir()

    def fixture(self):
        checkout = self.cache_root / "fixture"
        checkout.mkdir()
        bootstrap.git(checkout, "init", "--quiet")
        bootstrap.git(checkout, "config", "user.name", "CI fixture")
        bootstrap.git(checkout, "config", "user.email", "ci@example.invalid")
        bootstrap.git(checkout, "remote", "add", "origin", bootstrap.SOURCE_URL)
        (checkout / "common").mkdir()
        (checkout / "common/render_lint.py").write_text("# fixture\n")
        bootstrap.git(checkout, "add", ".")
        bootstrap.git(checkout, "commit", "--quiet", "-m", "Fixture")
        revision = bootstrap.git(checkout, "rev-parse", "HEAD")
        target = self.cache_root / revision
        checkout.rename(target)
        return target, revision

    def test_valid_pinned_cache_resolves_without_network(self):
        cache, revision = self.fixture()
        self.assertEqual(bootstrap.source_dir(self.root, {"revision": revision}), cache / "common")

    def test_tracked_and_untracked_tampering_rejected(self):
        cache, revision = self.fixture()
        for path in (cache / "common/render_lint.py", cache / "untracked.py"):
            with self.subTest(path=path):
                original = path.read_text() if path.exists() else None
                path.write_text("# changed\n")
                with self.assertRaisesRegex(ValueError, "modifications"):
                    bootstrap.validate_cache(cache, revision)
                if original is None:
                    path.unlink()
                else:
                    path.write_text(original)

    def test_ignored_and_hidden_tracked_tampering_rejected(self):
        cache, revision = self.fixture()
        (cache / ".git/info/exclude").write_text("hidden.py\n")
        (cache / "hidden.py").write_text("# injected\n")
        with self.assertRaisesRegex(ValueError, "modifications"):
            bootstrap.validate_cache(cache, revision)
        (cache / "hidden.py").unlink()
        bootstrap.git(cache, "update-index", "--assume-unchanged", "common/render_lint.py")
        (cache / "common/render_lint.py").write_text("# injected\n")
        with self.assertRaisesRegex(ValueError, "hidden index"):
            bootstrap.validate_cache(cache, revision)

    def test_wrong_commit_or_origin_rejected(self):
        cache, revision = self.fixture()
        with self.assertRaisesRegex(ValueError, "revision"):
            bootstrap.validate_cache(cache, "f" * 40)
        bootstrap.git(cache, "remote", "set-url", "origin", "https://example.invalid/other.git")
        with self.assertRaisesRegex(ValueError, "repository"):
            bootstrap.validate_cache(cache, revision)

    def test_symlink_cache_and_root_rejected(self):
        cache, revision = self.fixture()
        alias = self.root / "alias"
        alias.symlink_to(cache, target_is_directory=True)
        with self.assertRaises(ValueError):
            bootstrap.validate_cache(alias, revision)
        shutil.rmtree(self.cache_root)
        self.cache_root.symlink_to(self.root, target_is_directory=True)
        with self.assertRaises(ValueError):
            bootstrap.source_dir(self.root, {"revision": revision})

    def test_incomplete_source_rejected(self):
        cache, revision = self.fixture()
        (cache / "common/render_lint.py").unlink()
        bootstrap.git(cache, "add", "-u")
        bootstrap.git(cache, "commit", "--quiet", "-m", "Remove entrypoint")
        revision = bootstrap.git(cache, "rev-parse", "HEAD")
        with self.assertRaisesRegex(ValueError, "incomplete"):
            bootstrap.validate_cache(cache, revision)

    def test_local_source_is_explicit_and_validated(self):
        self.assertEqual(bootstrap.source_dir(self.root, {}, COMMON), COMMON)
        with self.assertRaises(ValueError):
            bootstrap.source_dir(self.root, {}, self.root)

    def test_local_source_rejected_in_privileged_event_contexts(self):
        for event in ("pull_request_target", "workflow_run", "issue_comment", ""):
            with self.subTest(event=event), mock.patch.dict(os.environ, {"GITHUB_ACTIONS": "true", "GITHUB_EVENT_NAME": event}):
                with self.assertRaisesRegex(ValueError, "ordinary"):
                    bootstrap.source_dir(self.root, {}, COMMON)
        for event in ("push", "pull_request"):
            with mock.patch.dict(os.environ, {"GITHUB_ACTIONS": "true", "GITHUB_EVENT_NAME": event}):
                self.assertEqual(bootstrap.source_dir(self.root, {}, COMMON), COMMON)

    def test_download_verifies_pins_and_rolls_back_without_overwriting_cache(self):
        fixture, first = self.fixture()
        remote = self.root / "offline-remote"
        fixture.rename(remote)
        (remote / "common/render_lint.py").write_text("# updated rule\n")
        bootstrap.git(remote, "add", ".")
        bootstrap.git(remote, "commit", "--quiet", "-m", "Update rule")
        second = bootstrap.git(remote, "rev-parse", "HEAD")
        real_git = bootstrap.git
        fetch_calls = []
        def offline_fetch(directory, *args):
            if args[0] == "fetch":
                fetch_calls.append(args)
                self.assertEqual(args[1:4], ("--quiet", "--depth=1", "origin"))
                return real_git(directory, "-c", "protocol.file.allow=always", "fetch", "--quiet", "--depth=1", remote.as_uri(), args[-1])
            return real_git(directory, *args)
        with mock.patch.object(bootstrap, "git", side_effect=offline_fetch):
            old = bootstrap.source_dir(self.root, {"revision": first})
            new = bootstrap.source_dir(self.root, {"revision": second})
            rolled_back = bootstrap.source_dir(self.root, {"revision": first})
        self.assertEqual(rolled_back, old)
        self.assertEqual(len(fetch_calls), 2)
        self.assertEqual((old / "render_lint.py").read_text(), "# fixture\n")
        self.assertEqual((new / "render_lint.py").read_text(), "# updated rule\n")
        self.assertEqual({path.name for path in self.cache_root.iterdir()}, {first, second})

    def test_failed_download_has_timeout_and_cleans_staging(self):
        def fail(root, *args):
            if args[0] == "fetch":
                raise ValueError("offline")
            return ""
        with mock.patch.object(bootstrap, "git", side_effect=fail):
            with self.assertRaisesRegex(ValueError, "offline"):
                bootstrap.source_dir(self.root, {"revision": "a" * 40})
        self.assertEqual(list(self.cache_root.iterdir()), [])

    def test_git_uses_fixed_arguments_bounded_process_and_sanitized_environment(self):
        with mock.patch.dict(os.environ, {"GIT_CONFIG_COUNT": "1", "GIT_SSL_NO_VERIFY": "true"}):
            with mock.patch.object(subprocess, "run", return_value=mock.Mock(stdout="ok\n")) as run:
                self.assertEqual(bootstrap.git(self.root, "fetch", "origin", "a" * 40), "ok")
                args, options = run.call_args
                self.assertEqual(args[0][-3:], ["fetch", "origin", "a" * 40])
                self.assertNotIn("shell", options)
                self.assertEqual(options["timeout"], 120)
                self.assertNotIn("GIT_CONFIG_COUNT", options["env"])
                self.assertNotIn("GIT_SSL_NO_VERIFY", options["env"])
                self.assertEqual(options["env"]["GIT_TERMINAL_PROMPT"], "0")

    def test_git_error_does_not_expose_stderr(self):
        failure = subprocess.CalledProcessError(1, ["git"], stderr="SECRET_VALUE")
        with mock.patch.object(subprocess, "run", side_effect=failure):
            with self.assertRaises(ValueError) as caught:
                bootstrap.git(self.root, "fetch", "origin", "a" * 40)
        self.assertNotIn("SECRET_VALUE", str(caught.exception))


if __name__ == "__main__":
    unittest.main()
