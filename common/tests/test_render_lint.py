"""Rendered profiles preserve baseline policy and expose only declared differences."""
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
import render_lint


class RenderTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.config = {"revision": "a" * 40, "profile": "service", "gosec_g115": False, "squawk_smallint": False}
        self.write_config()
        (self.root / "go.mod").write_text("module github.com/fin-assist/probe\n\ngo 1.26\n")

    def write_config(self):
        (self.root / bootstrap.CONFIG_NAME).write_text(json.dumps(self.config))

    def output(self, name):
        return (render_lint.render(self.root) / name).read_text()

    def test_service_baseline_only_changes_module_prefix(self):
        import re
        import yaml

        config = yaml.safe_load(self.output(".golangci.yml"))
        self.assertEqual(config["version"], "2")
        self.assertEqual(config["run"]["relative-path-mode"], "gomod")
        linters = config["linters"]
        self.assertEqual(linters["default"], "standard")
        self.assertEqual(set(linters["enable"]), {
            "bodyclose", "depguard", "errorlint", "forbidigo", "gosec", "misspell",
            "nilerr", "noctx", "rowserrcheck", "sqlclosecheck", "unconvert"})
        settings = linters["settings"]
        self.assertTrue(settings["forbidigo"]["analyze-types"])
        forbidden = settings["forbidigo"]["forbid"]
        for identifier in ("status.Error", "status.Errorf", "status.New", "status.Newf", "status.FromProto",
                           "errdetails.ErrorInfo", "grpc.Errorf"):
            with self.subTest(identifier=identifier):
                self.assertTrue(any(re.search(rule["pattern"], identifier) for rule in forbidden))
        denied = {rule["pkg"] for rule in settings["depguard"]["rules"]["main"]["deny"]}
        self.assertTrue({"github.com/gin-gonic/gin", "github.com/labstack/echo", "github.com/gofiber/fiber",
                         "github.com/go-chi/chi", "github.com/gorilla/mux", "github.com/go-kit/kit", "gorm.io",
                         "entgo.io", "github.com/jmoiron/sqlx", "go.uber.org/fx", "go.uber.org/dig",
                         "github.com/google/wire", "github.com/spf13/viper", "github.com/sirupsen/logrus",
                         "go.uber.org/zap", "github.com/rs/zerolog", "github.com/pkg/errors",
                         "github.com/stretchr/testify", "github.com/golang/mock", "go.uber.org/mock"} <= denied)
        self.assertEqual(linters["exclusions"]["rules"], [
            {"path": r"_test\.go", "linters": ["forbidigo", "gosec", "noctx"]}])
        self.assertEqual(set(config["formatters"]["enable"]), {"gofmt", "goimports"})
        self.assertEqual(config["formatters"]["settings"]["goimports"]["local-prefixes"],
                         ["github.com/fin-assist/probe"])
        self.assertNotIn("gosec", settings)
        squawk = self.output(".squawk.toml")
        self.assertEqual(re.findall(r'(?m)^pg_version\s*=\s*"([^"]+)"$', squawk), ["18.0"])
        self.assertEqual(re.findall(r"(?m)^assume_in_transaction\s*=\s*(\w+)$", squawk), ["true"])
        rules = re.search(r"(?m)^excluded_rules\s*=\s*(\[[^\n]+\])$", squawk)
        self.assertIsNotNone(rules)
        self.assertEqual(set(json.loads(rules[1])), {
            "require-lock-timeout", "require-statement-timeout", "prefer-bigint-over-int"})

    def test_library_profile_preserves_global_state_and_test_helper_rules(self):
        self.config["profile"] = "library"
        self.write_config()
        text = self.output(".golangci.yml")
        expected = (COMMON / "configs/golangci-library.yml").read_text().replace(
            "@@MODULE@@", "github.com/fin-assist/probe").replace("@@GOSEC_SETTINGS@@", "")
        self.assertEqual(text, expected)
        for required in ["analyze-types: true", "DefaultRegisterer", "Setenv", "net/http/pprof", "internal/pgtest", "_test\\.go"]:
            self.assertIn(required, text)
        self.assertNotIn("G115", text)

    def test_g115_is_explicit_per_profile(self):
        for profile in ("service", "library"):
            self.config.update(profile=profile, gosec_g115=True)
            self.write_config()
            self.assertIn("        - G115 # Explicit repository override.", self.output(".golangci.yml"))
            self.config["gosec_g115"] = False
            self.write_config()
            self.assertNotIn("G115", self.output(".golangci.yml"))

    def test_smallint_stays_enabled_by_default(self):
        for profile in ("service", "library"):
            self.config["profile"] = profile
            self.write_config()
            self.assertNotIn('"prefer-bigint-over-smallint"', self.output(".squawk.toml"))

    def test_smallint_only_for_exact_actions_repository(self):
        self.config["squawk_smallint"] = True
        self.write_config()
        with mock.patch.dict(os.environ, {"GITHUB_ACTIONS": "true", "GITHUB_REPOSITORY": "fin-assist/bank-statement-data"}):
            self.assertIn('"prefer-bigint-over-smallint"', self.output(".squawk.toml"))
        for repo in ("", "fin-assist/analytics", "fork/bank-statement-data", "fin-assist/bank-statement-data-evil"):
            with self.subTest(repo=repo), mock.patch.dict(os.environ, {"GITHUB_ACTIONS": "true", "GITHUB_REPOSITORY": repo}):
                with self.assertRaisesRegex(ValueError, "restricted"):
                    render_lint.render(self.root)

    def test_smallint_accepts_only_exact_local_origins(self):
        self.config["squawk_smallint"] = True
        self.write_config()
        allowed = ["https://github.com/fin-assist/bank-statement-data", "https://github.com/fin-assist/bank-statement-data.git",
                   "git@github.com:fin-assist/bank-statement-data.git", "ssh://git@github.com/fin-assist/bank-statement-data.git"]
        with mock.patch.dict(os.environ, {"GITHUB_ACTIONS": "false"}):
            for origin in allowed:
                with self.subTest(origin=origin), mock.patch.object(render_lint, "git", return_value=origin):
                    self.assertIn('"prefer-bigint-over-smallint"', self.output(".squawk.toml"))
            for origin in ["https://github.com.evil/fin-assist/bank-statement-data.git", "https://github.com/fin-assist/bank-statement-data.git/", "https://evil@github.com/fin-assist/bank-statement-data.git"]:
                with self.subTest(origin=origin), mock.patch.object(render_lint, "git", return_value=origin):
                    with self.assertRaises(ValueError):
                        render_lint.render(self.root)

    def test_missing_local_origin_cannot_enable_exception(self):
        self.config["squawk_smallint"] = True
        self.write_config()
        with mock.patch.dict(os.environ, {"GITHUB_ACTIONS": "false"}), mock.patch.object(render_lint, "git", side_effect=ValueError("absent")):
            with self.assertRaises(ValueError):
                render_lint.render(self.root)

    def test_unsafe_or_missing_module_path_rejected(self):
        for text in ('module "escape"\n', "module example.org/a\nmodule other.org/b\n", "go 1.26\n", "module a:b\n"):
            with self.subTest(text=text):
                (self.root / "go.mod").write_text(text)
                with self.assertRaisesRegex(ValueError, "module path"):
                    render_lint.render(self.root)

    def test_generated_output_is_replaced_on_policy_change(self):
        before = self.output(".golangci.yml")
        self.config["gosec_g115"] = True
        self.write_config()
        self.assertNotEqual(before, self.output(".golangci.yml"))
        self.config["gosec_g115"] = False
        self.write_config()
        self.assertEqual(before, self.output(".golangci.yml"))

    def test_output_symlinks_are_rejected(self):
        output = render_lint.render(self.root)
        file = output / ".golangci.yml"
        file.unlink()
        file.symlink_to(self.root / "go.mod")
        with self.assertRaisesRegex(ValueError, "symbolic link"):
            render_lint.render(self.root)
        file.unlink()
        shutil.rmtree(output)
        output.symlink_to(self.root, target_is_directory=True)
        with self.assertRaisesRegex(ValueError, "symbolic link"):
            render_lint.render(self.root)

    def test_copied_bootstrap_cli_source_render_and_path_modes(self):
        scripts = self.root / "scripts"
        scripts.mkdir()
        entry = scripts / "common_ci.py"
        shutil.copyfile(COMMON / "bootstrap.py", entry)
        for args in (["--source", str(COMMON), "render"], ["render", "--source", str(COMMON)]):
            result = subprocess.run([sys.executable, "-B", str(entry), *args], capture_output=True, text=True, timeout=30)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertTrue((self.root / ".finassist-ci/generated/.golangci.yml").is_file())
        result = subprocess.run([sys.executable, "-B", str(entry), "--source", str(COMMON)], capture_output=True, text=True, timeout=30)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), str(COMMON))

    def test_cli_error_returns_failure_without_rendering(self):
        result = subprocess.run([sys.executable, "-B", str(COMMON / "render_lint.py"), "--root", str(self.root / "missing")],
                                capture_output=True, text=True, timeout=30)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Shared lint configuration failed", result.stderr)


if __name__ == "__main__":
    unittest.main()
