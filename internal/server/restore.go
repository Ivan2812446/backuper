// Package server (restore.go) — восстановление сервер→клиент (раздел 13 ТЗ):
// PUT с докачкой, проверка размера, retry, защита от затирания более новых файлов.
package server

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"backuper/internal/protocol"
	"backuper/internal/tlsconn"
)

// RestoreOptions — параметры команды restore (22).
type RestoreOptions struct {
	Path  string // relpath файла или поддерева
	All   bool   // весь набор
	Force bool   // перезаписывать более новый файл на клиенте
}

type restoreJob struct {
	rel   string
	size  int64
	mtime int64
}

// Restore восстанавливает файлы на клиент.
func (s *Server) Restore(ctx context.Context, opts RestoreOptions) error {
	if !opts.All && opts.Path == "" {
		return fmt.Errorf("укажите --path <relpath|dir> или --all")
	}
	ctrl, err := s.connectControl(ctx)
	if err != nil {
		return fmt.Errorf("подключение к клиенту: %w", err)
	}
	defer ctrl.close()

	// список клиента для проверки «на клиенте новее» (13)
	if _, lerr := ctrl.loadList(s.st, ""); lerr != nil {
		s.log.Warn("restore", "не удалось получить список клиента для проверки новизны: %v", lerr)
	}
	tol := s.cfg.MtimeTolerance.Nanoseconds()

	prefix := ""
	if !opts.All {
		prefix = protocol.CleanRel(opts.Path)
	}

	jobs := make(chan restoreJob)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var restored, failed int

	workers := s.cfg.ParallelTransfers
	if workers < 1 {
		workers = 1
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.restoreWorker(ctx, jobs, &mu, &restored, &failed)
		}()
	}

	skipped := 0
	queued := 0
	after := ""
	for {
		page, perr := s.st.ListFilesPage(prefix, after, 1000)
		if perr != nil {
			s.log.Error("restore", "обход индекса: %v", perr)
			break
		}
		if len(page) == 0 {
			break
		}
		stop := false
		for _, fr := range page {
			after = fr.Relpath
			if !opts.Force {
				if cmt, ok, _ := s.st.ClientFileMtime(protocol.NormKey(fr.Relpath)); ok && cmt > fr.Mtime+tol {
					s.log.Info("restore", "пропуск (на клиенте новее, без --force): %s", fr.Relpath)
					skipped++
					continue
				}
			}
			queued++
			select {
			case <-ctx.Done():
				stop = true
			case jobs <- restoreJob{rel: fr.Relpath, size: fr.Size, mtime: fr.Mtime}:
			}
			if stop {
				break
			}
		}
		if stop {
			break
		}
	}
	close(jobs)
	wg.Wait()

	s.log.Info("restore", "готово: восстановлено=%d пропущено=%d ошибок=%d", restored, skipped, failed)
	fmt.Printf("Восстановление: восстановлено %d, пропущено %d, ошибок %d\n", restored, skipped, failed)
	if queued == 0 && skipped == 0 {
		fmt.Println("Нет файлов для восстановления по заданному пути.")
	}
	return nil
}

func (s *Server) restoreWorker(ctx context.Context, jobs <-chan restoreJob, mu *sync.Mutex, restored, failed *int) {
	var conn *tlsconn.Conn
	defer func() {
		if conn != nil {
			conn.Close()
		}
	}()
	for j := range jobs {
		if ctx.Err() != nil {
			return
		}
		ok := false
		for attempt := 1; attempt <= s.cfg.RetryCount; attempt++ {
			if conn == nil {
				c, derr := s.dialer.Dial()
				if derr != nil {
					s.log.Warn("restore", "соединение (попытка %d/%d): %v", attempt, s.cfg.RetryCount, derr)
					if !sleepCtx(ctx, s.cfg.RetryDelay) {
						return
					}
					continue
				}
				conn = c
			}
			if err := s.sendRestoreFile(ctx, conn, j); err == nil {
				ok = true
				break
			} else {
				s.log.Warn("restore", "восстановление %s (попытка %d/%d): %v", j.rel, attempt, s.cfg.RetryCount, err)
				conn.Close()
				conn = nil
				if attempt < s.cfg.RetryCount && !sleepCtx(ctx, s.cfg.RetryDelay) {
					return
				}
			}
		}
		mu.Lock()
		if ok {
			*restored++
		} else {
			*failed++
		}
		mu.Unlock()
		if !ok {
			s.log.Error("restore", "файл не восстановлен: %s", j.rel)
			_, _ = s.st.InsertEvent(0, "ERROR", "io", j.rel, "restore не удался", time.Now().UnixNano())
			s.log.Audit("restore", j.rel, j.size, "error", s.cfg.RetryCount, 0)
		}
	}
}

func (s *Server) sendRestoreFile(ctx context.Context, conn *tlsconn.Conn, j restoreJob) error {
	storagePath, err := protocol.SafeJoin(s.cfg.StorageDir, j.rel)
	if err != nil {
		return err
	}
	f, err := os.Open(storagePath)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	total := uint64(st.Size())
	mtime := st.ModTime().UnixNano()

	if err := conn.WriteMsg(protocol.MsgPutReq,
		protocol.PutReq{Path: j.rel, TotalSize: total, Offset: 0, Mtime: mtime}.Encode()); err != nil {
		return err
	}
	payload, err := conn.ReadExpect(protocol.MsgPutResp)
	if err != nil {
		return err
	}
	pr, _ := protocol.ParsePutResp(payload)
	resume := pr.ResumeOffset
	if resume > total {
		resume = 0
	}
	if resume > 0 {
		if _, err := f.Seek(int64(resume), io.SeekStart); err != nil {
			return err
		}
	}
	buf := make([]byte, s.cfg.ChunkSize)
	remaining := int64(total) - int64(resume)
	for remaining > 0 {
		toRead := int64(len(buf))
		if toRead > remaining {
			toRead = remaining
		}
		n, rerr := f.Read(buf[:toRead])
		if n > 0 {
			if err := conn.WriteMsg(protocol.MsgFileData, buf[:n]); err != nil {
				return err
			}
			remaining -= int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	if err := conn.WriteMsg(protocol.MsgFileEnd, nil); err != nil {
		return err
	}
	final, err := conn.ReadExpect(protocol.MsgPutResp)
	if err != nil {
		return err
	}
	fr, _ := protocol.ParsePutResp(final)
	if fr.Status != 0 {
		return fmt.Errorf("клиент отверг файл (status=%d)", fr.Status)
	}
	s.log.Info("restore", "восстановлен %s (%d Б)", j.rel, total)
	s.log.Audit("restore", j.rel, int64(total), "ok", 1, 0)
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
