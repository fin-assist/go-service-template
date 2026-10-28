"""Run the actual pinned linters against accepted and rejected Go/SQL fixtures."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

COMMON = Path(__file__).resolve().parent


def run(argv, root, accepted=True, expected="", env=None):
    result = subprocess.run(argv, cwd=root, capture_output=True, text=True, timeout=600, env=env)
    output = result.stdout + result.stderr
    if (result.returncode == 0) != accepted or expected not in output:
        raise RuntimeError("Probe failed: " + repr(argv) + "\n" + output)


def manifest(root, profile, smallint=False):
    (root / ".finassist-ci.json").write_text(json.dumps({
        "revision": "0" * 40, "profile": profile, "gosec_g115": False,
        "squawk_smallint": smallint}))


def render(root, env=None):
    run([sys.executable, str(COMMON / "render_lint.py"), "--root", str(root)], root, env=env)


def main():
    versions = json.loads((COMMON / "versions.json").read_text())
    with tempfile.TemporaryDirectory(prefix="finassist-lint-probe-") as directory:
        root = Path(directory)
        (root / "go.mod").write_text("module example.com/lintprobe\n\ngo 1.26.0\n")
        source = root / "probe.go"
        for profile in ("service", "library"):
            manifest(root, profile)
            source.write_text("package lintprobe\n\nfunc Value() int { return 42 }\n")
            render(root)
            config = root / ".finassist-ci/generated/.golangci.yml"
            lint = ["go", "run", "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@" +
                    versions["golangci-lint"], "run", "--config", str(config), "./..."]
            run(lint, root)
            # All profiles forbid a framework import. A local replacement proves depguard
            # without downloading or executing a third-party implementation.
            (root / "forbidden").mkdir(exist_ok=True)
            (root / "forbidden/go.mod").write_text("module github.com/gin-gonic/gin\n\ngo 1.26.0\n")
            (root / "forbidden/gin.go").write_text("package gin\n\nfunc Value() int { return 1 }\n")
            with (root / "go.mod").open("a") as module:
                module.write("\nrequire github.com/gin-gonic/gin v1.0.0\nreplace github.com/gin-gonic/gin => ./forbidden\n")
            source.write_text('package lintprobe\n\nimport "github.com/gin-gonic/gin"\n\nfunc Value() int { return gin.Value() }\n')
            run(lint, root, accepted=False, expected="depguard")
            (root / "go.mod").write_text("module example.com/lintprobe\n\ngo 1.26.0\n")
        sql = root / "probe.sql"
        sql.write_text("CREATE TABLE lint_probe (id bigint PRIMARY KEY);\n")
        manifest(root, "service")
        render(root)
        squawk = ["npx", "--yes", "squawk-cli@" + versions["squawk"], "--config",
                  str(root / ".finassist-ci/generated/.squawk.toml"), str(sql)]
        run(squawk, root)
        sql.write_text("CREATE TABLE lint_probe (id smallint PRIMARY KEY);\n")
        run(squawk, root, accepted=False, expected="prefer-bigint-over-smallint")
        # Only the approved repository can render the exception; the linter accepts it.
        env = dict(os.environ, GITHUB_ACTIONS="true", GITHUB_REPOSITORY="fin-assist/bank-statement-data")
        manifest(root, "service", smallint=True)
        render(root, env)
        run(squawk, root)
    print("Actual Go/SQL lint probes passed: both profiles, forbidden import, restricted smallint override.")


if __name__ == "__main__":
    main()
