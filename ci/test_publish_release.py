import copy
import hashlib
import io
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import publish_release
import release


class FakeGitHub:
    """Persist remote state across interrupted publisher invocations."""

    def __init__(self, tag, remote=None):
        self.tag = tag
        self.remote = copy.deepcopy(remote)
        self.assets = []
        self.contents = {}
        self.mutations = []
        self.downloads = []
        self.fail_after_create = False
        self.fail_after_upload = None
        self.fail_after_publish = False
        self.corrupt_upload = None
        self.next_id = 100

    def get_release(self, tag):
        if tag != self.tag:
            raise AssertionError(f"Unexpected tag: {tag}")
        return copy.deepcopy(self.remote)

    def verify_tag(self, tag):
        if tag != self.tag:
            raise AssertionError(f"Unexpected tag verification: {tag}")

    def list_assets(self, release_id):
        if release_id != self.remote["id"]:
            raise AssertionError(f"Unexpected release ID: {release_id}")
        return copy.deepcopy(self.assets)

    def download_asset(self, asset_id):
        self.downloads.append(asset_id)
        return self.contents[asset_id]

    def add_asset(self, name, content, state="uploaded"):
        asset = {"id": self.next_id, "name": name, "state": state, "size": len(content)}
        self.next_id += 1
        self.assets.append(asset)
        self.contents[asset["id"]] = content
        return asset

    def create_release(self, tag, body):
        if self.remote is not None:
            raise AssertionError("Existing release must not be recreated")
        self.mutations.append(("create", tag))
        self.remote = {
            "id": 1, "tag_name": tag, "draft": True,
            "prerelease": False, "body": body,
        }
        if self.fail_after_create:
            self.fail_after_create = False
            raise RuntimeError("Create response was lost")
        return copy.deepcopy(self.remote)

    def delete_asset(self, asset_id):
        self.mutations.append(("delete", asset_id))
        self.assets = [asset for asset in self.assets if asset["id"] != asset_id]
        del self.contents[asset_id]

    def upload_asset(self, tag, path):
        if tag != self.tag:
            raise AssertionError(f"Unexpected upload tag: {tag}")
        if not self.remote["draft"]:
            raise AssertionError("Published release must not receive uploads")
        if path.name in [asset["name"] for asset in self.assets]:
            raise AssertionError("Existing assets must not be overwritten")
        self.mutations.append(("upload", path.name))
        content = path.read_bytes()
        if path.name == self.corrupt_upload:
            content = b"x" * len(content)
        self.add_asset(path.name, content)
        if path.name == self.fail_after_upload:
            self.fail_after_upload = None
            raise RuntimeError("Upload response was lost")

    def update_release(self, release_id, **fields):
        if release_id != self.remote["id"]:
            raise AssertionError(f"Unexpected release ID: {release_id}")
        self.mutations.append(("update", copy.deepcopy(fields)))
        self.remote.update(fields)
        if fields.get("draft") is False and self.fail_after_publish:
            self.fail_after_publish = False
            raise RuntimeError("Publish response was lost")
        return copy.deepcopy(self.remote)


