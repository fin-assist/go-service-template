"""Shared local/CI linter commands. The caller pins this file with its configuration."""
import argparse
import json
from pathlib import Path
import subprocess
import sys


COMMON = Path(__file__).resolve().parent


def run(arguments, root, timeout=600, capture=False):
    return subprocess.run(arguments, cwd=root, check=True, timeout=timeout,
                          stdout=subprocess.PIPE if capture else None,
                          text=not capture)


def changed_files(root, base, diff_filter, pattern):
    # Separate argv and NUL-delimited paths preserve spaces and hostile filenames.
    if not base or base.startswith("-") or "\x00" in base:
        raise ValueError("invalid migration base ref")
    result = run(["git", "diff", "--name-only", "-z", "--diff-filter=" + diff_filter,
                  base + "...HEAD", "--", pattern], root, timeout=30, capture=True)
    return [p.decode("utf-8") for p in result.stdout.split(b"\x00") if p]


def migrations(root, base, config, squawk_version):
    changed = changed_files(root, base, "MDR", "migrations/*.sql")
    if changed:
        raise ValueError("Existing migrations were modified, renamed or deleted; add a new migration")
    added = changed_files(root, base, "A", "migrations/*.up.sql")
    if added:
        run(["npx", "--yes", "squawk-cli@" + squawk_version, "--config", str(config),
             "--", *added], root)
    else:
        print("No new migrations.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["lint", "fmt", "migrations"])
    parser.add_argument("--root", type=Path, default=Path.cwd())
    parser.add_argument("--base", default="origin/main")
    args = parser.parse_args()
    root = args.root.resolve()
    versions = json.loads((COMMON / "versions.json").read_text())
    run([sys.executable, str(COMMON / "render_lint.py"), "--root", str(root)], root, timeout=30)
    configs = root / ".finassist-ci/generated"
    if args.command == "migrations":
        migrations(root, args.base, configs / ".squawk.toml", versions["squawk"])
    else:
        command = "run" if args.command == "lint" else "fmt"
        run(["go", "run", "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@" +
             versions["golangci-lint"], command, "--config", str(configs / ".golangci.yml"), "./..."], root)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, subprocess.SubprocessError) as error:
        print("Shared lint failed: " + str(error), file=sys.stderr)
        sys.exit(1)
