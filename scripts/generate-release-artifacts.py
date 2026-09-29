#!/usr/bin/env python3
# Copyright (c) 2026 Super Durable
# SPDX-License-Identifier: MIT

"""Generate immutable Superverse release contracts from checked-in application sources."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile


ROOT = Path(__file__).resolve().parents[1]
DEX_APP = ROOT / "dex-app.yaml"
DEX_APP_SCHEMA = "superverse.dev/dex-app/v1"
BUNDLE_SCHEMA = "superverse.dev/flow-definition-bundle/v1"
CONNECTOR_CONTRACT_SCHEMA = "superverse.dev/connector-contract/v1"
ENVIRONMENT_CONTRACT_SCHEMA = "superverse.dev/environment-contract/v1"
CONNECTOR_KEYS = {
    "connectionName",
    "connectorId",
    "version",
    "authMethodId",
    "operations",
}


def main() -> None:
    arguments = parse_arguments()
    release_id = required_environment("SUPERVERSE_RELEASE_ID")
    project_id = required_environment("SUPERVERSE_PROJECT_ID")
    source_commit_sha = required_environment("SUPERVERSE_SOURCE_COMMIT_SHA")
    build_profile_digest = required_environment("SUPERVERSE_BUILD_PROFILE_DIGEST")
    manifest = load_manifest()
    output_directory = arguments.output_directory.resolve()
    output_directory.mkdir(parents=True, exist_ok=True)
    definitions = render_flow_definitions(manifest, arguments.dexcli)
    write_json(output_directory / "flow-definitions.json", {
        "schemaVersion": BUNDLE_SCHEMA,
        "releaseId": release_id,
        "projectId": project_id,
        "sourceCommitSha": source_commit_sha,
        "buildProfileDigest": build_profile_digest,
        "flowDefinitions": definitions,
    })
    write_json(output_directory / "connector-contract.json", {
        "schemaVersion": CONNECTOR_CONTRACT_SCHEMA,
        "releaseId": release_id,
        "projectId": project_id,
        "connections": connector_contract(manifest),
    })
    write_json(output_directory / "environment-contract.json", {
        "schemaVersion": ENVIRONMENT_CONTRACT_SCHEMA,
        "releaseId": release_id,
        "projectId": project_id,
        "application": environment_contract(manifest),
    })
    (output_directory / "dex-app.yaml").write_bytes(DEX_APP.read_bytes())


def parse_arguments() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--output-directory", type=Path, required=True)
    parser.add_argument("--dexcli", default=os.environ.get("DEXCLI", "dexcli"))
    return parser.parse_args()


def required_environment(name: str) -> str:
    value = os.environ.get(name, "").strip()
    if not value:
        raise SystemExit(f"{name} is required")
    return value


def load_manifest() -> dict[str, object]:
    try:
        manifest = json.loads(DEX_APP.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as failure:
        raise SystemExit("dex-app.yaml must contain valid JSON-compatible YAML") from failure
    if not isinstance(manifest, dict) or manifest.get("schemaVersion") != DEX_APP_SCHEMA:
        raise SystemExit("dex-app.yaml has an unsupported schemaVersion")
    flow_definitions = manifest.get("flowDefinitions")
    connectors = manifest.get("connectors")
    application = manifest.get("application")
    if not isinstance(flow_definitions, list) or not flow_definitions:
        raise SystemExit("dex-app.yaml must declare at least one Flow Definition")
    if not isinstance(connectors, list) or not isinstance(application, dict):
        raise SystemExit("dex-app.yaml connectors and application must be declared")
    return manifest


def render_flow_definitions(
    manifest: dict[str, object],
    dexcli: str,
) -> list[dict[str, object]]:
    definitions: list[dict[str, object]] = []
    flow_types: set[str] = set()
    for entry in manifest["flowDefinitions"]:
        if not isinstance(entry, dict) or set(entry) != {"sourcePath"}:
            raise SystemExit("each Flow Definition entry must contain only sourcePath")
        source_path = safe_relative_path(entry.get("sourcePath"), "Flow Definition sourcePath")
        source = ROOT / source_path
        if not source.is_file() or source.suffix != ".go":
            raise SystemExit(f"Flow Definition source does not exist: {source_path}")
        with tempfile.TemporaryDirectory(prefix="release-fdg-") as temporary:
            output_base = Path(temporary) / "flow"
            result = subprocess.run(
                [
                    dexcli,
                    "visualize",
                    source_path,
                    "--schema-version",
                    "2.0",
                    "--json",
                    "--out",
                    str(output_base),
                ],
                cwd=ROOT,
                check=False,
                capture_output=True,
                text=True,
            )
            if result.returncode != 0:
                raise SystemExit(
                    f"FDG rendering failed for {source_path}: "
                    + (result.stderr or result.stdout).strip()
                )
            try:
                graph = json.loads(output_base.with_suffix(".json").read_text(encoding="utf-8"))
            except (OSError, json.JSONDecodeError) as failure:
                raise SystemExit(f"FDG renderer returned invalid JSON for {source_path}") from failure
        if (not isinstance(graph, dict)
                or graph.get("schemaVersion") != "2.0"
                or graph.get("valid") is not True
                or graph.get("diagnostics")):
            raise SystemExit(f"FDG 2.0 graph is invalid for {source_path}")
        flow = graph.get("flow")
        flow_type = flow.get("name") if isinstance(flow, dict) else None
        if not isinstance(flow_type, str) or not flow_type or flow_type in flow_types:
            raise SystemExit("Flow Types must be non-empty and unique")
        flow_types.add(flow_type)
        definitions.append({
            "flowType": flow_type,
            "sourcePath": source_path,
            "schemaVersion": "2.0",
            "digest": "sha256:" + sha256(canonical_json(graph)),
            "valid": True,
            "graph": graph,
        })
    return sorted(definitions, key=lambda definition: definition["flowType"])


def connector_contract(manifest: dict[str, object]) -> list[dict[str, object]]:
    connections: list[dict[str, object]] = []
    connection_names: set[str] = set()
    for entry in manifest["connectors"]:
        if not isinstance(entry, dict) or set(entry) != CONNECTOR_KEYS:
            raise SystemExit("each connector entry must use the exact connector contract fields")
        for key in ("connectionName", "connectorId", "version", "authMethodId"):
            if not isinstance(entry.get(key), str) or not entry[key]:
                raise SystemExit(f"connector {key} must be a non-empty string")
        operations = entry.get("operations")
        if (not isinstance(operations, list)
                or not operations
                or any(not isinstance(value, str) or not value for value in operations)):
            raise SystemExit("connector operations must be a non-empty string list")
        if entry["connectionName"] in connection_names:
            raise SystemExit("connector connectionName values must be unique")
        connection_names.add(entry["connectionName"])
        connections.append({**entry, "operations": sorted(set(operations))})
    return sorted(connections, key=lambda connection: connection["connectionName"])


def environment_contract(manifest: dict[str, object]) -> dict[str, object]:
    application = manifest["application"]
    if set(application) != {"port", "healthPath"}:
        raise SystemExit("dex-app.yaml application must contain only port and healthPath")
    port = application.get("port")
    health_path = application.get("healthPath")
    if not isinstance(port, int) or isinstance(port, bool) or port < 1 or port > 65535:
        raise SystemExit("application port must be between 1 and 65535")
    if (not isinstance(health_path, str)
            or not health_path.startswith("/")
            or "?" in health_path
            or "#" in health_path):
        raise SystemExit("application healthPath must be an absolute path")
    return {
        "port": port,
        "healthPath": health_path,
        "publicBaseUrlRequired": True,
        "connectorConfigurationRequired": bool(manifest["connectors"]),
    }


def safe_relative_path(value: object, label: str) -> str:
    if not isinstance(value, str) or not value:
        raise SystemExit(f"{label} must be a non-empty string")
    path = Path(value)
    if path.is_absolute() or ".." in path.parts or path.as_posix() != value:
        raise SystemExit(f"{label} must be a normalized repository-relative path")
    return value


def canonical_json(value: object) -> bytes:
    return json.dumps(value, ensure_ascii=False, separators=(",", ":")).encode("utf-8")


def sha256(contents: bytes) -> str:
    return hashlib.sha256(contents).hexdigest()


def write_json(path: Path, value: object) -> None:
    path.write_bytes(canonical_json(value) + b"\n")


if __name__ == "__main__":
    main()
