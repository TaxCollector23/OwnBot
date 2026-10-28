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
    return f"ownbot-{version}-{goos}-{goarch}.tar.gz"


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


def _build_binary(goos: str, goarch: str, command: str, version: str, destination: Path) -> None:
    env = os.environ.copy()
    env.update({"CGO_ENABLED": "0", "GOOS": goos, "GOARCH": goarch})
    subprocess.run(
        ["go", "build", "-trimpath", f"-ldflags=-s -w -X ownbot/internal/buildinfo.Version={version}", "-o", str(destination), f"./cmd/{command}"],
        cwd=ROOT,
        env=env,
        check=True,
    )


def _tar_info(path: Path, name: str) -> tarfile.TarInfo:
    info = tarfile.TarInfo(name)
    info.size = path.stat().st_size
    info.mode = 0o755 if name in ("ownbot", "ownbotd", "ownbot.exe", "ownbotd.exe") else 0o644
    info.uid = info.gid = 0
    info.uname = info.gname = ""
    info.mtime = 0
    return info


def _write_archive(stage: Path, destination: Path, goos: str) -> None:
    suffix = ".exe" if goos == "windows" else ""
    files = [stage / f"ownbot{suffix}", stage / f"ownbotd{suffix}", ROOT / "README.md"]
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
            _build_binary(goos, goarch, command, version, stage / f"{command}{suffix}")
        name = archive_name(version, goos, goarch)
        destination = output / name
        binary_digests = {path.name: sha256(path) for path in stage.iterdir()}
        _write_archive(stage, destination, goos)
        shutil.rmtree(stage)
        assets.append({"os": goos, "arch": goarch, "file": name, "sha256": sha256(destination), "binaries": binary_digests})

    manifest = {"name": "ownbot", "version": version, "assets": assets}
    manifest_path = output / "manifest.json"
    manifest_path.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    write_checksums(output)


def write_checksums(output: Path) -> None:
    checksum_paths = sorted(path for path in output.iterdir() if path.is_file() and path.name != "SHA256SUMS")
    (output / "SHA256SUMS").write_text(
        "".join(f"{sha256(path)}  {path.name}\n" for path in checksum_paths), encoding="utf-8"
    )


def sign(output: Path, key_id: str, private_key_env: str) -> None:
    if not key_id or not private_key_env or not os.environ.get(private_key_env):
        raise ValueError(f"release signing requires a non-empty {private_key_env} secret and key id")
    subprocess.run(
        [
            "go",
            "run",
            "scripts/release/sign.go",
            "sign",
            "--key-id",
            key_id,
            "--private-key-env",
            private_key_env,
            "--manifest",
            str(output / "manifest.json"),
            "--signature",
            str(output / "manifest.sig"),
        ],
        cwd=ROOT,
        check=True,
    )
    write_checksums(output)


def verify(output: Path, require_signature: bool = False) -> None:
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
        binary_names = {"ownbot.exe", "ownbotd.exe"} if target[0] == "windows" else {"ownbot", "ownbotd"}
        binary_digests = asset.get("binaries")
        if not isinstance(binary_digests, dict) or set(binary_digests) != binary_names or any(
            not isinstance(digest, str) or len(digest) != 64 or any(char not in "0123456789abcdef" for char in digest)
            for digest in binary_digests.values()
        ):
            raise ValueError(f"manifest binary digests are malformed: {filename}")
        seen_files.add(filename)
        path = output / filename
        if not path.is_file() or sha256(path) != asset.get("sha256"):
            raise ValueError(f"missing or modified release archive: {filename}")
    if actual_targets != set(TARGETS):
        raise ValueError("manifest is missing one or more supported release targets")

    signature_path = output / "manifest.sig"
    if require_signature and not signature_path.is_file():
        raise ValueError("release signature is required")
    checksums = output / "SHA256SUMS"
    expected_paths = [manifest_path] + [output / name for name in sorted(seen_files)]
    if signature_path.is_file():
        expected_paths.append(signature_path)
    expected = {path.name: sha256(path) for path in expected_paths}
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
    sign_parser = subparsers.add_parser("sign")
    sign_parser.add_argument("--output", type=Path, required=True)
    sign_parser.add_argument("--key-id", required=True)
    sign_parser.add_argument("--private-key-env", default="OWNBOT_RELEASE_PRIVATE_KEY")
    verify_parser = subparsers.add_parser("verify")
    verify_parser.add_argument("--output", type=Path, required=True)
    verify_parser.add_argument("--require-signature", action="store_true")
    args = parser.parse_args()
    try:
        if args.command == "build":
            build(args.version, args.output)
            verify(args.output)
        elif args.command == "sign":
            sign(args.output, args.key_id, args.private_key_env)
            verify(args.output, require_signature=True)
        else:
            verify(args.output, require_signature=args.require_signature)
    except (OSError, ValueError, subprocess.CalledProcessError, json.JSONDecodeError) as error:
        print(f"release pipeline: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
