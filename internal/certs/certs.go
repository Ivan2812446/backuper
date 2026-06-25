// Package certs — генерация самоподписанных CA и leaf-сертификатов для mTLS
// (раздел 6.1 ТЗ). Используется подкомандой gen-certs и интеграционными тестами.
package certs

import (
	"crypto/rand"
	"crypto/rsa"
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

func serial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}

func applySANs(cert *x509.Certificate, hosts []string) {
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			cert.IPAddresses = append(cert.IPAddresses, ip)
		} else {
			cert.DNSNames = append(cert.DNSNames, h)
		}
	}
}

// GenerateCA создаёт самоподписанный корневой CA (RSA-2048).
func GenerateCA(days int) (certPEM, keyPEM []byte, caCert *x509.Certificate, caKey *rsa.PrivateKey, err error) {
	caKey, err = rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	sn, err := serial()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          sn,
		Subject:               pkix.Name{CommonName: "Backuper CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(0, 0, days),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	caCert, err = x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(caKey)})
	return certPEM, keyPEM, caCert, caKey, nil
}

// GenerateLeaf создаёт leaf-сертификат (RSA-2048), подписанный CA, с SAN из hosts.
func GenerateLeaf(cn string, hosts []string, days int, caCert *x509.Certificate, caKey *rsa.PrivateKey) (certPEM, keyPEM []byte, err error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}
	sn, err := serial()
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: sn,
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(0, 0, days),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	applySANs(tmpl, hosts)
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return certPEM, keyPEM, nil
}

func writeFile(path string, data []byte, perm os.FileMode) error {
	return os.WriteFile(path, data, perm)
}

// WriteAll генерирует в dir набор: ca.crt/ca.key, server.crt/server.key, client.crt/client.key.
// serverHosts/clientHosts — списки IP/hostname для SAN (если serverHosts пуст — localhost).
func WriteAll(dir string, serverHosts, clientHosts []string, days int) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	caCertPEM, caKeyPEM, caCert, caKey, err := GenerateCA(days)
	if err != nil {
		return fmt.Errorf("CA: %w", err)
	}
	if err := writeFile(filepath.Join(dir, "ca.crt"), caCertPEM, 0o644); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(dir, "ca.key"), caKeyPEM, 0o600); err != nil {
		return err
	}
	if len(serverHosts) == 0 {
		serverHosts = []string{"localhost", "127.0.0.1"}
	}
	for _, leaf := range []struct {
		name, cn string
		hosts    []string
	}{
		{"server", "backuper-server", serverHosts},
		{"client", "backuper-client", clientHosts},
	} {
		certPEM, keyPEM, err := GenerateLeaf(leaf.cn, leaf.hosts, days, caCert, caKey)
		if err != nil {
			return fmt.Errorf("%s: %w", leaf.name, err)
		}
		if err := writeFile(filepath.Join(dir, leaf.name+".crt"), certPEM, 0o644); err != nil {
			return err
		}
		if err := writeFile(filepath.Join(dir, leaf.name+".key"), keyPEM, 0o600); err != nil {
			return err
		}
	}
	return nil
}