class PublishReleaseTests(unittest.TestCase):
    tag = "v0.1.0"
    revision = "a" * 40
    image = "ghcr.io/adanomy/carla-exporter"
    digest = "sha256:" + "b" * 64

    def setUp(self):
        self.enterContext(patch("sys.stdout", io.StringIO()))
        temporary = tempfile.TemporaryDirectory(prefix="carla-release-test-")
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        for goos, goarch in release.PLATFORMS:
            suffix = ".zip" if goos == "windows" else ".tar.gz"
            name = f"carla-exporter_0.1.0_{goos}_{goarch}{suffix}"
            (self.directory / name).write_bytes(f"archive for {goos}/{goarch}\n".encode())
        self.metadata = {
            "version": "0.1.0", "revision": self.revision, "snapshot": False,
            "go_version": "go1.27.3", "source_date_epoch": 1700000000,
            "platforms": [f"{goos}/{goarch}" for goos, goarch in release.PLATFORMS],
        }
        (self.directory / "build-metadata.json").write_text(json.dumps(self.metadata) + "\n")
        (self.directory / "carla-exporter-dashboard.json").write_text('{"title":"CARLA Exporter"}\n')
        self.write_manifest()

    def write_manifest(self):
        entries = []
        for path in sorted(self.directory.iterdir()):
            if path.name != "SHA256SUMS" and path.is_file():
                entries.append(f"{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}\n")
        (self.directory / "SHA256SUMS").write_text("".join(entries))

    def publisher(self, api):
        artifacts = publish_release.load_artifacts(self.directory, self.tag, self.revision)
        return publish_release.Publisher(api, artifacts, self.tag, self.image, self.revision)

    def remote(self, draft=True):
        return {
            "id": 1, "tag_name": self.tag, "draft": draft,
            "prerelease": False, "body": "Existing release notes.\n",
        }

    def fill_remote(self, api):
        for path in self.directory.iterdir():
            api.add_asset(path.name, path.read_bytes())

    def assert_complete(self, api):
        expected = {path.name: path.read_bytes() for path in self.directory.iterdir()}
        actual = {asset["name"]: api.contents[asset["id"]] for asset in api.assets}
        self.assertEqual(actual, expected)
        self.assertFalse(api.remote["draft"])

    def test_prepare_is_read_only_when_release_does_not_exist(self):
        api = FakeGitHub(self.tag)
        self.assertFalse(self.publisher(api).prepare())
        self.assertIsNone(api.remote)
        self.assertEqual(api.mutations, [])

    def test_tag_verification_failure_prevents_remote_mutations(self):
        api = FakeGitHub(self.tag)
        for action in ("prepare", "publish"):
            with self.subTest(action=action):
                with patch.object(api, "verify_tag", side_effect=ValueError("Remote tag moved")):
                    with self.assertRaisesRegex(ValueError, "Remote tag moved"):
                        if action == "prepare":
                            self.publisher(api).prepare()
                        else:
                            self.publisher(api).publish(self.digest)
                self.assertEqual(api.mutations, [])

    def test_new_release_uploads_and_verifies_complete_bundle_before_publishing(self):
        api = FakeGitHub(self.tag)
        self.publisher(api).publish(self.digest)
        self.assert_complete(api)
        self.assertEqual(api.mutations[0], ("create", self.tag))
        self.assertEqual(api.mutations[-1][0], "update")
        self.assertIs(api.mutations[-1][1].get("draft"), False)
        self.assertEqual(len([entry for entry in api.mutations if entry[0] == "upload"]), 8)
        self.assertTrue({asset["id"] for asset in api.assets}.issubset(set(api.downloads)))
        self.assertIn(f"{self.image}@{self.digest}", api.remote["body"])

    def test_partial_draft_preserves_existing_assets_and_uploads_only_missing(self):
        api = FakeGitHub(self.tag, self.remote())
        existing = self.directory / "build-metadata.json"
        asset = api.add_asset(existing.name, existing.read_bytes())
        publisher = self.publisher(api)
        self.assertFalse(publisher.prepare())
        self.assertEqual(api.mutations, [])
        publisher.publish(self.digest)
        self.assert_complete(api)
        uploads = [entry[1] for entry in api.mutations if entry[0] == "upload"]
        self.assertNotIn(existing.name, uploads)
        self.assertEqual(len(uploads), 7)
        self.assertIn(asset["id"], [item["id"] for item in api.assets])
        self.assertFalse(any(entry[0] in ("create", "delete") for entry in api.mutations))

    def test_same_name_and_size_with_different_bytes_is_rejected_without_mutation(self):
        api = FakeGitHub(self.tag, self.remote())
        path = self.directory / "build-metadata.json"
        asset = api.add_asset(path.name, b"x" * path.stat().st_size)
        asset["digest"] = "sha256:" + hashlib.sha256(path.read_bytes()).hexdigest()
        with self.assertRaises(ValueError):
            self.publisher(api).publish(self.digest)
        self.assertEqual(api.mutations, [])
        self.assertTrue(api.remote["draft"])

    def test_published_complete_release_is_a_read_only_success(self):
        api = FakeGitHub(self.tag, self.remote(draft=False))
        self.fill_remote(api)
        publisher = self.publisher(api)
        self.assertTrue(publisher.prepare())
        publisher.publish(self.digest)
        self.assertEqual(api.mutations, [])
        self.assert_complete(api)

    def test_published_incomplete_release_is_not_modified(self):
        api = FakeGitHub(self.tag, self.remote(draft=False))
        path = self.directory / "SHA256SUMS"
        api.add_asset(path.name, path.read_bytes())
        with self.assertRaises(ValueError):
            self.publisher(api).publish(self.digest)
        self.assertEqual(api.mutations, [])

    def test_unexpected_or_duplicate_remote_assets_are_rejected(self):
        for duplicate in (False, True):
            with self.subTest(duplicate=duplicate):
                api = FakeGitHub(self.tag, self.remote())
                if duplicate:
                    path = self.directory / "SHA256SUMS"
                    api.add_asset(path.name, path.read_bytes())
                    api.add_asset(path.name, path.read_bytes())
                else:
                    api.add_asset("unexpected.txt", b"unexpected")
                with self.assertRaises(ValueError):
                    self.publisher(api).publish(self.digest)
                self.assertEqual(api.mutations, [])

    def test_incomplete_upload_is_replaced_only_in_a_draft(self):
        api = FakeGitHub(self.tag, self.remote())
        starter = api.add_asset("SHA256SUMS", b"", state="starter")
        self.assertFalse(self.publisher(api).prepare())
        self.assertEqual(api.mutations, [])
        self.publisher(api).publish(self.digest)
        self.assert_complete(api)
        self.assertIn(("delete", starter["id"]), api.mutations)
        self.assertNotIn(starter["id"], [asset["id"] for asset in api.assets])

    def test_conflict_is_detected_before_removing_incomplete_upload(self):
        api = FakeGitHub(self.tag, self.remote())
        api.add_asset("SHA256SUMS", b"", state="starter")
        api.add_asset("build-metadata.json", b"wrong content")
        with self.assertRaises(ValueError):
            self.publisher(api).publish(self.digest)
        self.assertEqual(api.mutations, [])

    def test_published_release_with_incomplete_upload_is_rejected(self):
        api = FakeGitHub(self.tag, self.remote(draft=False))
        api.add_asset("SHA256SUMS", b"", state="starter")
        with self.assertRaises(ValueError):
            self.publisher(api).prepare()
        self.assertEqual(api.mutations, [])

    def test_nonempty_starter_asset_is_not_deleted(self):
        api = FakeGitHub(self.tag, self.remote())
        api.add_asset("SHA256SUMS", b"unexpected bytes", state="starter")
        with self.assertRaises(ValueError):
            self.publisher(api).publish(self.digest)
        self.assertEqual(api.mutations, [])

    def test_upload_interruption_can_resume_without_duplicate_assets(self):
        api = FakeGitHub(self.tag)
        api.fail_after_upload = "SHA256SUMS"
        with self.assertRaisesRegex(RuntimeError, "Upload response was lost"):
            self.publisher(api).publish(self.digest)
        self.assertTrue(api.remote["draft"])
        api.mutations.clear()
        self.publisher(api).publish(self.digest)
        self.assert_complete(api)
        self.assertNotIn(("upload", "SHA256SUMS"), api.mutations)

    def test_lost_create_response_resumes_the_existing_draft(self):
        api = FakeGitHub(self.tag)
        api.fail_after_create = True
        with self.assertRaisesRegex(RuntimeError, "Create response was lost"):
            self.publisher(api).publish(self.digest)
        self.assertTrue(api.remote["draft"])
        api.mutations.clear()
        self.publisher(api).publish(self.digest)
        self.assert_complete(api)
        self.assertFalse(any(entry[0] == "create" for entry in api.mutations))

    def test_lost_publish_response_can_resume_as_read_only_success(self):
        api = FakeGitHub(self.tag)
        api.fail_after_publish = True
        with self.assertRaisesRegex(RuntimeError, "Publish response was lost"):
            self.publisher(api).publish(self.digest)
        self.assert_complete(api)
        api.mutations.clear()
        publisher = self.publisher(api)
        self.assertTrue(publisher.prepare())
        publisher.publish(self.digest)
        self.assertEqual(api.mutations, [])

    def test_uploaded_bytes_are_verified_before_draft_is_published(self):
        api = FakeGitHub(self.tag)
        api.corrupt_upload = "SHA256SUMS"
        with self.assertRaises(ValueError):
            self.publisher(api).publish(self.digest)
        self.assertTrue(api.remote["draft"])
        self.assertFalse(any(entry[0] == "update" and entry[1].get("draft") is False for entry in api.mutations))

    def test_resuming_draft_preserves_existing_release_notes(self):
        api = FakeGitHub(self.tag, self.remote())
        self.publisher(api).publish(self.digest)
        self.assertIn("Existing release notes.", api.remote["body"])
        self.assertIn(f"{self.image}@{self.digest}", api.remote["body"])

    def test_legacy_container_section_preserves_manual_and_generated_notes(self):
        old_digest = "sha256:" + "c" * 64
        for notes in ("Manual release notes.", "## What's Changed\n\nGenerated notes."):
            with self.subTest(notes=notes):
                old_body = (f"## Container\n\n`{self.image}:{self.tag}` · `{self.image}:0.1.0`\n\n"
                            f"Digest: `{self.image}@{old_digest}`\n\n{notes}")
                updated = publish_release.release_body(old_body, self.image, self.tag, self.digest)
                self.assertIn(notes, updated)
                self.assertIn(f"{self.image}@{self.digest}", updated)
                self.assertNotIn(old_digest, updated)

    def test_managed_container_section_preserves_surrounding_notes(self):
        old_body = ("Manual introduction.\n\n<!-- carla-exporter-container -->\nOld container metadata.\n"
                    "<!-- /carla-exporter-container -->\n\n## What's Changed\n\nGenerated notes.")
        updated = publish_release.release_body(old_body, self.image, self.tag, self.digest)
        self.assertIn("Manual introduction.", updated)
        self.assertIn("## What's Changed\n\nGenerated notes.", updated)
        self.assertNotIn("Old container metadata.", updated)
        self.assertEqual(updated.count("<!-- carla-exporter-container -->"), 1)

    def test_bad_remote_metadata_is_rejected_without_mutation(self):
        cases = [
            {"tag_name": "v0.2.0"}, {"prerelease": True}, {"id": 0},
            {"draft": "true"},
        ]
        for fields in cases:
            with self.subTest(fields=fields):
                remote = self.remote()
                remote.update(fields)
                api = FakeGitHub(self.tag, remote)
                with self.assertRaises(ValueError):
                    self.publisher(api).prepare()
                self.assertEqual(api.mutations, [])

    def test_modified_local_artifact_does_not_pass_its_manifest(self):
        (self.directory / "carla-exporter-dashboard.json").write_bytes(b"modified")
        with self.assertRaises(ValueError):
            publish_release.load_artifacts(self.directory, self.tag, self.revision)

    def test_manifest_must_cover_every_expected_artifact_exactly_once(self):
        manifest = self.directory / "SHA256SUMS"
        original = manifest.read_text()
        variants = ["", original.splitlines(keepends=True)[0] + original, "".join(original.splitlines(keepends=True)[1:])]
        for text in variants:
            with self.subTest(text=text[:80]):
                manifest.write_text(text)
                with self.assertRaises(ValueError):
                    publish_release.load_artifacts(self.directory, self.tag, self.revision)

    def test_extra_and_missing_local_files_are_rejected(self):
        extra = self.directory / "extra.txt"
        extra.write_text("extra")
        self.write_manifest()
        with self.assertRaises(ValueError):
            publish_release.load_artifacts(self.directory, self.tag, self.revision)
        extra.unlink()
        (self.directory / "carla-exporter-dashboard.json").unlink()
        self.write_manifest()
        with self.assertRaises(ValueError):
            publish_release.load_artifacts(self.directory, self.tag, self.revision)

    def test_bundle_metadata_must_match_tag_revision_and_platforms(self):
        variants = [
            {"version": "0.2.0"}, {"revision": "b" * 40}, {"snapshot": True},
            {"platforms": ["linux/amd64"]},
        ]
        for fields in variants:
            with self.subTest(fields=fields):
                metadata = dict(self.metadata, **fields)
                (self.directory / "build-metadata.json").write_text(json.dumps(metadata) + "\n")
                self.write_manifest()
                with self.assertRaises(ValueError):
                    publish_release.load_artifacts(self.directory, self.tag, self.revision)


