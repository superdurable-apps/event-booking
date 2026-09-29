#!/usr/bin/env python3
# Copyright (c) 2026 Super Durable
# SPDX-License-Identifier: MIT

from __future__ import annotations

import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock


SCRIPT = Path(__file__).with_name("generate-release-artifacts.py")
ROOT = SCRIPT.parents[1]


class GenerateReleaseArtifactsTest(unittest.TestCase):
    def test_generates_release_bound_contracts_from_a_real_fdg(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary)
            environment = os.environ.copy()
            environment.update({
                "SUPERVERSE_RELEASE_ID": "11111111-1111-4111-8111-111111111111",
                "SUPERVERSE_PROJECT_ID": "22222222-2222-4222-8222-222222222222",
                "SUPERVERSE_SOURCE_COMMIT_SHA": "a" * 40,
                "SUPERVERSE_BUILD_PROFILE_DIGEST": "b" * 64,
            })
            result = subprocess.run(
                ["python3", str(SCRIPT), "--output-directory", str(output)],
                cwd=ROOT,
                env=environment,
                check=False,
                capture_output=True,
                text=True,
            )

            self.assertEqual(result.returncode, 0, result.stderr)
            bundle = json.loads((output / "flow-definitions.json").read_text(encoding="utf-8"))
            self.assertEqual(bundle["schemaVersion"], "superverse.dev/flow-definition-bundle/v1")
            self.assertEqual(bundle["releaseId"], environment["SUPERVERSE_RELEASE_ID"])
            self.assertEqual(bundle["projectId"], environment["SUPERVERSE_PROJECT_ID"])
            self.assertEqual(
                [definition["flowType"] for definition in bundle["flowDefinitions"]],
                ["process.EventInventoryFlow", "process.RegistrationFlow"],
            )
            for definition in bundle["flowDefinitions"]:
                self.assertTrue(definition["valid"])
                self.assertEqual(definition["graph"]["schemaVersion"], "2.0")
                self.assertRegex(definition["digest"], r"^sha256:[0-9a-f]{64}$")
            connector_contract = json.loads(
                (output / "connector-contract.json").read_text(encoding="utf-8")
            )
            self.assertEqual(
                [connection["connectionName"] for connection in connector_contract["connections"]],
                ["event-payments", "event-tickets"],
            )
            environment_contract = json.loads(
                (output / "environment-contract.json").read_text(encoding="utf-8")
            )
            self.assertTrue(
                environment_contract["application"]["connectorConfigurationRequired"]
            )
            self.assertEqual(
                (output / "dex-app.yaml").read_bytes(),
                (ROOT / "dex-app.yaml").read_bytes(),
            )

    def test_rejects_a_missing_release_identity(self) -> None:
        with tempfile.TemporaryDirectory() as temporary, mock.patch.dict(
            os.environ,
            {
                "SUPERVERSE_RELEASE_ID": "",
                "SUPERVERSE_PROJECT_ID": "",
                "SUPERVERSE_SOURCE_COMMIT_SHA": "",
                "SUPERVERSE_BUILD_PROFILE_DIGEST": "",
            },
            clear=False,
        ):
            result = subprocess.run(
                ["python3", str(SCRIPT), "--output-directory", temporary],
                cwd=ROOT,
                env=os.environ.copy(),
                check=False,
                capture_output=True,
                text=True,
            )

            self.assertNotEqual(result.returncode, 0)
            self.assertIn("SUPERVERSE_RELEASE_ID is required", result.stderr)


if __name__ == "__main__":
    unittest.main()
