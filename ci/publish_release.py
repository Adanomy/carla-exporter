#!/usr/bin/env python3
"""Resume a draft GitHub Release and publish only verified, complete artifacts."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys

from release import PLATFORMS, RELEASE_TAG


def sha256(content):
    return hashlib.sha256(content).hexdigest()


def load_artifacts(directory, tag, revision):
    if not RELEASE_TAG.fullmatch(tag):
        raise ValueError("expected a vMAJOR.MINOR.PATCH release tag")
    if not re.fullmatch(r"[0-9a-f]{40}(?:[0-9a-f]{24})?", revision):
        raise ValueError("expected a full Git commit revision")
    version = tag[1:]
    expected = {"SHA256SUMS", "build-metadata.json", "carla-exporter-dashboard.json"}
    for goos, goarch in PLATFORMS:
        suffix = "zip" if goos == "windows" else "tar.gz"
        expected.add(f"carla-exporter_{version}_{goos}_{goarch}.{suffix}")
    artifacts = {path.name: path for path in Path(directory).iterdir()}
    if artifacts.keys() != expected:
        raise ValueError("release artifact directory must contain exactly the complete release file set")
    if any(path.is_symlink() or not path.is_file() for path in artifacts.values()):
        raise ValueError("release artifacts must be regular files")
    checksums = {}
    for line in artifacts["SHA256SUMS"].read_text().splitlines():
        match = re.fullmatch(r"([0-9a-f]{64})  ([^/\\\r\n]+)", line)
        if not match or match[2] in checksums:
            raise ValueError("invalid or duplicate SHA256SUMS entry")
        checksums[match[2]] = match[1]
    if checksums.keys() != expected - {"SHA256SUMS"}:
        raise ValueError("SHA256SUMS does not cover the complete release file set")
    for name, digest in checksums.items():
        if sha256(artifacts[name].read_bytes()) != digest:
            raise ValueError(f"local artifact checksum mismatch: {name}")
    metadata = json.loads(artifacts["build-metadata.json"].read_text())
    if not isinstance(metadata, dict) or any((
        metadata.get("version") != version,
        metadata.get("revision") != revision,
        metadata.get("snapshot") is not False,
        metadata.get("platforms") != [f"{goos}/{goarch}" for goos, goarch in PLATFORMS],
    )):
        raise ValueError("build metadata does not match the release tag, revision, and platforms")
    return artifacts


class GitHub:
    def __init__(self, repository, revision):
        if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository):
            raise ValueError("invalid GITHUB_REPOSITORY")
        self.repository, self.revision = repository, revision
        self.base = f"repos/{repository}/releases"

    def request(self, method, endpoint, data=None):
        args = ["gh", "api", "--include", "--method", method, endpoint,
                "--header", "Accept: application/vnd.github+json",
                "--header", "X-GitHub-Api-Version: 2022-11-28"]
        payload = None
        if data is not None:
            args += ["--input", "-"]
            payload = json.dumps(data).encode()
        result = subprocess.run(args, input=payload, capture_output=True, check=False)
        headers, separator, body = result.stdout.partition(b"\r\n\r\n")
        if not separator:
            headers, separator, body = result.stdout.partition(b"\n\n")
        status = re.match(rb"HTTP/\S+ (\d{3})(?:\s|$)", headers)
        if not separator or not status:
            raise ValueError(f"GitHub API did not return an HTTP response: {method} {endpoint}")
        code = int(status[1])
        if result.returncode and 200 <= code < 300:
            raise ValueError(f"GitHub API request did not complete: {method} {endpoint}")
        return code, body

    def json(self, method, endpoint, data=None):
        status, body = self.request(method, endpoint, data)
        if not 200 <= status < 300:
            raise ValueError(f"GitHub API request failed (HTTP {status}): {method} {endpoint}")
        return json.loads(body) if body else None

    def pages(self, endpoint):
        for page in range(1, 101):
            items = self.json("GET", f"{endpoint}?per_page=100&page={page}")
            if (not isinstance(items, list) or len(items) > 100
                    or any(not isinstance(item, dict) for item in items)):
                raise ValueError(f"invalid GitHub API list response: {endpoint}")
            yield from items
            if len(items) < 100:
                return
        raise ValueError(f"GitHub API pagination limit exceeded: {endpoint}")

    def get_release(self, tag):
        # The release-by-tag endpoint may omit drafts, so enumerate authenticated releases.
        releases = list(self.pages(self.base))
        if any(not isinstance(item.get("tag_name"), str) for item in releases):
            raise ValueError("invalid GitHub release tag in list response")
        matches = [item for item in releases if item["tag_name"] == tag]
        if len(matches) > 1:
            raise ValueError(f"multiple GitHub Releases use tag {tag}")
        return matches[0] if matches else None

    def list_assets(self, release_id):
        return list(self.pages(f"{self.base}/{release_id}/assets"))

    def download_asset(self, asset_id):
        return subprocess.check_output([
            "gh", "api", f"{self.base}/assets/{asset_id}",
            "--header", "Accept: application/octet-stream",
        ])

    def verify_tag(self, tag):
        ref = self.json("GET", f"repos/{self.repository}/git/ref/tags/{tag}")
        obj = ref.get("object") if isinstance(ref, dict) else None
        for _ in range(10):
            if not isinstance(obj, dict) or not isinstance(obj.get("sha"), str):
                raise ValueError("invalid GitHub release tag reference")
            if obj.get("type") == "commit":
                if obj["sha"] != self.revision:
                    raise ValueError("remote release tag does not match the build revision")
                break
            if obj.get("type") != "tag" or not re.fullmatch(r"[0-9a-f]{40}(?:[0-9a-f]{24})?", obj["sha"]):
                raise ValueError("invalid annotated GitHub release tag")
            annotated = self.json("GET", f"repos/{self.repository}/git/tags/{obj['sha']}")
            obj = annotated.get("object") if isinstance(annotated, dict) else None
        else:
            raise ValueError("GitHub release tag nesting limit exceeded")

    def create_release(self, tag, body):
        self.verify_tag(tag)
        notes = self.json("POST", f"{self.base}/generate-notes", {
            "tag_name": tag, "target_commitish": self.revision,
        })
        if not isinstance(notes, dict) or not isinstance(notes.get("body"), str):
            raise ValueError("GitHub generated invalid release notes")
        return self.json("POST", self.base, {
            "tag_name": tag, "target_commitish": self.revision, "name": tag,
            "draft": True, "prerelease": False, "body": body + "\n\n" + notes["body"],
        })

    def delete_asset(self, asset_id):
        self.json("DELETE", f"{self.base}/assets/{asset_id}")

    def upload_asset(self, tag, path):
        subprocess.run([
            "gh", "release", "upload", tag, str(path.resolve()), "--repo", self.repository,
        ], check=True)

    def update_release(self, release_id, **fields):
        return self.json("PATCH", f"{self.base}/{release_id}", fields)


def release_body(body, image, tag, digest):
    start, end = "<!-- carla-exporter-container -->", "<!-- /carla-exporter-container -->"
    if start in body and end in body:
        before, rest = body.split(start, 1)
        _, after = rest.split(end, 1)
        body = before + after
    else:
        # Replace only the exact prefix emitted by the previous workflow.
        legacy = (r"\A## Container\n\n" + re.escape(f"`{image}:{tag}` · `{image}:{tag[1:]}`")
                  + r"\n\nDigest: `" + re.escape(image) + r"@sha256:[0-9a-f]{64}`\n?")
        body = re.sub(legacy, "", body, count=1)
    container = (f"{start}\n## Container\n\n`{image}:{tag}` · `{image}:{tag[1:]}`\n\n"
                 f"Digest: `{image}@{digest}`\n{end}")
    return container + ("\n\n" + body.strip() if body.strip() else "")


class Publisher:
    def __init__(self, api, artifacts, tag, image, revision):
        self.api, self.artifacts, self.tag = api, artifacts, tag
        self.image, self.revision = image, revision
        if not re.fullmatch(r"ghcr\.io/[a-z0-9_.-]+/[a-z0-9_.-]+", image):
            raise ValueError("invalid release container image")
        self.hashes = {name: sha256(path.read_bytes()) for name, path in artifacts.items()}

    def inspect(self, release, complete=False):
        if not isinstance(release, dict) or any((
            type(release.get("id")) is not int or release["id"] <= 0,
            release.get("tag_name") != self.tag,
            type(release.get("draft")) is not bool,
            release.get("prerelease") is not False,
            release.get("body") is not None and not isinstance(release["body"], str),
        )):
            raise ValueError("GitHub Release does not match the stable release tag")
        assets, incomplete = {}, []
        for asset in self.api.list_assets(release["id"]):
            if not isinstance(asset, dict):
                raise ValueError("invalid GitHub release asset")
            name, asset_id = asset.get("name"), asset.get("id")
            if not isinstance(name, str) or name not in self.artifacts or name in assets:
                raise ValueError(f"unexpected or duplicate release asset: {name}")
            if type(asset_id) is not int or asset_id <= 0:
                raise ValueError(f"invalid release asset ID: {name}")
            if type(asset.get("size")) is not int or asset["size"] < 0:
                raise ValueError(f"invalid release asset size: {name}")
            assets[name] = asset
            if asset.get("state") == "starter" and release["draft"] and not complete:
                if asset["size"] != 0:
                    raise ValueError(f"unfinished release asset is not empty: {name}")
                incomplete.append(asset)
                continue
            if asset.get("state") != "uploaded":
                raise ValueError(f"release asset upload is incomplete: {name}")
            content = self.api.download_asset(asset_id)
            if asset.get("size") != len(content) or sha256(content) != self.hashes[name]:
                raise ValueError(f"remote artifact checksum mismatch: {name}")
        if (complete or not release["draft"]) and assets.keys() != self.artifacts.keys():
            raise ValueError("GitHub Release does not contain the complete artifact set")
        return assets, incomplete

    def prepare(self):
        self.api.verify_tag(self.tag)
        release = self.api.get_release(self.tag)
        if release is None:
            return False
        self.inspect(release)
        return not release["draft"]

    def publish(self, digest):
        if not re.fullmatch(r"sha256:[0-9a-f]{64}", digest):
            raise ValueError("invalid container digest")
        self.api.verify_tag(self.tag)
        release = self.api.get_release(self.tag)
        if release is not None:
            assets, incomplete = self.inspect(release)
            if not release["draft"]:
                print(f"Release {self.tag} is already published and verified")
                return
        else:
            release = self.api.create_release(self.tag, release_body("", self.image, self.tag, digest))
            assets, incomplete = self.inspect(release)
        for asset in incomplete:
            self.api.delete_asset(asset["id"])
            del assets[asset["name"]]
        for name, path in sorted(self.artifacts.items()):
            if name not in assets:
                print(f"Uploading missing release artifact: {name}", flush=True)
                self.api.upload_asset(self.tag, path)
        release = self.api.get_release(self.tag)
        self.inspect(release, complete=True)
        if not release["draft"]:
            return
        body = release_body(release.get("body") or "", self.image, self.tag, digest)
        self.api.update_release(release["id"], body=body, draft=False)
        published = self.api.get_release(self.tag)
        self.inspect(published, complete=True)
        if published["draft"]:
            raise ValueError("GitHub Release remains a draft after publication")
        print(f"Published verified release {self.tag}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("prepare", "publish"))
    parser.add_argument("--tag", required=True)
    parser.add_argument("--artifacts", type=Path, required=True)
    parser.add_argument("--image", required=True)
    parser.add_argument("--revision", required=True)
    parser.add_argument("--digest")
    args = parser.parse_args()
    artifacts = load_artifacts(args.artifacts, args.tag, args.revision)
    api = GitHub(os.environ.get("GITHUB_REPOSITORY", ""), args.revision)
    publisher = Publisher(api, artifacts, args.tag, args.image, args.revision)
    if args.action == "prepare":
        published = publisher.prepare()
        if output := os.environ.get("GITHUB_OUTPUT"):
            with Path(output).open("a") as stream:
                stream.write(f"published={str(published).lower()}\n")
        print(f"Release {args.tag}: {'published and verified' if published else 'ready to resume'}")
    else:
        publisher.publish(args.digest or "")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        print(f"publish-release: {error}", file=sys.stderr)
        raise SystemExit(1)
