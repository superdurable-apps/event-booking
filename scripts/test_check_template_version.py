import importlib.util
from pathlib import Path
import subprocess
import unittest
from unittest import mock


SCRIPT = Path(__file__).with_name("check-template-version.py")
SPEC = importlib.util.spec_from_file_location("check_template_version", SCRIPT)
if SPEC is None or SPEC.loader is None:
    raise RuntimeError("cannot load template version checker")
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class CheckTemplateVersionTest(unittest.TestCase):
    def test_accepts_stable_version(self) -> None:
        self.assertEqual(
            MODULE.parse_version('{"templateVersion":"1.6.0"}', "test"),
            ("1.6.0", (1, 6, 0)),
        )

    def test_rejects_prerelease(self) -> None:
        with self.assertRaisesRegex(SystemExit, "stable MAJOR.MINOR.PATCH"):
            MODULE.parse_version('{"templateVersion":"1.6.0-rc.1"}', "test")

    def test_rejects_missing_version(self) -> None:
        with self.assertRaisesRegex(SystemExit, "has no templateVersion"):
            MODULE.parse_version("{}", "test")

    def test_requires_a_strictly_newer_version(self) -> None:
        with self.assertRaisesRegex(SystemExit, "must advance beyond 1.6.0"):
            MODULE.require_advance("1.6.0", (1, 6, 0), "1.6.0", (1, 6, 0))

    def test_missing_base_manifest_is_an_initial_import(self) -> None:
        with mock.patch.object(MODULE.subprocess, "run") as run:
            run.side_effect = [
                subprocess.CompletedProcess([], 0),
                subprocess.CompletedProcess([], 1),
            ]

            self.assertIsNone(MODULE.manifest_at_revision("base-sha"))

        self.assertEqual(run.call_count, 2)

    def test_rejects_an_unresolvable_base_revision(self) -> None:
        with mock.patch.object(
            MODULE.subprocess,
            "run",
            side_effect=subprocess.CalledProcessError(128, ["git", "rev-parse"]),
        ):
            with self.assertRaisesRegex(SystemExit, "cannot resolve base revision"):
                MODULE.manifest_at_revision("missing-sha")


if __name__ == "__main__":
    unittest.main()
