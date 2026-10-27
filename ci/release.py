#!/usr/bin/env python3
"""Build deterministic release archives using Go and the Python standard library."""

import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import time
import zipfile


ROOT = Path(__file__).resolve().parent.parent
PLATFORMS = (
    ("linux", "amd64"),
    ("linux", "arm64"),
    ("darwin", "amd64"),
    ("darwin", "arm64"),
    ("windows", "amd64"),
)
RELEASE_TAG = re.compile(r"v(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)", re.ASCII)


def command(*args, env=None):
    return subprocess.check_output(args, cwd=ROOT, env=env, text=True).strip()


def release_version(revision):
    ref_type = os.environ.get("GITHUB_REF_TYPE")
    if ref_type:
        if ref_type != "tag":
            raise ValueError("release builds require a Git tag, not a branch")
        tag = os.environ.get("GITHUB_REF_NAME", "")
    else:
        tags = [
            tag for tag in command("git", "tag", "--points-at", "HEAD").splitlines()
            if RELEASE_TAG.fullmatch(tag)
        ]
        if len(tags) != 1:
            raise ValueError("release builds require exactly one vMAJOR.MINOR.PATCH tag at HEAD")
        tag = tags[0]
    if not RELEASE_TAG.fullmatch(tag):
        raise ValueError(f"invalid release tag {tag!r}; expected vMAJOR.MINOR.PATCH")
    if command("git", "rev-parse", f"refs/tags/{tag}^{{commit}}") != revision:
        raise ValueError(f"release tag {tag!r} must point to HEAD")
    return tag.removeprefix("v")


def release_metadata(snapshot):
    match = re.search(r"^go (\S+)$", (ROOT / "go.mod").read_text(), re.MULTILINE)
    if not match:
        raise ValueError("go.mod has no Go version")
    toolchain = "go" + match[1]
    env = dict(os.environ, GOTOOLCHAIN=toolchain, GOWORK="off", GOENV="off", GOFLAGS="")
    if command("go", "env", "GOVERSION", env=env) != toolchain:
        raise ValueError(f"release builds require exactly {toolchain}")

    revision = "uncommitted"
    epoch = 0
    head = subprocess.run(
        ["git", "rev-parse", "--verify", "HEAD"], cwd=ROOT,
        stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True,
    )
    if head.returncode == 0:
        revision = head.stdout.strip()
        epoch = int(command("git", "show", "-s", "--format=%ct", "HEAD"))
        dirty = bool(command("git", "status", "--porcelain", "--untracked-files=normal"))
        if dirty:
            if not snapshot:
                raise ValueError("release builds require a clean working tree; use --snapshot for a preview")
            revision += "-dirty"
    elif not snapshot:
        raise ValueError("release builds require a committed, tagged source; use --snapshot for a preview")

    return {
        "version": "dev" if snapshot else release_version(revision),
        "revision": revision,
        "go_version": toolchain,
        "source_date_epoch": epoch,
        "snapshot": snapshot,
        "platforms": [f"{goos}/{goarch}" for goos, goarch in PLATFORMS],
    }


def archive(path, members, epoch):
    if path.suffix == ".zip":
        # ZIP dates begin in 1980; use UTC rather than the builder's timezone.
        date = time.gmtime(max(epoch, 315532800))[:6]
        with zipfile.ZipFile(path, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=9) as output:
            for name, content, mode in sorted(members):
                info = zipfile.ZipInfo(name, date_time=date)
                info.create_system = 3
                info.external_attr = (0o100000 | mode) << 16
                info.compress_type = zipfile.ZIP_DEFLATED
                output.writestr(info, content, compresslevel=9)
    else:
        with path.open("wb") as raw:
            with gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=epoch, compresslevel=9) as compressed:
                with tarfile.open(fileobj=compressed, mode="w", format=tarfile.USTAR_FORMAT) as output:
                    for name, content, mode in sorted(members):
                        info = tarfile.TarInfo(name)
                        info.size, info.mode, info.mtime = len(content), mode, epoch
                        info.uid = info.gid = 0
                        info.uname = info.gname = ""
                        output.addfile(info, io.BytesIO(content))


