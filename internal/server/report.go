// Package server (report.go) — сбор данных и отправка отчёта по циклу (15.4 ТЗ).
package server

import (
	"time"

	"backuper/internal/alert"
	"backuper/internal/protocol"
	"backuper/internal/store"
	"backuper/internal/transfer"
)

func errSummaryCode(code uint16) string { return protocol.ErrName(code) }

const reportListLimit = 200

// sendReport формирует и отправляет письмо-отчёт по циклу, затем помечает события отправленными.
func (s *Server) sendReport(cyc store.Cycle, tres transfer.Result, serverDisk, clientDisk alert.Disk, start time.Time) {
	dur := time.Duration(cyc.FinishedAt - cyc.StartedAt)
	avg := 0.0
	if secs := dur.Seconds(); secs > 0 {
		avg = float64(cyc.DownloadedBytes) / secs
	}
	loc := s.cfg.Loc
	rep := alert.CycleReport{
		CycleID:         cyc.ID,
		Status:          cyc.Status,
		StartedAt:       time.Unix(0, cyc.StartedAt).In(loc),
		FinishedAt:      time.Unix(0, cyc.FinishedAt).In(loc),
		DownloadedFiles: cyc.DownloadedFiles,
		DownloadedBytes: cyc.DownloadedBytes,
		ChangedFiles:    cyc.ChangedFiles,
		TrashedFiles:    cyc.TrashedFiles,
		PurgedFiles:     cyc.PurgedFiles,
		SkippedFiles:    cyc.SkippedFiles,
		Passes:          cyc.PassesUsed,
		AvgSpeed:        avg,
		PeakParallel:    tres.PeakParallel,
		ServerDisk:      serverDisk,
		ClientDisk:      clientDisk,
	}
	for i, e := range tres.Errors {
		if i >= reportListLimit {
			break
		}
		rep.Errors = append(rep.Errors, alert.ErrorItem{
			Code: protocol.ErrName(e.Code), Relpath: e.Relpath, Message: e.Message,
		})
	}
	for i, sk := range tres.Skipped {
		if i >= reportListLimit {
			break
		}
		rep.Skipped = append(rep.Skipped, alert.SkippedItem{
			Relpath: sk.Relpath, Reason: sk.Reason, Attempts: sk.Attempts,
		})
	}
	evs, _ := s.st.ListEventsForCycle(cyc.ID)
	var ids []int64
	for _, e := range evs {
		ids = append(ids, e.ID)
		switch e.Type {
		case "disk", "mass_delete", "collision", "overlap", "restart", "auth":
			rep.Notes = append(rep.Notes, "["+e.Severity+"] "+e.Message)
		default:
			// события уровня цикла (подключение/LIST и пр.) — без relpath
			if e.Relpath == "" {
				rep.Notes = append(rep.Notes, "["+e.Severity+"] "+e.Message)
			}
		}
	}
	_ = s.alert.SendCycleReport(rep)
	_ = s.st.MarkEventsSent(ids)
}
