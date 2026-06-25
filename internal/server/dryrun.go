// Package server (dryrun.go) — цикл сверки без передачи (раздел 23.1 ТЗ):
// проверка соединения/mTLS/паролей, вывод списков «к скачиванию» и «в корзину».
package server

import (
	"context"
	"fmt"
	"os"
	"strings"

	"backuper/internal/alert"
	"backuper/internal/diskinfo"
)

// DryRun выполняет диагностический проход без реальной передачи.
func (s *Server) DryRun(ctx context.Context, testMail bool) error {
	out := os.Stdout
	fmt.Fprintln(out, "== Backuper dry-run ==")

	ctrl, err := s.connectControl(ctx)
	if err != nil {
		return fmt.Errorf("подключение к клиенту: %w", err)
	}
	defer ctrl.close()
	fmt.Fprintf(out, "Подключение, mTLS и пароли: OK (%s:%d)\n", s.cfg.ClientHost, s.cfg.ClientPort)

	if cd, derr := ctrl.disk(); derr == nil {
		fmt.Fprintf(out, "Диск клиента: %.1f%% свободно (%s / %s)\n",
			cd.FreePercent(), alert.HumanBytes(cd.Free), alert.HumanBytes(cd.Total))
	}
	if u, e := diskinfo.Get(s.cfg.StorageDir); e == nil {
		sd := alert.Disk{Total: u.Total, Free: u.Free}
		fmt.Fprintf(out, "Диск сервера: %.1f%% свободно (%s / %s)\n",
			sd.FreePercent(), alert.HumanBytes(sd.Free), alert.HumanBytes(sd.Total))
	}

	if init, _ := s.st.Initialized(); !init {
		fmt.Fprintln(out, "ВНИМАНИЕ: первый запуск ещё не выполнен — индекс пуст; реальный цикл сначала проиндексирует STORAGE_DIR.")
	}

	n, lerr := ctrl.loadList(s.st, "")
	if lerr != nil {
		return fmt.Errorf("получение списка клиента: %w", lerr)
	}
	fmt.Fprintf(out, "Список клиента: %d файлов\n", n)

	if col, _ := s.st.CountClientCollisions(); col > 0 {
		ex, _ := s.st.ListClientCollisionExamples(10)
		fmt.Fprintf(out, "Коллизии путей по регистру: %d (примеры: %s)\n", col, strings.Join(ex, ", "))
	}

	tol := s.cfg.MtimeTolerance.Nanoseconds()
	newC, changedC, _ := s.st.CountNewChanged(tol)
	trashC, _ := s.st.CountToTrash()
	fmt.Fprintf(out, "К скачиванию: %d (новых %d, изменённых %d); в корзину: %d\n",
		newC+changedC, newC, changedC, trashC)

	dl, _ := s.st.ListDownloadPreview(tol, 50)
	fmt.Fprintln(out, "-- к скачиванию (до 50) --")
	for _, d := range dl {
		tag := "изм"
		if d.IsNew {
			tag = "нов"
		}
		fmt.Fprintf(out, "  [%s] %s (%d Б)\n", tag, d.Relpath, d.Size)
	}

	tr, _ := s.st.ListTrashPreview(50)
	fmt.Fprintln(out, "-- в корзину (до 50) --")
	for _, t := range tr {
		fmt.Fprintf(out, "  %s (%d Б)\n", t.Relpath, t.Size)
	}

	if testMail {
		fmt.Fprintln(out, "Отправка тестового письма (SMTP)…")
		if err := s.alert.TestConnection(); err != nil {
			fmt.Fprintf(out, "SMTP: ОШИБКА: %v\n", err)
		} else {
			fmt.Fprintln(out, "SMTP: OK")
		}
	}
	fmt.Fprintln(out, "Передача не выполнялась (dry-run).")
	return nil
}
