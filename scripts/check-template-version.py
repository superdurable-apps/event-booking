#!/usr/bin/env python3

import argparse
import json
import os
from pathlib import Path
import re
import subprocess


ROOT = Path(__file__).resolve().parents[1]
MANIFEST = ".superverse/template.json"
STABLE_VERSION = re.compile(r"^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$")


def parse_version(contents: str, label: str) -> tuple[str, tuple[int, int, int]]:
    try:
        manifest = json.loads(contents)
    except json.JSONDecodeError as failure:
        raise SystemExit(f"{label} is not valid JSON") from failure
    if not isinstance(manifest, dict) or "templateVersion" not in manifest:
        raise SystemExit(f"{label} has no templateVersion")
    value = manifest["templateVersion"]
    if not isinstance(value, str) or STABLE_VERSION.fullmatch(value) is None:
        raise SystemExit(f"{label} templateVersion must be stable MAJOR.MINOR.PATCH")
    return value, tuple(int(part) for part in value.split("."))


def manifest_at_revision(revision: str) -> str:
    try:
        return subprocess.run(
            ["git", "show", f"{revision}:{MANIFEST}"],
            cwd=ROOT,
            check=True,
            capture_output=True,
            text=True,
        ).stdout
    except subprocess.CalledProcessError as failure:
        raise SystemExit(f"cannot read template manifest from {revision}") from failure


def write_output(name: str, value: str) -> None:
    output_path = os.environ.get("GITHUB_OUTPUT")
    if output_path:
        with Path(output_path).open("a") as output:
            output.write(f"{name}={value}\n")


def require_advance(
    current: str,
    current_parts: tuple[int, int, int],
    previous: str,
    previous_parts: tuple[int, int, int],
) -> None:
    if current_parts <= previous_parts:
        raise SystemExit(
            f"templateVersion must advance beyond {previous}; current value is {current}"
        )


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--base-ref")
    arguments = parser.parse_args()

    current, current_parts = parse_version(
        (ROOT / MANIFEST).read_text(),
        "current template manifest",
    )
    if arguments.base_ref:
        previous, previous_parts = parse_version(
            manifest_at_revision(arguments.base_ref),
            "base template manifest",
        )
        require_advance(current, current_parts, previous, previous_parts)

    write_output("version", current)
    write_output("tag", f"v{current}")
    print(f"template version v{current} is valid")


if __name__ == "__main__":
    main()