def checksums(directory):
    return {
        path.name: hashlib.sha256(path.read_bytes()).hexdigest()
        for path in sorted(directory.iterdir()) if path.name != "SHA256SUMS"
    }


def build(directory, metadata, shared, fresh_cache=False):
    directory.mkdir()
    env = dict(
        os.environ, CGO_ENABLED="0", GOTOOLCHAIN=metadata["go_version"], GOWORK="off",
        GOENV="off", GOFLAGS="", GOEXPERIMENT="", GOAMD64="v1", GOARM64="v8.0",
        SOURCE_DATE_EPOCH=str(metadata["source_date_epoch"]),
    )
    flags = (
        "-s -w -buildid= "
        f"-X main.version={metadata['version']} -X main.revision={metadata['revision']}"
    )
    with tempfile.TemporaryDirectory(prefix="carla-exporter-binaries-") as temporary:
        if fresh_cache:
            env["GOCACHE"] = str(Path(temporary) / "cache")
        for goos, goarch in PLATFORMS:
            print(f"Building {goos}/{goarch}", flush=True)
            binary_name = "carla-exporter" + (".exe" if goos == "windows" else "")
            binary = Path(temporary) / binary_name
            subprocess.run(
                ["go", "build", "-mod=readonly", "-trimpath", "-buildvcs=false", "-ldflags=" + flags,
                 "-o", str(binary), "./cmd/carla-exporter"],
                cwd=ROOT, env=dict(env, GOOS=goos, GOARCH=goarch), check=True,
            )
            suffix = ".zip" if goos == "windows" else ".tar.gz"
            name = f"carla-exporter_{metadata['version']}_{goos}_{goarch}{suffix}"
            archive(directory / name, shared + [(binary_name, binary.read_bytes(), 0o755)], metadata["source_date_epoch"])

    (directory / "carla-exporter-dashboard.json").write_bytes((ROOT / "grafana/carla-exporter.json").read_bytes())
    (directory / "build-metadata.json").write_text(json.dumps(metadata, indent=2) + "\n")
    manifest = checksums(directory)
    (directory / "SHA256SUMS").write_text("".join(f"{digest}  {name}\n" for name, digest in manifest.items()))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--snapshot", action="store_true", help="build an unpublished dev preview without requiring a tag")
    parser.add_argument("--verify-reproducible", action="store_true", help="build every artifact twice and compare SHA-256 digests")
    parser.add_argument("--output", type=Path, help="empty output directory (default: dist/preview or dist/release)")
    args = parser.parse_args()
    destination = (args.output or ROOT / "dist" / ("preview" if args.snapshot else "release")).resolve()
    if destination.exists() and (not destination.is_dir() or any(destination.iterdir())):
        raise ValueError(f"output must be an empty directory: {destination}")
    metadata = release_metadata(args.snapshot)
    shared = [(name, (ROOT / name).read_bytes(), 0o644) for name in ("LICENSE", "README.md")]
    with tempfile.TemporaryDirectory(prefix="carla-exporter-release-") as temporary:
        first = Path(temporary) / "first"
        build(first, metadata, shared)
        if args.verify_reproducible:
            second = Path(temporary) / "second"
            build(second, metadata, shared, fresh_cache=True)
            if checksums(first) != checksums(second):
                raise ValueError("reproducibility check failed: artifact digests differ")
            print("Reproducibility verified for all artifacts", flush=True)
        destination.mkdir(parents=True, exist_ok=True)
        for artifact in first.iterdir():
            shutil.copyfile(artifact, destination / artifact.name)
    print(f"{'Unpublished preview' if args.snapshot else 'Release'} artifacts: {destination}")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        print(f"release: {error}", file=sys.stderr)
        raise SystemExit(1)
