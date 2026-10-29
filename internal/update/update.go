// Package update verifies signed OwnBot releases and installs them side by side.
package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

const maxArchiveSize = 256 << 20
const maxBinarySize = 128 << 20

var versionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)

type Asset struct {
	OS       string            `json:"os"`
	Arch     string            `json:"arch"`
	File     string            `json:"file"`
	SHA256   string            `json:"sha256"`
	Binaries map[string]string `json:"binaries"`
}

type Manifest struct {
	Name    string  `json:"name"`
	Version string  `json:"version"`
	Assets  []Asset `json:"assets"`
}

type Signature struct {
	KeyID     string `json:"key_id"`
	Signature string `json:"signature"`
}

// VerifyRelease authenticates the exact manifest bytes before trusting any fields.
func VerifyRelease(rawManifest, rawSignature []byte, keys map[string]ed25519.PublicKey, goos, goarch, wantedVersion string) (Manifest, Asset, error) {
	var manifest Manifest
	var signature Signature
	if err := strictJSON(rawSignature, &signature); err != nil {
		return manifest, Asset{}, fmt.Errorf("parse release signature: %w", err)
	}
	key, ok := keys[signature.KeyID]
	if !ok || len(key) != ed25519.PublicKeySize {
		return manifest, Asset{}, errors.New("release signing key is not trusted")
	}
	sig, err := hex.DecodeString(signature.Signature)
	if err != nil || !ed25519.Verify(key, rawManifest, sig) {
		return manifest, Asset{}, errors.New("release manifest signature is invalid")
	}
	if err := strictJSON(rawManifest, &manifest); err != nil {
		return manifest, Asset{}, fmt.Errorf("parse signed release manifest: %w", err)
	}
	if manifest.Name != "ownbot" || !versionPattern.MatchString(manifest.Version) {
		return manifest, Asset{}, errors.New("signed release identity or version is invalid")
	}
	if wantedVersion != "" && strings.TrimPrefix(wantedVersion, "v") != manifest.Version {
		return manifest, Asset{}, errors.New("signed release version does not match the requested version")
	}
	if !supportedTarget(goos, goarch) {
		return manifest, Asset{}, fmt.Errorf("signed updates are not available for %s/%s", goos, goarch)
	}
	var match Asset
	seen := map[string]bool{}
	for _, asset := range manifest.Assets {
		if !supportedTarget(asset.OS, asset.Arch) {
			return manifest, Asset{}, fmt.Errorf("signed release contains unsupported target %s/%s", asset.OS, asset.Arch)
		}
		filename := archiveName(manifest.Version, asset.OS, asset.Arch)
		if asset.File != filename || filepath.Base(asset.File) != asset.File || seen[asset.File] {
			return manifest, Asset{}, errors.New("signed release contains an unsafe, invalid, or duplicate asset path")
		}
		if len(asset.SHA256) != 64 {
			return manifest, Asset{}, errors.New("signed release contains an invalid asset digest")
		}
		if _, err := hex.DecodeString(asset.SHA256); err != nil {
			return manifest, Asset{}, errors.New("signed release contains an invalid asset digest")
		}
		cliName, daemonName := binaryNames(asset.OS)
		if len(asset.Binaries) != 2 || !validDigest(asset.Binaries[cliName]) || !validDigest(asset.Binaries[daemonName]) {
			return manifest, Asset{}, errors.New("signed release contains invalid binary digests")
		}
		seen[asset.File] = true
		if asset.OS == goos && asset.Arch == goarch {
			if match.File != "" {
				return manifest, Asset{}, errors.New("signed release duplicates the current platform")
			}
			match = asset
		}
	}
	if match.File == "" {
		return manifest, Asset{}, fmt.Errorf("signed release does not include %s/%s", goos, goarch)
	}
	return manifest, match, nil
}

func strictJSON(raw []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return errors.New("expected exactly one JSON value")
	}
	return nil
}

func supportedTarget(goos, goarch string) bool {
	switch goos + "/" + goarch {
	case "darwin/arm64", "darwin/amd64", "linux/arm64", "linux/amd64", "windows/amd64", "freebsd/amd64":
		return true
	default:
		return false
	}
}

func archiveName(version, goos, goarch string) string {
	return "ownbot-" + version + "-" + goos + "-" + goarch + ".tar.gz"
}

type Active struct {
	Version  string `json:"version"`
	Previous string `json:"previous,omitempty"`
}

