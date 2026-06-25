// Package client — служба-слушатель клиента (раздел 4.3, 5 ТЗ): приём TLS+mTLS
// соединений, обмен паролями, маршрутизация команд LIST/GET/PUT/DISK/PING.
package client

import (
	"context"
	"crypto/tls"
	"net"
	"os"
	"sync"
	"time"

	"backuper/internal/logx"
	"backuper/internal/protocol"
	"backuper/internal/tlsconn"
)

const listBatchSize = 10000

// Config — параметры демона клиента.
type Config struct {
	ListenHost       string
	ListenPort       int
	BackupDir        string
	Include          []string
	Exclude          []string
	ChunkSize        int64
	BandwidthLimit   int64
	IOTimeout        time.Duration
	MaxFrame         uint64
	MaxConnections   int
	SaveFilePerms    os.FileMode
	SaveDirPerms     os.FileMode
	OwnPassword      string
	PeerPassword     string
	StatusFile       string
	RestoreTempDir   string
	MtimeToleranceNS int64
}

// Server — демон клиента.
type Server struct {
	cfg    Config
	tlsCfg *tls.Config
	log    *logx.Logger
	lim    *limiter
	ln     net.Listener
	status *statusState
}

// New создаёт демон клиента.
func New(cfg Config, tlsCfg *tls.Config, log *logx.Logger) *Server {
	return &Server{
		cfg:    cfg,
		tlsCfg: tlsCfg,
		log:    log,
		lim:    newLimiter(cfg.BandwidthLimit, cfg.ChunkSize),
		status: newStatus(cfg.StatusFile),
	}
}

// Run запускает слушатель до отмены ctx (раздел 19.3, graceful).
func (s *Server) Run(ctx context.Context) error {
	ln, err := tlsconn.Listen(s.cfg.ListenHost, s.cfg.ListenPort, s.tlsCfg)
	if err != nil {
		return err
	}
	s.ln = ln
	s.status.setListen(s.cfg.ListenHost, s.cfg.ListenPort)
	s.status.flush()
	s.log.Info("listener", "слушаю %s:%d, источник %s", s.cfg.ListenHost, s.cfg.ListenPort, s.cfg.BackupDir)

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	sem := make(chan struct{}, s.cfg.MaxConnections)
	var wg sync.WaitGroup
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				break // штатная остановка
			}
			s.log.Warn("listener", "accept: %v", err)
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			conn.Close()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			s.handleConn(ctx, conn)
		}()
	}
	wg.Wait()
	s.log.Info("listener", "слушатель остановлен")
	return nil
}

func (s *Server) handleConn(ctx context.Context, raw net.Conn) {
	defer raw.Close()
	remote := raw.RemoteAddr().String()

	if tc, ok := raw.(*tls.Conn); ok {
		_ = tc.SetDeadline(time.Now().Add(s.cfg.IOTimeout))
		if err := tc.Handshake(); err != nil {
			s.log.Warn("listener", "TLS-рукопожатие с %s отклонено: %v", remote, err)
			s.status.record(remote, "tls_failed")
			return
		}
		_ = tc.SetDeadline(time.Time{})
	}

	conn := tlsconn.NewConn(raw, s.cfg.IOTimeout, s.cfg.MaxFrame)
	if err := conn.AuthAsResponder(s.cfg.OwnPassword, s.cfg.PeerPassword); err != nil {
		s.log.Warn("listener", "аутентификация %s не пройдена: %v", remote, err)
		s.status.record(remote, "auth_failed")
		return
	}
	s.log.Info("listener", "соединение установлено: %s", remote)
	s.status.record(remote, "ok")

	for {
		if ctx.Err() != nil {
			return
		}
		mt, payload, err := conn.ReadMsg()
		if err != nil {
			s.log.Debug("listener", "соединение %s закрыто: %v", remote, err)
			return
		}
		if err := s.dispatch(ctx, conn, mt, payload); err != nil {
			s.log.Debug("listener", "команда %s от %s завершилась: %v", protocol.MsgName(mt), remote, err)
			return
		}
	}
}

// dispatch обрабатывает одну команду. Возвращает ошибку только при необходимости
// разорвать соединение.
func (s *Server) dispatch(ctx context.Context, conn *tlsconn.Conn, mt byte, payload []byte) error {
	switch mt {
	case protocol.MsgPing:
		return conn.Pong()
	case protocol.MsgListReq:
		req, err := protocol.ParseListReq(payload)
		if err != nil {
			return conn.SendError(protocol.ErrProtocol, "LIST_REQ: %v", err)
		}
		return s.handleList(conn, req)
	case protocol.MsgGetReq:
		req, err := protocol.ParseGetReq(payload)
		if err != nil {
			return conn.SendError(protocol.ErrProtocol, "GET_REQ: %v", err)
		}
		return s.handleGet(ctx, conn, req)
	case protocol.MsgPutReq:
		req, err := protocol.ParsePutReq(payload)
		if err != nil {
			return conn.SendError(protocol.ErrProtocol, "PUT_REQ: %v", err)
		}
		return s.handlePut(ctx, conn, req)
	case protocol.MsgDiskReq:
		return s.handleDisk(conn)
	default:
		return conn.SendError(protocol.ErrUnsupported, "неизвестный тип %s", protocol.MsgName(mt))
	}
}
