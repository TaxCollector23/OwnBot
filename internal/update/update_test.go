package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func signedFixture(t *testing.T) ([]byte, []byte, map[string]ed25519.PublicKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Name: "ownbot", Version: "1.2.3", Assets: []Asset{{OS: "linux", Arch: "amd64", File: archiveName("1.2.3", "linux", "amd64"), SHA256: string(bytes.Repeat([]byte("a"), 64)), Binaries: map[string]string{"ownbot": string(bytes.Repeat([]byte("b"), 64)), "ownbotd": string(bytes.Repeat([]byte("c"), 64))}}}}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := json.Marshal(Signature{KeyID: "fixture-test-key", Signature: hex.EncodeToString(ed25519.Sign(private, raw))})
	if err != nil {
		t.Fatal(err)
	}
	return raw, sig, map[string]ed25519.PublicKey{"fixture-test-key": public}
}

func TestVerifyReleaseAcceptsSignedManifest(t *testing.T) {
	raw, sig, keys := signedFixture(t)
	manifest, asset, err := VerifyRelease(raw, sig, keys, "linux", "amd64", "v1.2.3")
	if err != nil || manifest.Version != "1.2.3" || asset.OS != "linux" {
		t.Fatalf("manifest=%+v asset=%+v err=%v", manifest, asset, err)
	}
}

func TestVerifyReleaseRejectsTamperWrongVersionPlatformAndPath(t *testing.T) {
	raw, sig, keys := signedFixture(t)
	tampered := append(append([]byte(nil), raw...), ' ')
	if _, _, err := VerifyRelease(tampered, sig, keys, "linux", "amd64", ""); err == nil {
		t.Fatal("tampered signed manifest was accepted")
	}
	if _, _, err := VerifyRelease(raw, sig, keys, "linux", "amd64", "2.0.0"); err == nil {
		t.Fatal("wrong requested version was accepted")
	}
	if _, _, err := VerifyRelease(raw, sig, keys, "linux", "arm64", ""); err == nil {
		t.Fatal("missing platform asset was accepted")
	}

	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Assets[0].File = "../escape.tar.gz"
	bad, _ := json.Marshal(manifest)
	badSig, _ := json.Marshal(Signature{KeyID: "fixture-test-key", Signature: hex.EncodeToString(ed25519.Sign(private, bad))})
	if _, _, err := VerifyRelease(bad, badSig, map[string]ed25519.PublicKey{"fixture-test-key": public}, "linux", "amd64", ""); err == nil {
		t.Fatal("signed path traversal was accepted")
	}
}

