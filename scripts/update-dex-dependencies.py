#!/usr/bin/env python3

import argparse
import json
import os
import re
import subprocess
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SDK_MODULE = "github.com/superdurable/dex/sdk-go"
SEMVER = re.compile(r"^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$")


def fail(message: str) -> None:
    raise SystemExit(message)


def canonical_version(value: str, label: str) -> tuple[str, tuple[int, int, int]]:
    match = SEMVER.fullmatch(value.strip())
    if match is None:
        fail(f"{label} must be a stable semantic version, got: {value}")
    parts = tuple(int(part) for part in match.groups())
    return f"v{parts[0]}.{parts[1]}.{parts[2]}", parts


def component_tag(
    value: str, prefix: str, label: str
) -> tuple[str, str, tuple[int, int, int]]:
    if not value.startswith(prefix):
        fail(f"{label} must start with {prefix}, got: {value}")
    version, parts = canonical_version(value.removeprefix(prefix), label)
    return f"{prefix}{version}", version, parts


def replace_required(path: Path, old: str, new: str) -> None:
    content = path.read_text()
    if old not in content:
        fail(f"{path.relative_to(ROOT)} is missing expected value: {old}")
    path.write_text(content.replace(old, new))


def write_outputs(values: dict[str, str]) -> None:
    output_path = os.environ.get("GITHUB_OUTPUT")
    if output_path:
        with Path(output_path).open("a") as output:
            for key, value in values.items():
                output.write(f"{key}={value}\n")
    print(json.dumps(values, sort_keys=True))


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-version", required=True)
    parser.add_argument("--server-tag", required=True)
    parser.add_argument("--cli-tag", required=True)
    arguments = parser.parse_args()

    latest_sdk, latest_sdk_parts = canonical_version(
        arguments.sdk_version, "Dex Go SDK version"
    )
    latest_server_tag, latest_server, latest_server_parts = component_tag(
        arguments.server_tag, "server/", "Dex Server tag"
    )
    latest_cli_tag, latest_cli, latest_cli_parts = component_tag(
        arguments.cli_tag, "cli-", "Dex CLI tag"
    )

    go_mod = (ROOT / "go.mod").read_text()
    sdk_match = re.search(
        rf"(?m)^\s*{re.escape(SDK_MODULE)}\s+(v\d+\.\d+\.\d+)\s*$",
        go_mod,
    )
    if sdk_match is None:
        fail(f"go.mod does not pin {SDK_MODULE} to a stable version")
    current_sdk, current_sdk_parts = canonical_version(
        sdk_match.group(1), "current Dex Go SDK version"
    )
    current_server_tag, current_server, current_server_parts = component_tag(
        (ROOT / "DEX_SERVER_BASELINE").read_text().strip(),
        "server/",
        "current Dex Server tag",
    )
    current_cli_tag, current_cli, current_cli_parts = component_tag(
        (ROOT / "DEX_CLI_BASELINE").read_text().strip(),
        "cli-",
        "current Dex CLI tag",
    )

    sdk_changed = latest_sdk_parts > current_sdk_parts
    server_changed = latest_server_parts > current_server_parts
    cli_changed = latest_cli_parts > current_cli_parts
    contract_path = ROOT / "internal" / "templatecontract" / "contract_test.go"
    readme_path = ROOT / "README.md"

    if sdk_changed:
        subprocess.run(
            ["go", "mod", "edit", f"-require={SDK_MODULE}@{latest_sdk}"],
            cwd=ROOT,
            check=True,
        )
        replace_required(readme_path, f"SDK `{current_sdk}`", f"SDK `{latest_sdk}`")
        replace_required(
            contract_path,
            f"{SDK_MODULE} {current_sdk}",
            f"{SDK_MODULE} {latest_sdk}",
        )

    if server_changed:
        (ROOT / "DEX_SERVER_BASELINE").write_text(latest_server_tag + "\n")
        replace_required(
            readme_path,
            f"Dex Server `{current_server}`",
            f"Dex Server `{latest_server}`",
        )
        replace_required(contract_path, current_server_tag, latest_server_tag)

    if cli_changed:
        (ROOT / "DEX_CLI_BASELINE").write_text(latest_cli_tag + "\n")
        replace_required(
            readme_path,
            f"Dex CLI `{current_cli}`",
            f"Dex CLI `{latest_cli}`",
        )
        replace_required(contract_path, current_cli_tag, latest_cli_tag)

    changed = sdk_changed or server_changed or cli_changed
    manifest_path = ROOT / ".superverse" / "template.json"
    manifest = json.loads(manifest_path.read_text())
    current_template, current_template_parts = canonical_version(
        manifest.get("templateVersion", ""), "template version"
    )
    next_template = current_template
    if changed:
        next_template = (
            f"v{current_template_parts[0]}.{current_template_parts[1]}."
            f"{current_template_parts[2] + 1}"
        )
        manifest["templateVersion"] = next_template.removeprefix("v")
        manifest_path.write_text(json.dumps(manifest, indent=2) + "\n")
        replace_required(
            contract_path,
            f'TemplateVersion != "{current_template.removeprefix("v")}"',
            f'TemplateVersion != "{next_template.removeprefix("v")}"',
        )

    write_outputs(
        {
            "changed": str(changed).lower(),
            "sdk_previous": current_sdk,
            "sdk_latest": latest_sdk,
            "server_previous": current_server_tag,
            "server_latest": latest_server_tag,
            "cli_previous": current_cli_tag,
            "cli_latest": latest_cli_tag,
            "template_previous": current_template.removeprefix("v"),
            "template_latest": next_template.removeprefix("v"),
        }
    )


if __name__ == "__main__":
    main()
