package certs

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAllAndVerify(t *testing.T) {
	dir := t.TempDir()
	if err := WriteAll(dir, []string{"127.0.0.1"}, []string{"192.168.1.50", "client.lan"}, 3650); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"ca.crt", "ca.key", "server.crt", "server.key", "client.crt", "client.key"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("нет файла %s: %v", f, err)
		}
	}

	// keypair загружается
	if _, err := tls.LoadX509KeyPair(filepath.Join(dir, "client.crt"), filepath.Join(dir, "client.key")); err != nil {
		t.Fatalf("client keypair: %v", err)
	}
	if _, err := tls.LoadX509KeyPair(filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key")); err != nil {
		t.Fatalf("server keypair: %v", err)
	}

	// CA в пул
	caPEM, _ := os.ReadFile(filepath.Join(dir, "ca.crt"))
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("CA не распарсился")
	}

	// client.crt подписан CA и содержит нужный SAN
	clientPEM, _ := os.ReadFile(filepath.Join(dir, "client.crt"))
	block, _ := pem.Decode(clientPEM)
	if block == nil {
		t.Fatal("client.crt: PEM не распарсился")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		t.Fatalf("цепочка до CA не проверяется: %v", err)
	}
	foundIP := false
	for _, ip := range leaf.IPAddresses {
		if ip.Equal(net.ParseIP("192.168.1.50")) {
			foundIP = true
		}
	}
	if !foundIP {
		t.Fatalf("SAN client.crt не содержит IP клиента: %v", leaf.IPAddresses)
	}
	foundDNS := false
	for _, d := range leaf.DNSNames {
		if d == "client.lan" {
			foundDNS = true
		}
	}
	if !foundDNS {
		t.Fatalf("SAN client.crt не содержит DNS: %v", leaf.DNSNames)
	}
}
