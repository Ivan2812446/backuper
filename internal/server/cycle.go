// Package server (cycle.go) — цикл сверки (раздел 7.1 ТЗ): подключение, проверка
// дисков, проходы LIST→дифф→скачивание→корзина, очистка корзины, отчёт.
package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"backuper/internal/alert"
	"backuper/internal/diskinfo"
	"backuper/internal/store"
	"backuper/internal/transfer"
)

// connectControl подключает контрольное соединение с повторами (14).
func (s *Server) connectControl(ctx context.Context) (*control, error) {
	var lastErr error
	for attempt := 1; attempt <= s.cfg.RetryCount; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		conn, err := s.dialer.Dial()
		if err == nil {
			return &control{conn: conn, log: s.log}, nil
		}
		lastErr = err
		s.log.Warn("control", "подключение к клиенту не удалось (попытка %d/%d): %v", attempt, s.cfg.RetryCount, err)
		if attempt < s.cfg.RetryCount {
			timer := time.NewTimer(s.cfg.RetryDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return nil, lastErr
}

// checkDisks проверяет пороги дисков сервера и клиента (15.2, 21).
func (s *Server) checkDisks(cycleID int64, clientDisk alert.Disk) (serverDisk alert.Disk, diskFull bool) {
	now := time.Now().UnixNano()
	if u, err := diskinfo.Get(s.cfg.StorageDir); err == nil {
		serverDisk = alert.Disk{Name: "сервер:" + s.cfg.StorageDir, Total: u.Total, Free: u.Free}
	} else {
		s.log.Error("disk", "diskinfo сервера: %v", err)
	}
	thr := float64(s.cfg.DiskAlertThreshold)

	if serverDisk.Total > 0 && serverDisk.UsedPercent() >= thr {
		msg := fmt.Sprintf("Диск сервера: занято %.1f%% (свободно %s из %s)",
			serverDisk.UsedPercent(), alert.HumanBytes(serverDisk.Free), alert.HumanBytes(serverDisk.Total))
		_, _ = s.st.InsertEvent(cycleID, "WARN", "disk", "", msg, now)
		_ = s.alert.SendImmediate("disk_server", "Диск сервера выше порога", []string{msg})
		s.log.Warn("disk", "%s", msg)
	}
	if clientDisk.Total > 0 && clientDisk.UsedPercent() >= thr {
		msg := fmt.Sprintf("Диск клиента: занято %.1f%% (свободно %s из %s)",
			clientDisk.UsedPercent(), alert.HumanBytes(clientDisk.Free), alert.HumanBytes(clientDisk.Total))
		_, _ = s.st.InsertEvent(cycleID, "WARN", "disk", "", msg, now)
		_ = s.alert.SendImmediate("disk_client", "Диск клиента выше порога", []string{msg})
		s.log.Warn("disk", "%s", msg)
	}
	if serverDisk.Total > 0 && (serverDisk.FreePercent() < 0.5 || serverDisk.Free < 64<<20) {
		diskFull = true
		msg := fmt.Sprintf("Недостаточно места на диске сервера (свободно %s) — цикл приостановлен",
			alert.HumanBytes(serverDisk.Free))
		_, _ = s.st.InsertEvent(cycleID, "ERROR", "disk", "", msg, now)
		_ = s.alert.SendImmediate("disk_full", "Недостаточно места на сервере (DISK_FULL)", []string{msg})
		s.log.Error("disk", "%s", msg)
	}
	return
}

// handleCollisions обнаруживает и устраняет коллизии путей по регистру (5.6).
func (s *Server) handleCollisions(cycleID int64) {
	n, err := s.st.CountClientCollisions()
	if err != nil || n == 0 {
		return
	}
	ex, _ := s.st.ListClientCollisionExamples(20)
	removed, _ := s.st.DedupClientFiles()
	msg := fmt.Sprintf("коллизии путей по регистру: пропущено %d дубликат(ов); примеры: %s",
		removed, strings.Join(ex, ", "))
	_, _ = s.st.InsertEvent(cycleID, "WARN", "collision", "", msg, time.Now().UnixNano())
	s.log.Warn("differ", "%s", msg)
}

// RunCycle выполняет один полный цикл сверки.
func (s *Server) RunCycle(ctx context.Context) {
	start := time.Now()
	cycleID, err := s.st.CreateCycle(start.UnixNano())
	if err != nil {
		s.log.Error("cycle", "создание записи цикла: %v", err)
		return
	}
	s.log.Info("cycle", "цикл #%d начат", cycleID)

	cyc := store.Cycle{ID: cycleID, StartedAt: start.UnixNano()}
	status := "OK"
	var serverDisk, clientDisk alert.Disk
	var tres transfer.Result
	var changedFirst, trashedTotal, purged int64
	passes := 0

	finish := func() {
		cyc.FinishedAt = time.Now().UnixNano()
		cyc.Status = status
		cyc.PassesUsed = passes
		cyc.DownloadedFiles = tres.DownloadedFiles
		cyc.DownloadedBytes = tres.DownloadedBytes
		cyc.ChangedFiles = changedFirst
		cyc.TrashedFiles = trashedTotal
		cyc.PurgedFiles = purged
		cyc.SkippedFiles = tres.SkippedFiles
		cyc.ErrorSummary = summarizeErrors(tres.Errors)
		_ = s.st.FinalizeCycle(cyc)
		s.sendReport(cyc, tres, serverDisk, clientDisk, start)
		s.log.Info("cycle", "цикл #%d завершён: %s", cycleID, status)
	}

	// подключение контрольного соединения (retry)
	ctrl, err := s.connectControl(ctx)
	if err != nil {
		status = "FAILED"
		msg := "не удалось подключиться к клиенту: " + err.Error()
		_, _ = s.st.InsertEvent(cycleID, "ERROR", "io", "", msg, time.Now().UnixNano())
		s.log.Error("cycle", "%s", msg)
		finish()
		return
	}
	defer ctrl.close()

	// свободное место клиента + пороги
	if cd, derr := ctrl.disk(); derr == nil {
		clientDisk = cd
	} else {
		s.log.Warn("disk", "DISK клиента: %v", derr)
	}
	var diskFull bool
	serverDisk, diskFull = s.checkDisks(cycleID, clientDisk)

	// PING на контрольном соединении во время цикла (5.7)
	stopPing := make(chan struct{})
	go s.pingLoop(ctrl, stopPing)
	defer close(stopPing)

	// первый запуск: индексирование ручного переноса (12)
	if init, _ := s.st.Initialized(); !init {
		s.log.Info("firstrun", "первый запуск: индексирование %s", s.cfg.StorageDir)
		n, ierr := s.firstRunIndex(ctx)
		if ierr != nil {
			s.log.Error("firstrun", "индексирование хранилища: %v", ierr)
		} else {
			s.log.Info("firstrun", "проиндексировано %d файлов хранилища", n)
		}
		_ = s.st.SetInitialized()
	}

	if diskFull {
		status = "PARTIAL"
		passes = 0
		finish()
		return
	}

	tm := s.newTransferManager()
	tolNS := s.cfg.MtimeTolerance.Nanoseconds()

	for pass := 1; pass <= s.cfg.SyncMaxPasses; pass++ {
		if ctx.Err() != nil {
			status = "PARTIAL"
			break
		}
		passes = pass
		listCount, lerr := ctrl.loadList(s.st, "")
		if lerr != nil {
			status = "FAILED"
			msg := "ошибка получения списка клиента: " + lerr.Error()
			_, _ = s.st.InsertEvent(cycleID, "ERROR", "io", "", msg, time.Now().UnixNano())
			s.log.Error("cycle", "%s", msg)
			break
		}
		s.handleCollisions(cycleID)
		newC, changedC, _ := s.st.CountNewChanged(tolNS)
		if pass == 1 {
			changedFirst = changedC
		}
		enq, eerr := s.st.EnqueueDiffDownloads(cycleID, tolNS, time.Now().UnixNano())
		if eerr != nil {
			status = "FAILED"
			s.log.Error("cycle", "постановка в очередь: %v", eerr)
			break
		}
		moved, terr := s.trasher.MoveDeleted(cycleID, 1000)
		if terr != nil {
			s.log.Error("cycle", "перенос в корзину: %v", terr)
		}
		trashedTotal += int64(moved)
		s.log.Info("cycle", "проход %d: список=%d новых=%d изменён=%d в очередь=%d в корзину=%d",
			pass, listCount, newC, changedC, enq, moved)

		r, rerr := tm.RunDownloadQueue(ctx, cycleID)
		tres = r
		if rerr != nil && ctx.Err() != nil {
			status = "PARTIAL"
			break
		}
		if enq == 0 && moved == 0 {
			break // сошлось раньше
		}
	}

	// очистка корзины по сроку (11.3)
	purged = s.cleanupTrashIfDue(cycleID)

	// остаток расхождений → PARTIAL
	pend, _ := s.st.CountTasks("pending")
	inprog, _ := s.st.CountTasks("in_progress")
	if status == "OK" && (pend > 0 || inprog > 0 || tres.SkippedFiles > 0) {
		status = "PARTIAL"
	}

	// очистка очереди
	_ = s.st.DeleteDoneTasks()
	_ = s.st.DeleteTasksByStatus("skipped")
	_ = s.st.DeleteTasksByStatus("failed")

	finish()
}

func (s *Server) pingLoop(ctrl *control, stop <-chan struct{}) {
	t := time.NewTicker(s.cfg.HealthcheckInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if err := ctrl.ping(); err != nil {
				s.log.Warn("control", "PING контрольного соединения: %v", err)
			}
		}
	}
}

// cleanupTrashIfDue запускает очистку корзины по интервалу TRASH_CLEANUP_INTERVAL.
func (s *Server) cleanupTrashIfDue(cycleID int64) int64 {
	now := time.Now()
	last, ok, _ := s.st.GetMeta("last_cleanup_at")
	if ok {
		if ts, perr := time.Parse(time.RFC3339Nano, last); perr == nil {
			if now.Sub(ts) < s.cfg.TrashCleanupInterval {
				return 0
			}
		}
	}
	purged, err := s.trasher.Cleanup(cycleID, now.UnixNano())
	if err != nil {
		s.log.Error("trash", "очистка корзины: %v", err)
	}
	_ = s.st.SetMeta("last_cleanup_at", now.Format(time.RFC3339Nano))
	return int64(purged)
}

func summarizeErrors(errs []transfer.Err) string {
	if len(errs) == 0 {
		return ""
	}
	var b strings.Builder
	limit := len(errs)
	if limit > 50 {
		limit = 50
	}
	for i := 0; i < limit; i++ {
		fmt.Fprintf(&b, "%s %s: %s; ", errSummaryCode(errs[i].Code), errs[i].Relpath, errs[i].Message)
	}
	if len(errs) > limit {
		fmt.Fprintf(&b, "… и ещё %d", len(errs)-limit)
	}
	return b.String()
}