func VersionDir(dataDir, version string) string {
	return filepath.Join(dataDir, "updates", "versions", version)
}

func ActiveFile(dataDir string) string {
	return filepath.Join(dataDir, "updates", "active.json")
}

func ReadActive(dataDir string) (Active, error) {
	var active Active
	for _, directory := range []string{dataDir, filepath.Dir(ActiveFile(dataDir))} {
		if err := checkSecureDirectory(directory); err != nil {
			return active, err
		}
	}
	path := ActiveFile(dataDir)
	info, err := os.Lstat(path)
	if err != nil {
		return active, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return active, errors.New("active release marker is not a secure regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return active, err
	}
	if err := strictJSON(raw, &active); err != nil {
		return active, fmt.Errorf("read active release: %w", err)
	}
	if !versionPattern.MatchString(active.Version) || (active.Previous != "" && !versionPattern.MatchString(active.Previous)) {
		return Active{}, errors.New("active release marker is invalid")
	}
	return active, nil
}

func ActiveBinary(dataDir, name string) string {
	active, err := ReadActive(dataDir)
	if err != nil || (name != "ownbot" && name != "ownbotd") {
		return ""
	}
	directory := VersionDir(dataDir, active.Version)
	if err := checkSecureDirectory(filepath.Dir(directory)); err != nil {
		return ""
	}
	directoryInfo, err := os.Lstat(directory)
	if err != nil || !directoryInfo.IsDir() || directoryInfo.Mode()&os.ModeSymlink != 0 || runtime.GOOS != "windows" && directoryInfo.Mode().Perm()&0077 != 0 {
		return ""
	}
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(VersionDir(dataDir, active.Version), name)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return ""
	}
	return path
}

func binaryNames(goos string) (string, string) {
	if goos == "windows" {
		return "ownbot.exe", "ownbotd.exe"
	}
	return "ownbot", "ownbotd"
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}

// SeedBaseline copies an npm-installed release into the private update store
// only after checking its detached signature and both installed binary hashes.
func SeedBaseline(dataDir, installedDir, goos, goarch string, keys map[string]ed25519.PublicKey) (string, error) {
	var version string
	err := withUpdateLock(dataDir, func() error {
		var err error
		version, err = seedBaseline(dataDir, installedDir, goos, goarch, keys)
		return err
	})
	return version, err
}

