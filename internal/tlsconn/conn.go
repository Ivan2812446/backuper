// Package tlsconn (conn.go) — кадрированное соединение поверх net.Conn с дедлайнами
// ввода-вывода (5.7) и обменом паролями (5.3, 6.2 ТЗ).
package tlsconn

import (
	"fmt"
	"net"
	"time"

	"backuper/internal/protocol"
)

// Conn — соединение с покадровым чтением/записью и дедлайном на каждый кадр.
type Conn struct {
	raw       net.Conn
	ioTimeout time.Duration
	maxFrame  uint64
}

// NewConn оборачивает установленное соединение.
func NewConn(raw net.Conn, ioTimeout time.Duration, maxFrame uint64) *Conn {
	return &Conn{raw: raw, ioTimeout: ioTimeout, maxFrame: maxFrame}
}

// Raw возвращает нижележащее соединение.
func (c *Conn) Raw() net.Conn { return c.raw }

// RemoteAddr — адрес собеседника.
func (c *Conn) RemoteAddr() net.Addr { return c.raw.RemoteAddr() }

// Close закрывает соединение.
func (c *Conn) Close() error { return c.raw.Close() }

// WriteMsg пишет кадр, обновляя дедлайн записи (5.7).
func (c *Conn) WriteMsg(msgType byte, payload []byte) error {
	if c.ioTimeout > 0 {
		_ = c.raw.SetWriteDeadline(time.Now().Add(c.ioTimeout))
	}
	return protocol.WriteFrame(c.raw, msgType, payload)
}

// ReadMsg читает кадр, обновляя дедлайн чтения (сбрасывается при прогрессе, 5.7).
func (c *Conn) ReadMsg() (byte, []byte, error) {
	if c.ioTimeout > 0 {
		_ = c.raw.SetReadDeadline(time.Now().Add(c.ioTimeout))
	}
	return protocol.ReadFrame(c.raw, c.maxFrame)
}

// ReadExpect читает кадр и проверяет тип. Кадр ERROR превращается в *protocol.ProtoError.
func (c *Conn) ReadExpect(expected byte) ([]byte, error) {
	mt, payload, err := c.ReadMsg()
	if err != nil {
		return nil, err
	}
	if mt == protocol.MsgError {
		em, _ := protocol.ParseErrorMsg(payload)
		return nil, &protocol.ProtoError{Code: em.Code, Msg: em.Message}
	}
	if mt != expected {
		return nil, protocol.Errorf(protocol.ErrProtocol,
			"ожидался %s, получен %s", protocol.MsgName(expected), protocol.MsgName(mt))
	}
	return payload, nil
}

// SendError отправляет кадр ERROR.
func (c *Conn) SendError(code uint16, format string, a ...any) error {
	em := protocol.ErrorMsg{Code: code, Message: fmt.Sprintf(format, a...)}
	return c.WriteMsg(protocol.MsgError, em.Encode())
}

// Ping/Pong — служебные кадры контроля живости (5.7).
func (c *Conn) Ping() error { return c.WriteMsg(protocol.MsgPing, nil) }
func (c *Conn) Pong() error { return c.WriteMsg(protocol.MsgPong, nil) }

// AuthAsInitiator выполняет обмен паролями со стороны инициатора (backuper-server, 5.3).
func (c *Conn) AuthAsInitiator(ownPassword, peerPassword string) error {
	req := protocol.AuthReq{ProtoVersion: protocol.ProtoVersion, Password: ownPassword}
	if err := c.WriteMsg(protocol.MsgAuthReq, req.Encode()); err != nil {
		return err
	}
	payload, err := c.ReadExpect(protocol.MsgAuthResp)
	if err != nil {
		return err
	}
	resp, err := protocol.ParseAuthResp(payload)
	if err != nil {
		return err
	}
	if resp.Status != 0 || resp.Password != peerPassword {
		_ = c.SendError(protocol.ErrAuthFailed, "несовпадение пароля собеседника")
		return protocol.Errorf(protocol.ErrAuthFailed, "несовпадение пароля клиента")
	}
	return nil
}

// AuthAsResponder выполняет обмен паролями со стороны слушателя (backuper-client, 5.3).
func (c *Conn) AuthAsResponder(ownPassword, peerPassword string) error {
	payload, err := c.ReadExpect(protocol.MsgAuthReq)
	if err != nil {
		return err
	}
	req, err := protocol.ParseAuthReq(payload)
	if err != nil {
		return err
	}
	if req.ProtoVersion != protocol.ProtoVersion {
		_ = c.SendError(protocol.ErrProtocol, "неподдерживаемая версия протокола %d", req.ProtoVersion)
		return protocol.Errorf(protocol.ErrProtocol, "версия протокола %d", req.ProtoVersion)
	}
	if req.Password != peerPassword {
		resp := protocol.AuthResp{Password: ownPassword, Status: 1}
		_ = c.WriteMsg(protocol.MsgAuthResp, resp.Encode())
		_ = c.SendError(protocol.ErrAuthFailed, "несовпадение пароля сервера")
		return protocol.Errorf(protocol.ErrAuthFailed, "несовпадение пароля инициатора")
	}
	resp := protocol.AuthResp{Password: ownPassword, Status: 0}
	return c.WriteMsg(protocol.MsgAuthResp, resp.Encode())
}
