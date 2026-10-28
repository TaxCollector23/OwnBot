#!/usr/bin/env python3
"""Build and verify the public OwnBot CLI/daemon release archives."""

from __future__ import annotations

import argparse
import gzip
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import tarfile
import zipfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
TARGETS = (
    ("darwin", "arm64"),
    ("darwin", "amd64"),
    ("linux", "arm64"),
    ("linux", "amd64"),
    ("windows", "amd64"),
    ("freebsd", "amd64"),
)


def archive_name(version: str, goos: str, goarch: str) -> str:
    if (goos, goarch) not in TARGETS:
        raise ValueError(f"unsupported release target: {goos}/{goarch}")
    extension = "zip" if goos == "windows" else "tar.gz"
    return f"ownbot-{version}-{goos}-{goarch}.{extension}"


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as file:
        for chunk in iter(lambda: file.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _version_from_tag(value: str) -> str:
    version = value.removeprefix("v")
    if not re.fullmatch(r"\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?", version):
        raise ValueError("release version must be a semantic version, optionally prefixed with v")
    return version


def _check_package_version(version: str) -> None:
    package = json.loads((ROOT / "package.json").read_text(encoding="utf-8"))
    if package.get("version") != version:
        raise ValueError(f"package.json is {package.get('version')!r}; release tag is {version!r}")


def _build_binary(goos: str, goarch: str, command: str, destination: Path) -> None:
    env = os.environ.copy()
    env.update({"CGO_ENABLED": "0", "GOOS": goos, "GOARCH": goarch})
    subprocess.run(
        ["go", "build", "-trimpath", "-ldflags=-s -w", "-o", str(destination), f"./cmd/{command}"],
        cwd=ROOT,
        env=env,
        check=True,
    )


def _tar_info(path: Path, name: str) -> tarfile.TarInfo:
    info = tarfile.TarInfo(name)
    info.size = path.stat().st_size
    info.mode = 0o755 if name in ("ownbot", "ownbotd") else 0o644
    info.uid = info.gid = 0
    info.uname = info.gname = ""
    info.mtime = 0
    return info


def _write_archive(stage: Path, destination: Path, goos: str) -> None:
    suffix = ".exe" if goos == "windows" else ""
    files = [stage / f"ownbot{suffix}", stage / f"ownbotd{suffix}", ROOT / "README.md"]
    if goos == "windows":
        with zipfile.ZipFile(destination, "w", compression=zipfile.ZIP_DEFLATED) as archive:
            for source in files:
                name = source.name
                info = zipfile.ZipInfo(name, date_time=(1980, 1, 1, 0, 0, 0))
                info.external_attr = (0o100755 if name.endswith(".exe") else 0o100644) << 16
                archive.writestr(info, source.read_bytes())
        return

    with destination.open("wb") as raw:
        with gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=0) as compressed:
            with tarfile.open(fileobj=compressed, mode="w") as archive:
                for source in files:
                    name = source.name
                    with source.open("rb") as file:
                        archive.addfile(_tar_info(source, name), file)


def build(version_arg: str, output: Path) -> None:
    version = _version_from_tag(version_arg)
    _check_package_version(version)
    if output.exists() and any(output.iterdir()):
        raise ValueError(f"output directory must be empty: {output}")
    output.mkdir(parents=True, exist_ok=True)
    assets = []
    for goos, goarch in TARGETS:
        stage = output / f".stage-{goos}-{goarch}"
        stage.mkdir()
        suffix = ".exe" if goos == "windows" else ""
        for command in ("ownbot", "ownbotd"):
            _build_binary(goos, goarch, command, stage / f"{command}{suffix}")
        name = archive_name(version, goos, goarch)
        destination = output / name
        _write_archive(stage, destination, goos)
        shutil.rmtree(stage)
        assets.append({"os": goos, "arch": goarch, "file": name, "sha256": sha256(destination)})

    manifest = {"name": "ownbot", "version": version, "assets": assets}
    manifest_path = output / "manifest.json"
    manifest_path.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    checksum_paths = sorted([output / asset["file"] for asset in assets] + [manifest_path])
    (output / "SHA256SUMS").write_text(
        "".join(f"{sha256(path)}  {path.name}\n" for path in checksum_paths), encoding="utf-8"
    )


def verify(output: Path) -> None:
    manifest_path = output / "manifest.json"
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    if manifest.get("name") != "ownbot":
        raise ValueError("manifest identity or version is invalid")
    version = _version_from_tag(manifest.get("version", ""))
    assets = manifest.get("assets")
    if not isinstance(assets, list):
        raise ValueError("manifest assets must be a list")
    actual_targets = set()
    seen_files = set()
    for asset in assets:
        if not isinstance(asset, dict):
            raise ValueError("manifest asset entry must be an object")
        target = (asset.get("os"), asset.get("arch"))
        if target not in TARGETS or target in actual_targets:
            raise ValueError(f"manifest target is unsupported or duplicated: {target}")
        actual_targets.add(target)
        filename = archive_name(version, *target)
        if asset.get("file") != filename or filename in seen_files:
            raise ValueError(f"manifest filename does not match target: {asset.get('file')}")
        seen_files.add(filename)
        path = output / filename
        if not path.is_file() or sha256(path) != asset.get("sha256"):
            raise ValueError(f"missing or modified release archive: {filename}")
    if actual_targets != set(TARGETS):
        raise ValueError("manifest is missing one or more supported release targets")

    checksums = output / "SHA256SUMS"
    expected = {path.name: sha256(path) for path in [manifest_path] + [output / name for name in sorted(seen_files)]}
    entries = {}
    for line in checksums.read_text(encoding="utf-8").splitlines():
        digest, separator, filename = line.partition("  ")
        if not separator or filename in entries:
            raise ValueError("SHA256SUMS contains a malformed or duplicate entry")
        entries[filename] = digest
    if entries != expected:
        raise ValueError("SHA256SUMS is incomplete or does not match release assets")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)
    build_parser = subparsers.add_parser("build")
    build_parser.add_argument("--version", required=True, help="tag or semantic version")
    build_parser.add_argument("--output", type=Path, required=True)
    verify_parser = subparsers.add_parser("verify")
    verify_parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        if args.command == "build":
            build(args.version, args.output)
            verify(args.output)
        else:
            verify(args.output)
    except (OSError, ValueError, subprocess.CalledProcessError, json.JSONDecodeError) as error:
        print(f"release pipeline: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
