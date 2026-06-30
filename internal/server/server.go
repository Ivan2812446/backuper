// Package server — оркестрация сервера (раздел 4.3, 7 ТЗ): планировщик циклов,
// диффер, передача, корзина, алерты, первый запуск, восстановление, блокировка.
package server

import (
	"os"

	"backuper/internal/alert"
	"backuper/internal/config"
	"backuper/internal/logx"
	"backuper/internal/store"
	"backuper/internal/tlsconn"
	"backuper/internal/transfer"
	"backuper/internal/trash"
)

// Server — сервер резервного копирования.
type Server struct {
	cfg     *config.ServerConfig
	log     *logx.Logger
	st      *store.Store
	dialer  *tlsconn.Dialer
	alert   *alert.Manager
	trasher *trash.Trasher
}

// New собирает сервер: открывает базу, готовит TLS-dialer, алерты и корзину.
func New(cfg *config.ServerConfig, log *logx.Logger) (*Server, error) {
	st, err := store.Open(cfg.StateDB)
	if err != nil {
		return nil, err
	}
	tlsCfg, err := tlsconn.DialerTLS(cfg.TLSCertFile, cfg.TLSKeyFile, cfg.TLSCAFile, cfg.TLSMinVersion, cfg.ClientHost)
	if err != nil {
		st.Close()
		return nil, err
	}
	dialer := &tlsconn.Dialer{
		Host:           cfg.ClientHost,
		Port:           cfg.ClientPort,
		TLSConfig:      tlsCfg,
		ConnectTimeout: cfg.ConnectTimeout,
		IOTimeout:      cfg.IOTimeout,
		MaxFrame:       uint64(cfg.MaxFrameBytes),
		OwnPassword:    cfg.OwnPassword,
		PeerPassword:   cfg.PeerPassword,
	}
	al := alert.New(log, alert.SMTPConfig{
		Host: cfg.SMTPHost, Port: cfg.SMTPPort, User: cfg.SMTPUser,
		Password: cfg.SMTPPassword, From: cfg.SMTPFrom, To: cfg.SMTPTo, Security: cfg.SMTPSecurity,
	}, cfg.Loc, cfg.AlertAggregationWindow)
	tr := trash.New(st, log, trash.Config{
		StorageDir: cfg.StorageDir, TrashDir: cfg.TrashDir,
		RetentionDays: cfg.TrashRetentionDays, MassDeleteThreshold: cfg.AlertMassDeleteThreshold,
	})
	return &Server{cfg: cfg, log: log, st: st, dialer: dialer, alert: al, trasher: tr}, nil
}

// Store даёт доступ к состоянию (для команды status).
func (s *Server) Store() *store.Store { return s.st }

// Alert даёт доступ к менеджеру алертов (для проверки SMTP в dry-run).
func (s *Server) Alert() *alert.Manager { return s.alert }

// Close закрывает ресурсы.
func (s *Server) Close() error { return s.st.Close() }

func (s *Server) newDataConn() (*tlsconn.Conn, error) { return s.dialer.Dial() }

func (s *Server) newTransferManager() *transfer.Manager {
	return transfer.New(transfer.Deps{
		Store:   s.st,
		Log:     s.log,
		NewConn: s.newDataConn,
		Cfg: transfer.Config{
			StorageDir:       s.cfg.StorageDir,
			TempDir:          s.cfg.TempDir,
			Parallel:         s.cfg.ParallelTransfers,
			ChunkSize:        s.cfg.ChunkSize,
			BandwidthLimit:   s.cfg.BandwidthLimit,
			RetryCount:       s.cfg.RetryCount,
			RetryDelay:       s.cfg.RetryDelay,
			SaveFilePerms:    os.FileMode(s.cfg.SaveFilePerms),
			SaveDirPerms:     os.FileMode(s.cfg.SaveDirPerms),
			MtimeToleranceNS: s.cfg.MtimeTolerance.Nanoseconds(),
			MaxFrame:         uint64(s.cfg.MaxFrameBytes),
			DeltaMinSize:     s.cfg.DeltaMinSize,
			DeltaBlockSize:   s.cfg.DeltaBlockSize,
		},
	})
}
