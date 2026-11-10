package daemon

import (
	"crypto/x509"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLANCertificateCanBeInstalledAsVerifiedLocalRoot(t *testing.T) {
	dir := t.TempDir()
	tlsConfig, fingerprint, err := LANCertificate(dir, "192.168.1.12:8766")
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint == "" || tlsConfig == nil || len(tlsConfig.Certificates) != 1 {
		t.Fatalf("incomplete LAN TLS setup: fingerprint=%q config=%+v", fingerprint, tlsConfig)
	}
	der, err := os.ReadFile(filepath.Join(dir, "lan.cert.cer"))
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	if !cert.IsCA || cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Fatalf("LAN certificate is not installable as a local root CA: IsCA=%v KeyUsage=%v", cert.IsCA, cert.KeyUsage)
	}
	if err := cert.VerifyHostname("192.168.1.12"); err != nil {
		t.Fatalf("LAN certificate hostname: %v", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: "192.168.1.12", KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		t.Fatalf("installed CA did not verify the LAN TLS certificate: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "lan.cert.cer"))
	if err != nil {
		t.Fatal(err)
	}
	// Windows does not expose POSIX file permission bits through Mode().Perm();
	// access is controlled by the file's ACL instead.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		t.Fatalf("exported public certificate permissions too broad: %o", info.Mode().Perm())
	}
	profile, err := os.ReadFile(filepath.Join(dir, "lan.cert.mobileconfig"))
	if err != nil {
		t.Fatal(err)
	}
	profileText := string(profile)
	if !strings.Contains(profileText, "com.apple.security.root") || !strings.Contains(profileText, base64.StdEncoding.EncodeToString(der)) || strings.Contains(profileText, "PRIVATE KEY") {
		t.Fatal("iOS profile must contain only the local root certificate")
	}
}