class GitHubTransportTests(unittest.TestCase):
    def client(self):
        return publish_release.GitHub("Adanomy/carla-exporter", "a" * 40)

    @staticmethod
    def response(value, status=200):
        return status, json.dumps(value).encode()

    def test_absent_release_requires_successful_listing(self):
        client = self.client()
        with patch.object(client, "request", return_value=self.response([])):
            self.assertIsNone(client.get_release("v0.1.0"))

    def test_draft_is_found_on_later_release_listing_page(self):
        client = self.client()
        first_page = [{"id": index, "tag_name": f"v0.0.{index}", "draft": False} for index in range(1, 101)]
        draft = {"id": 200, "tag_name": "v0.1.0", "draft": True}
        with patch.object(client, "request", side_effect=[self.response(first_page), self.response([draft])]) as request:
            self.assertEqual(client.get_release("v0.1.0"), draft)
        self.assertEqual(request.call_count, 2)
        self.assertIn("page=1", request.call_args_list[0].args[1])
        self.assertIn("page=2", request.call_args_list[1].args[1])

    def test_duplicate_matching_release_tags_are_rejected(self):
        client = self.client()
        releases = [
            {"id": 1, "tag_name": "v0.1.0", "draft": False},
            {"id": 2, "tag_name": "v0.1.0", "draft": True},
        ]
        with patch.object(client, "request", return_value=self.response(releases)):
            with self.assertRaises(ValueError):
                client.get_release("v0.1.0")

    def test_asset_listing_includes_later_pages(self):
        client = self.client()
        first_page = [{"id": index, "name": f"file-{index}"} for index in range(1, 101)]
        last_asset = {"id": 101, "name": "last-file"}
        with patch.object(client, "request", side_effect=[self.response(first_page), self.response([last_asset])]) as request:
            assets = client.list_assets(1)
        self.assertEqual(len(assets), 101)
        self.assertEqual(assets[-1], last_asset)
        self.assertEqual(request.call_count, 2)

    def test_authentication_not_found_and_server_errors_are_not_absence(self):
        for status in (401, 403, 404, 500, 503):
            with self.subTest(status=status):
                client = self.client()
                with patch.object(client, "request", return_value=self.response({"message": "API failed"}, status)):
                    with self.assertRaises((ValueError, RuntimeError)):
                        client.get_release("v0.1.0")

    def test_malformed_api_results_are_rejected(self):
        for body in (b"not json", b"null", b'{}', b'["unexpected"]'):
            with self.subTest(body=body):
                client = self.client()
                with patch.object(client, "request", return_value=(200, body)):
                    with self.assertRaises((ValueError, RuntimeError)):
                        client.get_release("v0.1.0")

    def test_structured_request_preserves_json_and_parses_http_headers(self):
        payload = {"body": 'Release notes with "quotes", $HOME, and\nnew lines', "draft": False}
        for newline in (b"\r\n", b"\n"):
            with self.subTest(newline=newline):
                response = newline.join([b"HTTP/2.0 200 OK", b"Content-Type: application/json", b"", b'{"id":1}'])
                completed = subprocess.CompletedProcess([], 0, stdout=response, stderr=b"")
                with patch.object(publish_release.subprocess, "run", return_value=completed) as run:
                    status, body = self.client().request("PATCH", "repos/Adanomy/carla-exporter/releases/1", payload)
                self.assertEqual(status, 200)
                self.assertEqual(json.loads(body), {"id": 1})
                self.assertEqual(json.loads(run.call_args.kwargs["input"]), payload)
                self.assertIn("--input", run.call_args.args[0])
                self.assertFalse(run.call_args.kwargs.get("shell", False))

    def test_transport_failure_is_not_a_successful_response(self):
        responses = [
            subprocess.CompletedProcess([], 1, stdout=b"", stderr=b"Connection failed"),
            subprocess.CompletedProcess([], 1, stdout=b"HTTP/2.0 200 OK\n\n{}", stderr=b"Incomplete body"),
        ]
        for completed in responses:
            with self.subTest(stdout=completed.stdout):
                with patch.object(publish_release.subprocess, "run", return_value=completed):
                    with self.assertRaises(ValueError):
                        self.client().request("GET", "repos/Adanomy/carla-exporter/releases")

    def test_create_release_is_explicitly_a_draft_with_generated_notes(self):
        client = self.client()
        ref = {"object": {"type": "commit", "sha": "a" * 40}}
        with patch.object(client, "json", side_effect=[ref, {"body": "Generated notes."}, {"id": 1}]) as request:
            client.create_release("v0.1.0", "Container metadata.")
        self.assertEqual(request.call_count, 3)
        creation = request.call_args_list[-1].args[2]
        self.assertIs(creation["draft"], True)
        self.assertIs(creation["prerelease"], False)
        self.assertEqual(creation["tag_name"], "v0.1.0")
        self.assertEqual(creation["target_commitish"], "a" * 40)
        self.assertIn("Container metadata.", creation["body"])
        self.assertIn("Generated notes.", creation["body"])

    def test_remote_tag_must_exist_at_the_build_revision_before_creation(self):
        client = self.client()
        for ref in ({}, {"object": {"type": "commit", "sha": "b" * 40}}):
            with self.subTest(ref=ref):
                with patch.object(client, "json", return_value=ref) as request:
                    with self.assertRaises(ValueError):
                        client.create_release("v0.1.0", "Container metadata.")
                self.assertTrue(all(call.args[0] == "GET" for call in request.call_args_list))

    def test_missing_remote_tag_fails_before_creating_a_release(self):
        client = self.client()
        with patch.object(client, "request", return_value=self.response({"message": "Not Found"}, 404)) as request:
            with self.assertRaises(ValueError):
                client.create_release("v0.1.0", "Container metadata.")
        self.assertEqual(request.call_count, 1)
        self.assertEqual(request.call_args.args[0], "GET")
        self.assertIn("/git/ref/tags/v0.1.0", request.call_args.args[1])

    def test_annotated_release_tag_is_resolved_to_its_commit(self):
        client = self.client()
        responses = [
            {"object": {"type": "tag", "sha": "b" * 40}},
            {"object": {"type": "commit", "sha": "a" * 40}},
            {"body": "Generated notes."}, {"id": 1},
        ]
        with patch.object(client, "json", side_effect=responses) as request:
            client.create_release("v0.1.0", "Container metadata.")
        self.assertIn("/git/tags/" + "b" * 40, request.call_args_list[1].args[1])

    def test_binary_download_requests_the_actual_asset_bytes(self):
        client = self.client()
        content = b"\x00\xffarchive\n"
        with patch.object(publish_release.subprocess, "check_output", return_value=content) as download:
            self.assertEqual(client.download_asset(42), content)
        self.assertIn("repos/Adanomy/carla-exporter/releases/assets/42", download.call_args.args[0])
        self.assertIn("Accept: application/octet-stream", download.call_args.args[0])

if __name__ == "__main__":
    unittest.main()
