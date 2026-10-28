"""Exercise immutable SQL selection and safe argument passing with real Git history."""
import importlib.util
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch


COMMON = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("shared_lint", COMMON / "lint.py")
LINT = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(LINT)


class MigrationCommands(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.git("init", "-q")
        self.git("config", "user.email", "test@example.invalid")
        self.git("config", "user.name", "Test")
        (self.root / "migrations").mkdir()
        (self.root / "migrations/001.up.sql").write_text("CREATE TABLE one (id bigint);\n")
        self.commit()
        self.base = self.git("rev-parse", "HEAD")

    def git(self, *args):
        return subprocess.run(["git", *args], cwd=self.root, check=True,
                              capture_output=True, text=True, timeout=10).stdout.strip()

    def commit(self):
        self.git("add", ".")
        self.git("commit", "-qm", "Fixture")

    def test_no_migration_does_not_invoke_squawk(self):
        with patch.object(LINT, "run", wraps=LINT.run) as run:
            LINT.migrations(self.root, self.base, self.root / "config", "2.70.0")
        self.assertEqual(len(run.call_args_list), 2)

    def test_added_up_files_are_passed_as_individual_arguments(self):
        name = "migrations/002 $(touch injected).up.sql"
        (self.root / name).write_text("CREATE TABLE two (id bigint);\n")
        (self.root / "migrations/002.down.sql").write_text("DROP TABLE two;\n")
        self.commit()
        real_run = LINT.run
        calls = []

        def run(argv, root, **kwargs):
            if argv[0] == "npx":
                calls.append(argv)
            else:
                return real_run(argv, root, **kwargs)

        with patch.object(LINT, "run", side_effect=run):
            LINT.migrations(self.root, self.base, self.root / "config", "2.70.0")
        self.assertEqual(calls, [["npx", "--yes", "squawk-cli@2.70.0", "--config",
                                  str(self.root / "config"), "--", name]])
        self.assertFalse((self.root / "injected").exists())

    def test_changed_deleted_and_renamed_old_migrations_fail(self):
        original = self.root / "migrations/001.up.sql"
        for change in ("modify", "delete", "rename"):
            with self.subTest(change=change):
                self.git("reset", "--hard", self.base)
                if change == "modify":
                    original.write_text("CREATE TABLE other (id bigint);\n")
                elif change == "delete":
                    original.unlink()
                else:
                    original.rename(self.root / "migrations/renamed.up.sql")
                self.commit()
                with self.assertRaisesRegex(ValueError, "Existing migrations"):
                    LINT.migrations(self.root, self.base, self.root / "config", "2.70.0")

    def test_missing_or_option_base_fails(self):
        with self.assertRaises(subprocess.CalledProcessError):
            LINT.changed_files(self.root, "missing-base", "A", "migrations/*.sql")
        for base in ("", "--output=bad", "x\x00y"):
            with self.assertRaises(ValueError):
                LINT.changed_files(self.root, base, "A", "migrations/*.sql")


if __name__ == "__main__":
    unittest.main()
