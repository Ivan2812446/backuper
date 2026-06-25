// Package client (scanner.go) — потоковый обход источника с фильтрами включения/
// исключения, пропуск symlink и нерегулярных файлов, выдача LIST батчами (7.4, 7.5, 20 ТЗ).
package client

import (
	"io/fs"
	"path"
	"path/filepath"

	"backuper/internal/protocol"
	"backuper/internal/tlsconn"
)

func matchAny(patterns []string, name, rel string) bool {
	for _, p := range patterns {
		if ok, _ := filepath.Match(p, name); ok {
			return true
		}
		if ok, _ := path.Match(p, rel); ok {
			return true
		}
	}
	return false
}

// included применяет маски: при наличии include файл должен совпасть; exclude отсекает.
func (s *Server) included(rel, name string) bool {
	if len(s.cfg.Include) > 0 && !matchAny(s.cfg.Include, name, rel) {
		return false
	}
	if len(s.cfg.Exclude) > 0 && matchAny(s.cfg.Exclude, name, rel) {
		return false
	}
	return true
}

// handleList обходит источник и отправляет список файлов батчами (5.4 LIST_BATCH).
func (s *Server) handleList(conn *tlsconn.Conn, req protocol.ListReq) error {
	root := s.cfg.BackupDir
	if req.Root != "" {
		full, err := protocol.SafeJoin(s.cfg.BackupDir, req.Root)
		if err != nil {
			return conn.SendError(protocol.ErrProtocol, "LIST root: %v", err)
		}
		root = full
	}

	batch := make([]protocol.FileEntry, 0, listBatchSize)
	flush := func(last bool) error {
		msg := protocol.ListBatch{IsLast: last, Entries: batch}
		if err := conn.WriteMsg(protocol.MsgListBatch, msg.Encode()); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}

	var sendErr error
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			s.log.Debug("scanner", "пропуск %s: %v", p, err)
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		typ := d.Type()
		if typ&fs.ModeSymlink != 0 {
			s.log.Debug("scanner", "symlink игнорируется: %s", p)
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, rerr := protocol.ToRel(s.cfg.BackupDir, p)
		if rerr != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			// каталоги не передаются; применяем exclude для отсечения поддерева
			if rel != "." && len(s.cfg.Exclude) > 0 && matchAny(s.cfg.Exclude, name, rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if !typ.IsRegular() {
			return nil
		}
		if !s.included(rel, name) {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			s.log.Debug("scanner", "stat %s: %v", p, ierr)
			return nil
		}
		batch = append(batch, protocol.FileEntry{
			Path:  protocol.CleanRel(rel),
			Size:  uint64(info.Size()),
			Mtime: info.ModTime().UnixNano(),
		})
		if len(batch) >= listBatchSize {
			if err := flush(false); err != nil {
				sendErr = err
				return err
			}
		}
		return nil
	})
	if sendErr != nil {
		return sendErr
	}
	if walkErr != nil && walkErr != filepath.SkipDir {
		s.log.Warn("scanner", "обход %s: %v", root, walkErr)
	}
	return flush(true)
}
