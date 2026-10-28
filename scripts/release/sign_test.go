package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeygenAndSignUsePrivateFilesWithoutPrintingKeyMaterial(t *testing.T) {
	dir := t.TempDir()
	privatePath := filepath.Join(dir, "private.key")
	publicPath := filepath.Join(dir, "public.key")
	previous := os.Stdout
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writePipe
	if err := keygen([]string{"--key-id", "fixture-test-key", "--private-out", privatePath, "--public-out", publicPath}); err != nil {
		os.Stdout = previous
		t.Fatal(err)
	}
	os.Stdout = previous
	if err := writePipe.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(readPipe)
	if err != nil {
		t.Fatal(err)
	}
	_ = readPipe.Close()
	privateInfo, err := os.Stat(privatePath)
	if err != nil || privateInfo.Mode().Perm() != 0600 {
		t.Fatalf("private key mode info=%v err=%v", privateInfo, err)
	}
	privateText, err := os.ReadFile(privatePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(output), strings.TrimSpace(string(privateText))) {
		t.Fatal("key generation printed private key bytes")
	}
	publicText, err := os.ReadFile(publicPath)
	if err != nil {
		t.Fatal(err)
	}
	publicParts := strings.Fields(string(publicText))
	if len(publicParts) != 2 || publicParts[0] != "fixture-test-key" {
		t.Fatalf("unexpected public key file: %q", publicText)
	}
	public, err := base64.StdEncoding.DecodeString(publicParts[1])
	if err != nil || len(public) != ed25519.PublicKeySize {
		t.Fatalf("public key length=%d err=%v", len(public), err)
	}
	manifestPath := filepath.Join(dir, "manifest.json")
	manifest := []byte(`{"name":"ownbot","version":"0.1.0"}`)
	if err := os.WriteFile(manifestPath, manifest, 0600); err != nil {
		t.Fatal(err)
	}
	signaturePath := filepath.Join(dir, "manifest.sig")
	if err := sign([]string{"--key-id", "fixture-test-key", "--private-key", privatePath, "--manifest", manifestPath, "--signature", signaturePath}); err != nil {
		t.Fatal(err)
	}
	signatureBytes, err := os.ReadFile(signaturePath)
	if err != nil {
		t.Fatal(err)
	}
	var signature signature
	if err := json.Unmarshal(signatureBytes, &signature); err != nil {
		t.Fatal(err)
	}
	if signature.KeyID != "fixture-test-key" || !ed25519.Verify(ed25519.PublicKey(public), manifest, mustDecodeHex(t, signature.Signature)) {
		t.Fatal("detached signature did not verify")
	}
	if !strings.HasSuffix(string(privateText), "\n") {
		t.Fatal("private key file is not newline terminated")
	}
}

func TestSignRejectsInsecurePrivateFilePermissions(t *testing.T) {
	dir := t.TempDir()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_ = public
	privatePath := filepath.Join(dir, "private.key")
	if err := os.WriteFile(privatePath, []byte(base64.StdEncoding.EncodeToString(private)), 0644); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(manifestPath, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	err = sign([]string{"--key-id", "fixture-test-key", "--private-key", privatePath, "--manifest", manifestPath, "--signature", filepath.Join(dir, "manifest.sig")})
	if err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Fatalf("expected key permission rejection, got %v", err)
	}
}

func mustDecodeHex(t *testing.T, input string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(input)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}
