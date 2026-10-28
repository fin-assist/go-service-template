#!/usr/bin/env python3
"""Render shared Go and SQL lint profiles for CI and local commands."""
from __future__ import annotations

import argparse
import os
from pathlib import Path
import re
import sys

sys.dont_write_bytecode = True

from bootstrap import git, read_config



def repository_name(root: Path) -> str:
    """Use the Actions repository identity, or an exact local GitHub origin."""
    if os.environ.get("GITHUB_ACTIONS") == "true":
        return os.environ.get("GITHUB_REPOSITORY", "")
    try:
        origin = git(root, "config", "--get", "remote.origin.url")
    except ValueError:
        return ""
    match = re.fullmatch(
        r"(?:https://github\.com/|git@github\.com:|ssh://git@github\.com/)(fin-assist/[A-Za-z0-9_.-]+?)(?:\.git)?",
        origin)
    return match[1] if match else ""


def render(root: Path) -> Path:
    """Render only declared overrides; retain every other shared rule unchanged."""
    root = root.resolve()
    config = read_config(root)
    if config["squawk_smallint"] and repository_name(root) != "fin-assist/bank-statement-data":
        raise ValueError("The smallint override is restricted to fin-assist/bank-statement-data.")
    matches = re.findall(r"(?m)^module\s+(\S+)\s*$", (root / "go.mod").read_text())
    if len(matches) != 1 or not re.fullmatch(r"[A-Za-z0-9._~!+/-]+", matches[0]):
        raise ValueError("go.mod must declare one valid module path.")
    configs = Path(__file__).resolve().parent / "configs"
    golangci = (configs / ("golangci-" + config["profile"] + ".yml")).read_text()
    golangci = golangci.replace("@@MODULE@@", matches[0])
    settings = "    gosec:\n      excludes:\n        - G115 # Explicit repository override.\n" if config["gosec_g115"] else ""
    golangci = golangci.replace("@@GOSEC_SETTINGS@@", settings)
    squawk = (configs / "squawk.toml").read_text().replace(
        "@@SMALLINT@@", ', "prefer-bigint-over-smallint"' if config["squawk_smallint"] else "")
    output = root / ".finassist-ci" / "generated"
    if (root / ".finassist-ci").is_symlink() or output.is_symlink():
        raise ValueError("Generated configuration directory must not be a symbolic link.")
    output.mkdir(parents=True, exist_ok=True)
    for name, content in ((".golangci.yml", golangci), (".squawk.toml", squawk)):
        target = output / name
        if target.is_symlink():
            raise ValueError("Generated configuration must not be a symbolic link.")
        target.write_text(content)
    return output


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, required=True)
    args = parser.parse_args()
    try:
        render(args.root)
    except (ValueError, OSError) as error:
        print("Shared lint configuration failed: " + str(error), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
