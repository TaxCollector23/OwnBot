// sign.go uses Go's standard-library Ed25519 implementation for release signing.
// Keep private-key bytes out of command arguments and stdout.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type signature struct {
	KeyID     string `json:"key_id"`
	Signature string `json:"signature"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "release signer:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("use keygen or sign")
	}
	switch args[0] {
	case "keygen":
		return keygen(args[1:])
	case "sign":
		return sign(args[1:])
	default:
		return errors.New("use keygen or sign")
	}
}

func keygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	keyID := fs.String("key-id", "", "public key identifier")
	privatePath := fs.String("private-out", "", "new private key file (created mode 0600)")
	publicPath := fs.String("public-out", "", "new public key file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *keyID == "" || *privatePath == "" || *publicPath == "" || filepath.Clean(*privatePath) == filepath.Clean(*publicPath) {
		return errors.New("keygen requires distinct --private-out and --public-out paths plus --key-id")
	}
	if err := os.MkdirAll(filepath.Dir(*privatePath), 0700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*publicPath), 0755); err != nil {
		return err
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	privateFile, err := os.OpenFile(*privatePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("create private key (refusing to overwrite): %w", err)
	}
	if _, err := privateFile.WriteString(base64.StdEncoding.EncodeToString(private) + "\n"); err != nil {
		_ = privateFile.Close()
		_ = os.Remove(*privatePath)
		return err
	}
	if err := privateFile.Sync(); err != nil {
		_ = privateFile.Close()
		_ = os.Remove(*privatePath)
		return err
	}
	if err := privateFile.Close(); err != nil {
		_ = os.Remove(*privatePath)
		return err
	}
	publicFile, err := os.OpenFile(*publicPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		_ = os.Remove(*privatePath)
		return fmt.Errorf("create public key (refusing to overwrite): %w", err)
	}
	if _, err := fmt.Fprintf(publicFile, "%s %s\n", *keyID, base64.StdEncoding.EncodeToString(public)); err != nil {
		_ = publicFile.Close()
		_ = os.Remove(*privatePath)
		_ = os.Remove(*publicPath)
		return err
	}
	if err := publicFile.Close(); err != nil {
		_ = os.Remove(*privatePath)
		_ = os.Remove(*publicPath)
		return err
	}
	fmt.Fprintf(os.Stdout, "Created key pair. Keep %s private and outside the repository; commit only the public key after review.\n", *privatePath)
	return nil
}

func sign(args []string) error {
	fs := flag.NewFlagSet("sign", flag.ContinueOnError)
	keyID := fs.String("key-id", "", "trusted public key identifier")
	privatePath := fs.String("private-key", "", "private key file created by keygen")
	privateEnv := fs.String("private-key-env", "", "environment variable containing base64 private key")
	manifestPath := fs.String("manifest", "", "release manifest JSON")
	signaturePath := fs.String("signature", "", "detached signature output JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *keyID == "" || *manifestPath == "" || *signaturePath == "" || ((*privatePath == "") == (*privateEnv == "")) {
		return errors.New("sign requires --key-id, --manifest, --signature and exactly one private-key source")
	}
	var privateBytes []byte
	if *privateEnv != "" {
		encoded := strings.TrimSpace(os.Getenv(*privateEnv))
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return errors.New("private key environment variable must contain base64 Ed25519 key bytes")
		}
		privateBytes = decoded
	} else {
		info, err := os.Stat(*privatePath)
		if err != nil {
			return fmt.Errorf("read private key file metadata: %w", err)
		}
		if info.Mode().Perm()&0077 != 0 {
			return errors.New("private key file permissions must not allow group or other access")
		}
		contents, err := os.ReadFile(*privatePath)
		if err != nil {
			return err
		}
		privateBytes, err = base64.StdEncoding.DecodeString(strings.TrimSpace(string(contents)))
		if err != nil {
			return errors.New("private key file must contain base64 Ed25519 key bytes")
		}
	}
	if len(privateBytes) != ed25519.PrivateKeySize {
		return errors.New("private key must be a 64-byte Ed25519 private key")
	}
	manifest, err := os.ReadFile(*manifestPath)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(signature{KeyID: *keyID, Signature: hex.EncodeToString(ed25519.Sign(ed25519.PrivateKey(privateBytes), manifest))})
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(*signaturePath, encoded, 0644); err != nil {
		return err
	}
	return nil
}
