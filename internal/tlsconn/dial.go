// Package tlsconn (dial.go) — установка соединений: dialer инициатора с TLS+auth
// и слушатель ответчика (раздел 5.3, 4.2 ТЗ).
package tlsconn

import (
	"crypto/tls"
	"fmt"
	"net"
	"time"
)

// Dialer создаёт авторизованные соединения от инициатора к слушателю.
type Dialer struct {
	Host           string
	Port           int
	TLSConfig      *tls.Config
	ConnectTimeout time.Duration
	IOTimeout      time.Duration
	MaxFrame       uint64
	OwnPassword    string
	PeerPassword   string
}

// Dial устанавливает TCP+TLS, выполняет рукопожатие и обмен паролями (5.3).
func (d *Dialer) Dial() (*Conn, error) {
	addr := net.JoinHostPort(d.Host, fmt.Sprintf("%d", d.Port))
	nd := &net.Dialer{Timeout: d.ConnectTimeout}
	rawTCP, err := nd.Dial("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("TCP-подключение к %s: %w", addr, err)
	}
	tc := tls.Client(rawTCP, d.TLSConfig)
	if d.ConnectTimeout > 0 {
		_ = tc.SetDeadline(time.Now().Add(d.ConnectTimeout))
	}
	if err := tc.Handshake(); err != nil {
		rawTCP.Close()
		return nil, fmt.Errorf("TLS-рукопожатие с %s: %w", addr, err)
	}
	_ = tc.SetDeadline(time.Time{})
	conn := NewConn(tc, d.IOTimeout, d.MaxFrame)
	// обмен паролями в пределах CONNECT_TIMEOUT
	if d.ConnectTimeout > 0 {
		_ = tc.SetDeadline(time.Now().Add(d.ConnectTimeout))
	}
	if err := conn.AuthAsInitiator(d.OwnPassword, d.PeerPassword); err != nil {
		conn.Close()
		return nil, err
	}
	_ = tc.SetDeadline(time.Time{})
	return conn, nil
}

// Listen открывает TLS-слушатель ответчика.
func Listen(host string, port int, tlsCfg *tls.Config) (net.Listener, error) {
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	ln, err := tls.Listen("tcp", addr, tlsCfg)
	if err != nil {
		return nil, fmt.Errorf("прослушивание %s: %w", addr, err)
	}
	return ln, nil
}
