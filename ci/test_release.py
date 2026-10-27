import os
import re
import subprocess
import unittest
from unittest.mock import patch

import release


class ReleaseVersionTests(unittest.TestCase):
    revision = "a" * 40

    def test_github_tag_is_the_version_source(self):
        with patch.dict(os.environ, {"GITHUB_REF_TYPE": "tag", "GITHUB_REF_NAME": "v0.1.0"}, clear=True):
            with patch.object(release, "command", return_value=self.revision) as command:
                self.assertEqual(release.release_version(self.revision), "0.1.0")
                command.assert_called_once_with("git", "rev-parse", "refs/tags/v0.1.0^{commit}")

    def test_local_tag_is_the_version_source(self):
        with patch.dict(os.environ, {}, clear=True):
            with patch.object(release, "command", side_effect=["latest\nv2.3.4", self.revision]):
                self.assertEqual(release.release_version(self.revision), "2.3.4")

    def test_rejects_invalid_github_tags(self):
        for tag in ["", "0.1.0", "v01.1.0", "v0.1", "v0.1.0-rc.1", "v0.1.0+build", "v0.1.0\n", "v1٢.0.0"]:
            with self.subTest(tag=tag):
                with patch.dict(os.environ, {"GITHUB_REF_TYPE": "tag", "GITHUB_REF_NAME": tag}, clear=True):
                    with self.assertRaisesRegex(ValueError, "invalid release tag"):
                        release.release_version(self.revision)

    def test_rejects_branch_builds(self):
        with patch.dict(os.environ, {"GITHUB_REF_TYPE": "branch", "GITHUB_REF_NAME": "main"}, clear=True):
            with self.assertRaisesRegex(ValueError, "not a branch"):
                release.release_version(self.revision)

    def test_rejects_missing_or_ambiguous_local_tags(self):
        for tags in ["", "latest", "v0.1.0\nv0.2.0"]:
            with self.subTest(tags=tags):
                with patch.dict(os.environ, {}, clear=True):
                    with patch.object(release, "command", return_value=tags):
                        with self.assertRaisesRegex(ValueError, "exactly one"):
                            release.release_version(self.revision)

    def test_rejects_a_tag_pointing_to_another_commit(self):
        with patch.dict(os.environ, {"GITHUB_REF_TYPE": "tag", "GITHUB_REF_NAME": "v0.1.0"}, clear=True):
            with patch.object(release, "command", return_value="b" * 40):
                with self.assertRaisesRegex(ValueError, "must point to HEAD"):
                    release.release_version(self.revision)

    def test_snapshot_needs_neither_a_tag_nor_a_commit(self):
        toolchain = "go" + re.search(r"^go (\S+)$", (release.ROOT / "go.mod").read_text(), re.MULTILINE)[1]
        no_head = subprocess.CompletedProcess([], 1, stdout="")
        with patch.object(release, "command", return_value=toolchain):
            with patch.object(release.subprocess, "run", return_value=no_head):
                with patch.object(release, "release_version", side_effect=AssertionError("unexpected tag lookup")):
                    metadata = release.release_metadata(snapshot=True)
        self.assertEqual(metadata["version"], "dev")
        self.assertEqual(metadata["revision"], "uncommitted")

    def test_release_metadata_uses_the_tag_without_a_version_file(self):
        toolchain = "go" + re.search(r"^go (\S+)$", (release.ROOT / "go.mod").read_text(), re.MULTILINE)[1]
        head = subprocess.CompletedProcess([], 0, stdout=self.revision)
        with patch.dict(os.environ, {"GITHUB_REF_TYPE": "tag", "GITHUB_REF_NAME": "v3.2.1"}, clear=True):
            with patch.object(release, "command", side_effect=[toolchain, "1700000000", "", self.revision]):
                with patch.object(release.subprocess, "run", return_value=head):
                    metadata = release.release_metadata(snapshot=False)
        self.assertEqual(metadata["version"], "3.2.1")
        self.assertEqual(metadata["revision"], self.revision)
        self.assertFalse(metadata["snapshot"])


if __name__ == "__main__":
    unittest.main()
