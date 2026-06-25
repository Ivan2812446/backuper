// Package server (firstrun.go) — первый запуск (раздел 12 ТЗ): индексирование
// перенесённого вручную содержимого STORAGE_DIR по фактическим size/mtime.
package server

import (
	"context"
	"io/fs"
	"path/filepath"
	"time"

	"backuper/internal/protocol"
)

// firstRunIndex обходит STORAGE_DIR и заполняет индекс files (только stat, без чтения).
func (s *Server) firstRunIndex(ctx context.Context) (int, error) {
	w, err := s.st.BeginFileIndex()
	if err != nil {
		return 0, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = w.Rollback()
		}
	}()

	now := time.Now().UnixNano()
	storageAbs, _ := filepath.Abs(s.cfg.StorageDir)
	trashAbs, _ := filepath.Abs(s.cfg.TrashDir)
	tempAbs, _ := filepath.Abs(s.cfg.TempDir)

	walkErr := filepath.WalkDir(s.cfg.StorageDir, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			s.log.Debug("firstrun", "пропуск %s: %v", p, err)
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		abs, _ := filepath.Abs(p)
		// не индексировать корзину/temp, если они внутри хранилища
		if d.IsDir() && (abs == trashAbs || abs == tempAbs) {
			return filepath.SkipDir
		}
		if d.Type()&fs.ModeSymlink != 0 {
			s.log.Debug("firstrun", "symlink игнорируется: %s", p)
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, rerr := protocol.ToRel(storageAbs, abs)
		if rerr != nil {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		return w.Add(protocol.CleanRel(rel), protocol.NormKey(rel), info.Size(), info.ModTime().UnixNano(), now)
	})
	if walkErr != nil {
		return 0, walkErr
	}
	if err := w.Commit(); err != nil {
		return 0, err
	}
	committed = true
	return w.Count(), nil
}
