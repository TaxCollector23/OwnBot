import json
import tarfile
import tempfile
import unittest
from pathlib import Path

from scripts.release import pipeline


class ReleasePipelineTests(unittest.TestCase):
    def test_archive_name_supports_release_targets_and_platform_extensions(self):
        self.assertEqual(
            pipeline.archive_name("0.1.0", "darwin", "arm64"),
            "ownbot-0.1.0-darwin-arm64.tar.gz",
        )
        self.assertEqual(
            pipeline.archive_name("0.1.0", "windows", "amd64"),
            "ownbot-0.1.0-windows-amd64.tar.gz",
        )
        with self.assertRaises(ValueError):
            pipeline.archive_name("0.1.0", "windows", "arm64")

    def _fixture(self, output: Path) -> None:
        assets = []
        for goos, goarch in pipeline.TARGETS:
            filename = pipeline.archive_name("0.1.0", goos, goarch)
            artifact = output / filename
            artifact.write_bytes(f"fixture:{goos}/{goarch}".encode())
            binary_names = {"ownbot.exe", "ownbotd.exe"} if goos == "windows" else {"ownbot", "ownbotd"}
            assets.append({
                "os": goos,
                "arch": goarch,
                "file": filename,
                "sha256": pipeline.sha256(artifact),
                "binaries": {name: "a" * 64 for name in binary_names},
            })
        manifest = output / "manifest.json"
        manifest.write_text(json.dumps({"name": "ownbot", "version": "0.1.0", "assets": assets}))
        checksum_paths = sorted([output / asset["file"] for asset in assets] + [manifest])
        (output / "SHA256SUMS").write_text(
            "".join(f"{pipeline.sha256(path)}  {path.name}\n" for path in checksum_paths)
        )

    def test_verify_accepts_manifest_and_rejects_tampering(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            output = Path(temp_dir)
            self._fixture(output)
            pipeline.verify(output)

            (output / "ownbot-0.1.0-linux-amd64.tar.gz").write_bytes(b"tampered")
            with self.assertRaisesRegex(ValueError, "modified release archive"):
                pipeline.verify(output)

    def test_signed_release_requires_and_checks_manifest_signature_asset(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            output = Path(temp_dir)
            self._fixture(output)
            with self.assertRaisesRegex(ValueError, "signature is required"):
                pipeline.verify(output, require_signature=True)
            (output / "manifest.sig").write_text('{"key_id":"fixture-test-key","signature":"fixture"}')
            pipeline.write_checksums(output)
            pipeline.verify(output, require_signature=True)
            (output / "manifest.sig").write_text("changed")
            with self.assertRaisesRegex(ValueError, "SHA256SUMS"):
                pipeline.verify(output, require_signature=True)

    def test_windows_release_archive_uses_tar_gzip_for_builtin_installers(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            stage = root / "stage"
            stage.mkdir()
            for name in ("ownbot.exe", "ownbotd.exe"):
                (stage / name).write_bytes(name.encode())
            (root / "README.md").write_text("fixture")
            archive = root / pipeline.archive_name("1.2.3", "windows", "amd64")
            pipeline._write_archive(stage, archive, "windows")
            with tarfile.open(archive, "r:gz") as tar:
                self.assertEqual(set(tar.getnames()), {"ownbot.exe", "ownbotd.exe", "README.md"})
                self.assertEqual(tar.getmember("ownbot.exe").mode, 0o755)


if __name__ == "__main__":
    unittest.main()