func TestInstallHealthFailureRestoresActiveVersionAndKeepsData(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture archive uses the Unix release format")
	}
	dataDir := t.TempDir()
	if err := os.Chmod(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(VersionDir(dataDir, "1.0.0"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeActive(dataDir, Active{Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(dataDir, "ownbot.db")
	if err := os.WriteFile(data, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	archive := fixtureTarGzip(t)
	digest := sha256.Sum256(archive)
	manifest := Manifest{Name: "ownbot", Version: "1.2.3"}
	asset := Asset{OS: runtime.GOOS, Arch: runtime.GOARCH, File: archiveName(manifest.Version, runtime.GOOS, runtime.GOARCH), SHA256: hex.EncodeToString(digest[:]), Binaries: map[string]string{}}
	cliName, daemonName := binaryNames(runtime.GOOS)
	cliDigest := sha256.Sum256([]byte("fixture " + cliName))
	daemonDigest := sha256.Sum256([]byte("fixture " + daemonName))
	asset.Binaries[cliName], asset.Binaries[daemonName] = hex.EncodeToString(cliDigest[:]), hex.EncodeToString(daemonDigest[:])
	checks := 0
	err := Install(dataDir, manifest, asset, archive, func(_ string) error {
		checks++
		if checks == 2 {
			return os.ErrInvalid
		}
		return nil
	})
	if err == nil || checks != 3 {
		t.Fatalf("expected activation health failure, checks=%d err=%v", checks, err)
	}
	active, err := ReadActive(dataDir)
	if err != nil || active.Version != "1.0.0" {
		t.Fatalf("active=%+v err=%v", active, err)
	}
	if _, err := os.Stat(VersionDir(dataDir, "1.2.3")); !os.IsNotExist(err) {
		t.Fatalf("failed version was not removed: %v", err)
	}
	if got, err := os.ReadFile(data); err != nil || string(got) != "preserve" {
		t.Fatalf("state changed: %q err=%v", got, err)
	}
}

func TestRollbackWaitsForFailedInstallTransaction(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture archive uses the Unix release format")
	}
	dataDir := t.TempDir()
	if err := os.Chmod(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"3.0.0", "4.0.0"} {
		if err := os.MkdirAll(VersionDir(dataDir, version), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeActive(dataDir, Active{Version: "4.0.0", Previous: "3.0.0"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "ownbot.db"), []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	archive := fixtureTarGzip(t)
	digest := sha256.Sum256(archive)
	manifest := Manifest{Name: "ownbot", Version: "5.0.0"}
	asset := Asset{OS: runtime.GOOS, Arch: runtime.GOARCH, File: archiveName(manifest.Version, runtime.GOOS, runtime.GOARCH), SHA256: hex.EncodeToString(digest[:]), Binaries: map[string]string{}}
	cliName, daemonName := binaryNames(runtime.GOOS)
	asset.Binaries[cliName] = digestBytes([]byte("fixture " + cliName))
	asset.Binaries[daemonName] = digestBytes([]byte("fixture " + daemonName))
	activated := make(chan struct{})
	allowInstallFailure := make(chan struct{})
	installResult := make(chan error, 1)
	go func() {
		checks := 0
		installResult <- Install(dataDir, manifest, asset, archive, func(string) error {
			checks++
			if checks == 2 {
				close(activated)
				<-allowInstallFailure
				return os.ErrInvalid
			}
			return nil
		})
	}()
	<-activated

	rollbackStarted := make(chan struct{})
	rollbackTarget := make(chan string, 1)
	rollbackResult := make(chan error, 1)
	go func() {
		close(rollbackStarted)
		rollbackResult <- Rollback(dataDir, func(path string) error {
			rollbackTarget <- filepath.Base(path)
			return nil
		})
	}()
	<-rollbackStarted
	select {
	case err := <-rollbackResult:
		t.Fatalf("rollback escaped the in-flight install transaction: %v", err)
	case <-time.After(75 * time.Millisecond):
	}
	close(allowInstallFailure)
	if err := <-installResult; err == nil {
		t.Fatal("expected activated health check failure")
	}
	if err := <-rollbackResult; err != nil {
		t.Fatalf("rollback after failed install: %v", err)
	}
	if target := <-rollbackTarget; target != "3.0.0" {
		t.Fatalf("rollback selected %s instead of the retained prior release", target)
	}
	active, err := ReadActive(dataDir)
	if err != nil || active.Version != "3.0.0" || active.Previous != "4.0.0" {
		t.Fatalf("active=%+v err=%v", active, err)
	}
	if _, err := os.Stat(VersionDir(dataDir, "5.0.0")); !os.IsNotExist(err) {
		t.Fatalf("failed install was not removed after lock release: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dataDir, "ownbot.db")); err != nil || string(got) != "preserve" {
		t.Fatalf("database changed: %q err=%v", got, err)
	}
}

func TestRollbackSwitchesVersionAndPreservesData(t *testing.T) {
	dataDir := t.TempDir()
	for _, version := range []string{"1.0.0", "1.2.3"} {
		if err := os.MkdirAll(VersionDir(dataDir, version), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeActive(dataDir, Active{Version: "1.2.3", Previous: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(dataDir, "api.token")
	if err := os.WriteFile(data, []byte("token"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Rollback(dataDir, func(path string) error {
		if filepath.Base(path) != "1.0.0" {
			t.Fatalf("health checked %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	active, err := ReadActive(dataDir)
	if err != nil || active.Version != "1.0.0" || active.Previous != "1.2.3" {
		t.Fatalf("active=%+v err=%v", active, err)
	}
	if got, err := os.ReadFile(data); err != nil || string(got) != "token" {
		t.Fatalf("state changed: %q err=%v", got, err)
	}
}

func TestArchiveNameUsesCrossPlatformTarFormat(t *testing.T) {
	if got := archiveName("1.2.3", "windows", "amd64"); got != "ownbot-1.2.3-windows-amd64.tar.gz" {
		t.Fatalf("Windows archive name = %q", got)
	}
}

func TestNPMReleaseSeedsSignedRollbackAndPreservesData(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture archive uses the Unix release format")
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]ed25519.PublicKey{"fixture-test-key": public}
	dataDir := t.TempDir()
	if err := os.Chmod(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dataDir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"model":"local"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "ownbot.db"), []byte("state-v4"), 0600); err != nil {
		t.Fatal(err)
	}
	installedV4 := filepath.Join(t.TempDir(), "4.0.0")
	if err := os.MkdirAll(installedV4, 0700); err != nil {
		t.Fatal(err)
	}
	cliName, daemonName := binaryNames(runtime.GOOS)
	v4CLI, v4Daemon := []byte("signed npm cli v4"), []byte("signed npm daemon v4")
	if err := os.WriteFile(filepath.Join(installedV4, cliName), v4CLI, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installedV4, daemonName), v4Daemon, 0700); err != nil {
		t.Fatal(err)
	}
	v4Asset := Asset{OS: runtime.GOOS, Arch: runtime.GOARCH, File: archiveName("4.0.0", runtime.GOOS, runtime.GOARCH), SHA256: strings.Repeat("0", 64), Binaries: map[string]string{cliName: digestBytes(v4CLI), daemonName: digestBytes(v4Daemon)}}
	v4Manifest := Manifest{Name: "ownbot", Version: "4.0.0", Assets: []Asset{v4Asset}}
	v4Raw, v4Sig := signManifestFixture(t, v4Manifest, private)
	if err := os.WriteFile(filepath.Join(installedV4, "manifest.json"), v4Raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installedV4, "manifest.sig"), v4Sig, 0600); err != nil {
		t.Fatal(err)
	}
	if version, err := SeedBaseline(dataDir, installedV4, runtime.GOOS, runtime.GOARCH, keys); err != nil || version != "4.0.0" {
		t.Fatalf("seed version=%q err=%v", version, err)
	}
	archiveV5 := fixtureTarGzip(t)
	cliName, daemonName = binaryNames(runtime.GOOS)
	v5Asset := Asset{OS: runtime.GOOS, Arch: runtime.GOARCH, File: archiveName("5.0.0", runtime.GOOS, runtime.GOARCH), SHA256: sha256Hex(archiveV5), Binaries: map[string]string{
		cliName: digestBytes([]byte("fixture " + cliName)), daemonName: digestBytes([]byte("fixture " + daemonName)),
	}}
	v5Manifest := Manifest{Name: "ownbot", Version: "5.0.0", Assets: []Asset{v5Asset}}
	v5Raw, v5Sig := signManifestFixture(t, v5Manifest, private)
	verifiedV5, assetV5, err := VerifyRelease(v5Raw, v5Sig, keys, runtime.GOOS, runtime.GOARCH, "5.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := Install(dataDir, verifiedV5, assetV5, archiveV5, func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	active, err := ReadActive(dataDir)
	if err != nil || active.Version != "5.0.0" || active.Previous != "4.0.0" {
		t.Fatalf("post-update active=%+v err=%v", active, err)
	}
	if err := Rollback(dataDir, func(path string) error {
		if filepath.Base(path) != "4.0.0" {
			t.Fatalf("rollback health checked %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	active, err = ReadActive(dataDir)
	if err != nil || active.Version != "4.0.0" || active.Previous != "5.0.0" {
		t.Fatalf("post-rollback active=%+v err=%v", active, err)
	}
	if got, err := os.ReadFile(filepath.Join(dataDir, "ownbot.db")); err != nil || string(got) != "state-v4" {
		t.Fatalf("database changed: %q err=%v", got, err)
	}
	if got, err := os.ReadFile(configPath); err != nil || string(got) != `{"model":"local"}` {
		t.Fatalf("config changed: %q err=%v", got, err)
	}
}

func TestSeedBaselineRejectsTamperedInstalledBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture archive uses the Unix release format")
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]ed25519.PublicKey{"fixture-test-key": public}
	installed := filepath.Join(t.TempDir(), "4.0.0")
	if err := os.MkdirAll(installed, 0700); err != nil {
		t.Fatal(err)
	}
	cliName, daemonName := binaryNames(runtime.GOOS)
	cliBytes, daemonBytes := []byte("cli"), []byte("daemon")
	if err := os.WriteFile(filepath.Join(installed, cliName), []byte("tampered"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installed, daemonName), daemonBytes, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Name: "ownbot", Version: "4.0.0", Assets: []Asset{{OS: runtime.GOOS, Arch: runtime.GOARCH, File: archiveName("4.0.0", runtime.GOOS, runtime.GOARCH), SHA256: strings.Repeat("0", 64), Binaries: map[string]string{cliName: digestBytes(cliBytes), daemonName: digestBytes(daemonBytes)}}}}
	raw, signature := signManifestFixture(t, manifest, private)
	if err := os.WriteFile(filepath.Join(installed, "manifest.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installed, "manifest.sig"), signature, 0600); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	if err := os.Chmod(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := SeedBaseline(dataDir, installed, runtime.GOOS, runtime.GOARCH, keys); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected binary tamper rejection, got %v", err)
	}
}

func digestBytes(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func signManifestFixture(t *testing.T, manifest Manifest, private ed25519.PrivateKey) ([]byte, []byte) {
	t.Helper()
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := json.Marshal(Signature{KeyID: "fixture-test-key", Signature: hex.EncodeToString(ed25519.Sign(private, raw))})
	if err != nil {
		t.Fatal(err)
	}
	return raw, sig
}

func fixtureTarGzip(t *testing.T) []byte {
	t.Helper()
	var output bytes.Buffer
	gz := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gz)
	for _, name := range []string{"ownbot", "ownbotd"} {
		body := []byte("fixture " + name)
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