func seedBaseline(dataDir, installedDir, goos, goarch string, keys map[string]ed25519.PublicKey) (string, error) {
	if _, err := ReadActive(dataDir); err == nil {
		return "", nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	manifestBytes, err := readRegular(filepath.Join(installedDir, "manifest.json"), 1<<20)
	if err != nil {
		return "", errors.New("current install has no signed rollback baseline; install it from a signed npm package first")
	}
	signatureBytes, err := readRegular(filepath.Join(installedDir, "manifest.sig"), 16<<10)
	if err != nil {
		return "", errors.New("current install has no signed rollback baseline; install it from a signed npm package first")
	}
	manifest, asset, err := VerifyRelease(manifestBytes, signatureBytes, keys, goos, goarch, filepath.Base(installedDir))
	if err != nil {
		return "", fmt.Errorf("verify current signed rollback baseline: %w", err)
	}
	if filepath.Base(installedDir) != manifest.Version {
		return "", errors.New("current signed rollback baseline directory does not match its version")
	}
	cliName, daemonName := binaryNames(goos)
	for _, name := range []string{cliName, daemonName} {
		digest, err := hashFile(filepath.Join(installedDir, name))
		if err != nil || digest != asset.Binaries[name] {
			return "", errors.New("current installed binary does not match its signed rollback baseline")
		}
	}
	if err := secureDirectory(dataDir); err != nil {
		return "", fmt.Errorf("secure state directory: %w", err)
	}
	root := filepath.Join(dataDir, "updates", "versions")
	if err := secureDirectory(root); err != nil {
		return "", err
	}
	final := VersionDir(dataDir, manifest.Version)
	if _, err := os.Lstat(final); err == nil {
		return "", errors.New("rollback baseline version path already exists without an active marker")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	stage, err := os.MkdirTemp(root, ".baseline-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	for _, name := range []string{cliName, daemonName} {
		if err := copyFile(filepath.Join(installedDir, name), filepath.Join(stage, name)); err != nil {
			return "", err
		}
		digest, err := hashFile(filepath.Join(stage, name))
		if err != nil || digest != asset.Binaries[name] {
			return "", errors.New("copied rollback binary does not match its signed digest")
		}
	}
	if err := os.Rename(stage, final); err != nil {
		return "", fmt.Errorf("store signed rollback baseline: %w", err)
	}
	if err := writeActive(dataDir, Active{Version: manifest.Version}); err != nil {
		_ = os.RemoveAll(final)
		return "", err
	}
	return manifest.Version, nil
}

// Install verifies an archive, stages both binaries, runs health, then atomically
// switches the active marker. The callback may start the new daemon and should
// return an error if it does not become healthy; the old marker is restored.
func Install(dataDir string, manifest Manifest, asset Asset, archive []byte, health func(string) error) error {
	return withUpdateLock(dataDir, func() error {
		return install(dataDir, manifest, asset, archive, health)
	})
}

func install(dataDir string, manifest Manifest, asset Asset, archive []byte, health func(string) error) error {
	if len(archive) == 0 || len(archive) > maxArchiveSize || sha256Hex(archive) != asset.SHA256 {
		return errors.New("release archive is empty, too large, or does not match its signed digest")
	}
	if !versionPattern.MatchString(manifest.Version) || manifest.Name != "ownbot" || asset.File != archiveName(manifest.Version, runtime.GOOS, runtime.GOARCH) {
		return errors.New("release target does not match this installation")
	}
	if err := secureDirectory(dataDir); err != nil {
		return fmt.Errorf("secure state directory: %w", err)
	}
	if err := secureDirectory(filepath.Join(dataDir, "updates")); err != nil {
		return fmt.Errorf("secure update state directory: %w", err)
	}
	root := filepath.Join(dataDir, "updates", "versions")
	if err := secureDirectory(root); err != nil {
		return fmt.Errorf("create private release directory: %w", err)
	}
	stage, err := os.MkdirTemp(root, ".stage-")
	if err != nil {
		return fmt.Errorf("stage release: %w", err)
	}
	defer os.RemoveAll(stage)
	err = extractTarGzip(archive, stage)
	if err != nil {
		return err
	}
	cliName, daemonName := "ownbot", "ownbotd"
	if runtime.GOOS == "windows" {
		cliName += ".exe"
		daemonName += ".exe"
	}
	for _, name := range []string{cliName, daemonName} {
		digest, err := hashFile(filepath.Join(stage, name))
		if err != nil || digest != asset.Binaries[name] {
			return errors.New("release archive binary does not match its signed digest")
		}
	}
	for _, name := range []string{cliName, daemonName} {
		if err := os.Chmod(filepath.Join(stage, name), 0700); err != nil {
			return fmt.Errorf("secure staged %s: %w", name, err)
		}
	}
	if health != nil {
		if err := health(stage); err != nil {
			return fmt.Errorf("staged release health check failed: %w", err)
		}
	}
	final := VersionDir(dataDir, manifest.Version)
	if _, err := os.Stat(final); err == nil {
		return fmt.Errorf("release %s is already installed", manifest.Version)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect installed release: %w", err)
	}
	if err := os.Rename(stage, final); err != nil {
		return fmt.Errorf("store verified release: %w", err)
	}
	active, readErr := ReadActive(dataDir)
	previous := ""
	if readErr == nil {
		previous = active.Version
	} else if !errors.Is(readErr, os.ErrNotExist) {
		_ = os.RemoveAll(final)
		return readErr
	}
	if err := writeActive(dataDir, Active{Version: manifest.Version, Previous: previous}); err != nil {
		_ = os.RemoveAll(final)
		return err
	}
	if health != nil {
		if err := health(final); err != nil {
			if previous == "" {
				_ = os.Remove(ActiveFile(dataDir))
			} else {
				_ = writeActive(dataDir, active)
				if restoreErr := health(VersionDir(dataDir, previous)); restoreErr != nil {
					err = errors.Join(err, fmt.Errorf("restore previous release health: %w", restoreErr))
				}
			}
			_ = os.RemoveAll(final)
			return fmt.Errorf("activated release health check failed; previous release restored: %w", err)
		}
	}
	return nil
}

func Rollback(dataDir string, health func(string) error) error {
	return withUpdateLock(dataDir, func() error {
		return rollback(dataDir, health)
	})
}

func rollback(dataDir string, health func(string) error) error {
	active, err := ReadActive(dataDir)
	if err != nil {
		return err
	}
	if active.Previous == "" {
		return errors.New("no previous OwnBot release is available")
	}
	previousDir := VersionDir(dataDir, active.Previous)
	if info, err := os.Lstat(previousDir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("previous OwnBot release is missing")
	}
	if err := writeActive(dataDir, Active{Version: active.Previous, Previous: active.Version}); err != nil {
		return err
	}
	if health != nil {
		if err := health(previousDir); err != nil {
			_ = writeActive(dataDir, active)
			if restoreErr := health(VersionDir(dataDir, active.Version)); restoreErr != nil {
				err = errors.Join(err, fmt.Errorf("restore active release health: %w", restoreErr))
			}
			return fmt.Errorf("rollback health check failed; active release restored: %w", err)
		}
	}
	return nil
}

func writeActive(dataDir string, active Active) error {
	dir := filepath.Dir(ActiveFile(dataDir))
	if err := secureDirectory(dataDir); err != nil {
		return fmt.Errorf("secure state directory: %w", err)
	}
	if err := secureDirectory(dir); err != nil {
		return fmt.Errorf("create update state directory: %w", err)
	}
	raw, err := json.Marshal(active)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(dir, ".active-*.tmp")
	if err != nil {
		return fmt.Errorf("write active release marker: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, ActiveFile(dataDir)); err != nil {
		return fmt.Errorf("activate release atomically: %w", err)
	}
	return nil
}

func extractTarGzip(data []byte, destination string) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("open release archive: %w", err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	seen := map[string]bool{}
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read release archive: %w", err)
		}
		if !safeArchiveName(header.Name) {
			return errors.New("release archive contains an unsafe path")
		}
		if (header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA) || header.Size < 0 || header.Size > maxBinarySize {
			return errors.New("release archive contains an unsafe entry")
		}
		if seen[header.Name] {
			return errors.New("release archive contains a duplicate file")
		}
		seen[header.Name] = true
		cliName, daemonName := "ownbot", "ownbotd"
		if runtime.GOOS == "windows" {
			cliName, daemonName = "ownbot.exe", "ownbotd.exe"
		}
		if header.Name != cliName && header.Name != daemonName {
			if header.Name != "README.md" {
				return errors.New("release archive contains an unexpected file")
			}
			if header.Size > 1<<20 {
				return errors.New("release archive README is too large")
			}
			continue // README is informational and is not installed.
		}
		if err := writeArchiveFile(header.Name, header.Size, reader, destination); err != nil {
			return err
		}
	}
	return requireBinaries(seen)
}

func requireBinaries(seen map[string]bool) error {
	cli, daemon := seen["ownbot"], seen["ownbotd"]
	if runtime.GOOS == "windows" {
		cli, daemon = seen["ownbot.exe"], seen["ownbotd.exe"]
	}
	if !cli || !daemon {
		return errors.New("release archive is missing OwnBot binaries")
	}
	return nil
}

func writeArchiveFile(name string, size int64, reader io.Reader, destination string) error {
	if !safeArchiveName(name) {
		return errors.New("release archive contains an unsafe path")
	}
	file, err := os.OpenFile(filepath.Join(destination, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return err
	}
	written, copyErr := io.CopyN(file, reader, size)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written != size {
		return io.ErrUnexpectedEOF
	}
	return nil
}

func safeArchiveName(name string) bool {
	return name != "" && filepath.Base(name) == name && !filepath.IsAbs(name) && !strings.ContainsAny(name, "/\\:") && name != "." && name != ".."
}

func secureDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("update state path must be a real directory")
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0700); err != nil {
			return err
		}
		info, err = os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode().Perm()&0077 != 0 {
			return errors.New("update state directory is accessible to other users")
		}
	}
	return nil
}

func checkSecureDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("update state directory is not a secure real directory: %s (%s)", path, info.Mode())
	}
	return nil
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func hashFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxBinarySize {
		return "", errors.New("binary is missing, non-regular, or too large")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, io.LimitReader(file, info.Size()+1)); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func readRegular(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > limit {
		return nil, errors.New("file is missing, non-regular, or too large")
	}
	return os.ReadFile(path)
}

func copyFile(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxBinarySize {
		return errors.New("rollback source binary is missing, non-regular, or too large")
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return err
	}
	written, copyErr := io.CopyN(out, in, info.Size())
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written != info.Size() {
		return io.ErrUnexpectedEOF
	}
	return nil
}
