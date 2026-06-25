// Package tlsconn (tlsconfig.go) — построение tls.Config для mTLS (раздел 6.1 ТЗ):
// самоподписанные сертификаты, взаимная проверка по общему CA, минимум TLS 1.2.
package tlsconn

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

func loadBase(certFile, keyFile, caFile string) (tls.Certificate, *x509.CertPool, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("загрузка сертификата/ключа: %w", err)
	}
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("чтение CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return tls.Certificate{}, nil, fmt.Errorf("CA %s не содержит валидных сертификатов", caFile)
	}
	return cert, pool, nil
}

// DialerTLS — конфиг для инициатора (backuper-server подключается к клиенту).
// Проверяет сертификат слушателя по CA; ServerName сверяется с SAN.
func DialerTLS(certFile, keyFile, caFile string, minVersion uint16, serverName string) (*tls.Config, error) {
	cert, pool, err := loadBase(certFile, keyFile, caFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		MinVersion:   minVersion,
		ServerName:   serverName,
	}, nil
}

// ListenerTLS — конфиг для слушателя (backuper-client), требует и проверяет
// клиентский сертификат инициатора по CA (mTLS).
func ListenerTLS(certFile, keyFile, caFile string, minVersion uint16) (*tls.Config, error) {
	cert, pool, err := loadBase(certFile, keyFile, caFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   minVersion,
	}, nil
}
