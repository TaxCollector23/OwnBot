import json
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
            "ownbot-0.1.0-windows-amd64.zip",
        )
        with self.assertRaises(ValueError):
            pipeline.archive_name("0.1.0", "windows", "arm64")

    def _fixture(self, output: Path) -> None:
        assets = []
        for goos, goarch in pipeline.TARGETS:
            filename = pipeline.archive_name("0.1.0", goos, goarch)
            artifact = output / filename
            artifact.write_bytes(f"fixture:{goos}/{goarch}".encode())
            assets.append(
                {"os": goos, "arch": goarch, "file": filename, "sha256": pipeline.sha256(artifact)}
            )
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


if __name__ == "__main__":
    unittest.main()
