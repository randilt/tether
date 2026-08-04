package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	certDir      = "certs"
	certFile     = "certs/cert.pem" // server leaf (+ CA chain)
	keyFile      = "certs/key.pem"  // server private key
	caFile       = "certs/ca.pem"
	certDERFile  = "certs/cert.cer" // CA DER — what the phone installs
	certValidity = 365 * 24 * time.Hour
)

// ensureCert creates a local CA + server cert (LAN SANs) on first run.
// /cert.cer serves the CA so iOS Certificate Trust Settings can enable Full Trust.
func ensureCert() (string, string, error) {
	if fileExists(certFile) && fileExists(keyFile) && fileExists(certDERFile) && fileExists(caFile) {
		return certFile, keyFile, nil
	}

	if err := os.MkdirAll(certDir, 0o755); err != nil {
		return "", "", fmt.Errorf("create cert dir: %w", err)
	}

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate CA key: %w", err)
	}
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate server key: %w", err)
	}

	caSerial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		return "", "", err
	}
	serverSerial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		return "", "", err
	}

	caTmpl := &x509.Certificate{
		SerialNumber: caSerial,
		Subject: pkix.Name{
			Organization: []string{"Tether Local"},
			CommonName:   "Tether Local CA",
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(certValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}

	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return "", "", fmt.Errorf("create CA: %w", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return "", "", err
	}

	hosts := localHosts()
	serverTmpl := &x509.Certificate{
		SerialNumber: serverSerial,
		Subject: pkix.Name{
			Organization: []string{"Tether Local"},
			CommonName:   "Tether",
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(certValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           hosts,
	}

	serverDER, err := x509.CreateCertificate(rand.Reader, serverTmpl, caCert, &serverKey.PublicKey, caKey)
	if err != nil {
		return "", "", fmt.Errorf("create server cert: %w", err)
	}

	// TLS leaf + CA (chain) in cert.pem
	certOut, err := os.OpenFile(certFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return "", "", err
	}
	for _, der := range [][]byte{serverDER, caDER} {
		if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
			_ = certOut.Close()
			return "", "", err
		}
	}
	if err := certOut.Close(); err != nil {
		return "", "", err
	}

	if err := writePEM(caFile, "CERTIFICATE", caDER, 0o644); err != nil {
		return "", "", err
	}
	// Phone installs the CA (must be a root for iOS Full Trust toggle).
	if err := os.WriteFile(certDERFile, caDER, 0o644); err != nil {
		return "", "", fmt.Errorf("write der CA: %w", err)
	}

	keyBytes, err := x509.MarshalECPrivateKey(serverKey)
	if err != nil {
		return "", "", err
	}
	if err := writePEM(keyFile, "EC PRIVATE KEY", keyBytes, 0o600); err != nil {
		return "", "", err
	}

	fmt.Printf("generated CA + server cert → %s (SANs: localhost", certFile)
	for _, ip := range hosts {
		fmt.Printf(", %s", ip)
	}
	fmt.Println(")")
	fmt.Println("iPhone: install /cert.cer (CA), then enable Full Trust in Certificate Trust Settings")

	return certFile, keyFile, nil
}

func writePEM(path, typ string, der []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: typ, Bytes: der})
}

func localHosts() []net.IP {
	ips := []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}
	ifaces, err := net.Interfaces()
	if err != nil {
		return ips
	}
	seen := map[string]bool{"127.0.0.1": true, "::1": true}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLinkLocalUnicast() {
				continue
			}
			key := ip.String()
			if seen[key] {
				continue
			}
			seen[key] = true
			ips = append(ips, ip)
		}
	}
	return ips
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func certDERPath() string {
	return filepath.Clean(certDERFile)
}
