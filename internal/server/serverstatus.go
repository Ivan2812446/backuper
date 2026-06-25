// Package server (serverstatus.go) — команда status (раздел 19.3, 22 ТЗ):
// последний цикл, состояние очереди, healthcheck.
package server

import (
	"fmt"
	"io"
	"time"
)

// PrintStatus печатает состояние сервера.
func (s *Server) PrintStatus(w io.Writer) error {
	loc := s.cfg.Loc
	files, _ := s.st.CountFiles()
	trashN, _ := s.st.CountTrash()
	pend, _ := s.st.CountTasks("pending")
	inprog, _ := s.st.CountTasks("in_progress")
	skipped, _ := s.st.CountTasks("skipped")
	init, _ := s.st.Initialized()

	fmt.Fprintln(w, "== Backuper server status ==")
	fmt.Fprintf(w, "Инициализирован: %v\n", init)
	fmt.Fprintf(w, "Индекс хранилища: %d файлов\n", files)
	fmt.Fprintf(w, "Корзина: %d записей\n", trashN)
	fmt.Fprintf(w, "Очередь: pending=%d in_progress=%d skipped=%d\n", pend, inprog, skipped)

	if v, ok, _ := s.st.GetMeta("running"); ok {
		state := "остановлена (чисто)"
		if v == "1" {
			state = "выполняется/не завершалась чисто"
		}
		fmt.Fprintf(w, "Состояние службы (флаг): %s\n", state)
	}

	c, ok, err := s.st.GetLastCycle()
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintln(w, "Циклов ещё не было.")
		return nil
	}
	started := time.Unix(0, c.StartedAt).In(loc).Format("2006-01-02 15:04:05")
	finished := "—"
	if c.FinishedAt > 0 {
		finished = time.Unix(0, c.FinishedAt).In(loc).Format("2006-01-02 15:04:05")
	}
	fmt.Fprintf(w, "\nПоследний цикл #%d: %s\n", c.ID, c.Status)
	fmt.Fprintf(w, "  Период: %s — %s\n", started, finished)
	fmt.Fprintf(w, "  Скачано: %d файлов / %d Б; изменено %d; в корзину %d; удалено по сроку %d; пропущено %d; проходов %d\n",
		c.DownloadedFiles, c.DownloadedBytes, c.ChangedFiles, c.TrashedFiles, c.PurgedFiles, c.SkippedFiles, c.PassesUsed)
	if c.ErrorSummary != "" {
		fmt.Fprintf(w, "  Ошибки: %s\n", c.ErrorSummary)
	}
	return nil
}
