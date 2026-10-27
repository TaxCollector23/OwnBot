package daemon

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// LAN access binds one private interface, never all interfaces or a public IP.
func validateLANAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil { return fmt.Errorf("invalid LAN address: %w", err) }
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsPrivate() || ip.IsLoopback() {
		return errors.New("LAN address must use a private interface IP (not 0.0.0.0 or a public IP)")
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 { return errors.New("LAN port must be between 1 and 65535") }
	return nil
}

// DefaultLANAddress chooses a physical interface before virtual/VPN interfaces.
// Users can select a different private interface with --lan-address.
func DefaultLANAddress() (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil { return "", fmt.Errorf("detect LAN interface: %w", err) }
	sort.SliceStable(interfaces, func(i,j int) bool { return interfaces[i].Index < interfaces[j].Index })
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 { continue }
		name := strings.ToLower(iface.Name)
		virtual := false
		for _, prefix := range []string{"utun", "tun", "tap", "docker", "veth", "br-", "wg", "tailscale"} {
			if strings.HasPrefix(name, prefix) { virtual = true; break }
		}
		if virtual { continue }
		addresses, _ := iface.Addrs()
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err == nil && ip.To4() != nil && ip.IsPrivate() && !ip.IsLoopback() {
				return net.JoinHostPort(ip.String(), "8766"), nil
			}
		}
	}
	return "", errors.New("no private LAN interface found; connect to Wi-Fi/Ethernet or select --lan-address IP:8766")
}

// LANCertificate reuses a certificate for the selected IP until renewal is
// necessary. The private key stays in the protected daemon state directory.
func LANCertificate(dir, address string) (*tls.Config, string, error) {
	if err := validateLANAddress(address); err != nil { return nil, "", err }
	host, _, _ := net.SplitHostPort(address)
	certPath, keyPath := filepath.Join(dir,"lan.cert.pem"), filepath.Join(dir,"lan.key.pem")
	for _, path := range []string{certPath,keyPath} {
		info, err := os.Lstat(path)
		if errors.Is(err,os.ErrNotExist) { continue }
		if err != nil { return nil,"",err }
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
			return nil,"",errors.New("LAN certificate/key must be regular private files")
		}
	}
	pair, err := tls.LoadX509KeyPair(certPath,keyPath)
	if err == nil {
		cert, e := x509.ParseCertificate(pair.Certificate[0])
		if e == nil && cert.VerifyHostname(host) == nil && time.Now().After(cert.NotBefore) && time.Until(cert.NotAfter) > 24*time.Hour {
			return lanTLS(pair), certificateFingerprint(cert.Raw), nil
		}
	} else if !errors.Is(err,os.ErrNotExist) {
		return nil,"",fmt.Errorf("read LAN certificate: %w",err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(),rand.Reader)
	if err != nil { return nil,"",err }
	serial, err := rand.Int(rand.Reader,new(big.Int).Lsh(big.NewInt(1),128))
	if err != nil { return nil,"",err }
	now := time.Now()
	template := &x509.Certificate{SerialNumber:serial,Subject:pkix.Name{CommonName:"OwnYourBot local network"},NotBefore:now.Add(-5*time.Minute),NotAfter:now.AddDate(1,0,0),IPAddresses:[]net.IP{net.ParseIP(host)},KeyUsage:x509.KeyUsageDigitalSignature,ExtKeyUsage:[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},BasicConstraintsValid:true}
	der, err := x509.CreateCertificate(rand.Reader,template,template,&key.PublicKey,key)
	if err != nil { return nil,"",err }
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil { return nil,"",err }
	certPEM, keyPEM := pem.EncodeToMemory(&pem.Block{Type:"CERTIFICATE",Bytes:der}), pem.EncodeToMemory(&pem.Block{Type:"PRIVATE KEY",Bytes:keyDER})
	// Both files are replaced atomically individually; a mismatched pair fails
	// closed on startup rather than silently regenerating trust material.
	if err = replacePrivateFile(keyPath,keyPEM); err != nil { return nil,"",err }
	if err = replacePrivateFile(certPath,certPEM); err != nil { return nil,"",err }
	pair, err = tls.X509KeyPair(certPEM,keyPEM)
	if err != nil { return nil,"",err }
	return lanTLS(pair), certificateFingerprint(der), nil
}

func lanTLS(pair tls.Certificate) *tls.Config {
	return &tls.Config{MinVersion:tls.VersionTLS13,Certificates:[]tls.Certificate{pair}}
}
func certificateFingerprint(der []byte) string {
	sum:=sha256.Sum256(der)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}
func replacePrivateFile(path string, data []byte) error {
	f,err:=os.CreateTemp(filepath.Dir(path),".lan-*")
	if err!=nil{return err}; defer os.Remove(f.Name())
	if err=f.Chmod(0600);err!=nil{f.Close();return err}
	if _,err=f.Write(data);err!=nil{f.Close();return err}
	if err=f.Sync();err!=nil{f.Close();return err}
	if err=f.Close();err!=nil{return err}
	return os.Rename(f.Name(),path)
}
