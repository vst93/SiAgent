#!/usr/bin/env python3
"""Check numeric ordering, tag reservation, explicit tags and rerun stability."""
import importlib.util
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

spec = importlib.util.spec_from_file_location(
    "release_version", Path(__file__).resolve().parents[1] / "scripts/release-version.py")
version = importlib.util.module_from_spec(spec)
spec.loader.exec_module(version)


class ReleaseVersionTests(unittest.TestCase):
    def test_patch_selection(self):
        self.assertEqual(version.next_version([]), "v0.0.1")
        self.assertEqual(version.next_version(["v1.9.9", "v1.10.2", "other"]), "v1.10.3")
        self.assertEqual(version.next_version(["v2.0.0-rc.1", "v1.9.9"]), "v2.0.1")
        self.assertEqual(version.next_version(["v01.2.3", "v1.2", "unrelated"]), "v0.0.1")

    def test_git_resolution(self):
        previous = os.getcwd()
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            subprocess.run(["git", "init", "--bare", str(root / "origin")], check=True, capture_output=True)
            subprocess.run(["git", "init", str(root / "work")], check=True, capture_output=True)
            os.chdir(root / "work")
            try:
                version.git("config", "user.name", "Release test")
                version.git("config", "user.email", "release@example.invalid")
                version.git("remote", "add", "origin", str(root / "origin"))
                version.git("commit", "--allow-empty", "-m", "initial")
                first = version.git("rev-parse", "HEAD")
                self.assertEqual(version.resolve_release("", "workflow_dispatch", "main"), ("v0.0.1", first))
                version.git("commit", "--allow-empty", "-m", "next")
                second = version.git("rev-parse", "HEAD")
                self.assertEqual(version.resolve_release("v0.0.1", "workflow_dispatch", "main"), ("v0.0.1", first))
                self.assertEqual(version.resolve_release("", "workflow_dispatch", "main"), ("v0.0.2", second))
                self.assertEqual(version.resolve_release("v1.0.0", "workflow_dispatch", "main"), ("v1.0.0", second))
                self.assertEqual(version.resolve_release("ignored", "push", "v0.0.1"), ("v0.0.1", first))
                with self.assertRaises(ValueError):
                    version.resolve_release("--invalid", "workflow_dispatch", "main")
                with self.assertRaises(ValueError):
                    version.resolve_release("", "push", "v9.0.0")
            finally:
                os.chdir(previous)


if __name__ == "__main__":
    unittest.main()
