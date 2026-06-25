// Package trash — корзина сервера (раздел 11 ТЗ): перенос удалённых на клиенте
// файлов из хранилища в корзину с сохранением relpath, версии через скрытую
// подпапку .versions/<seq>, очистка по сроку TRASH_RETENTION_DAYS.
package trash

import (
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"time"

	"backuper/internal/logx"
	"backuper/internal/protocol"
	"backuper/internal/store"
)

// Config — параметры корзины.
type Config struct {
	StorageDir          string
	TrashDir            string
	RetentionDays       int
	MassDeleteThreshold int
}

// Trasher — менеджер корзины.
type Trasher struct {
	st  *store.Store
	log *logx.Logger
	cfg Config
}

// New создаёт менеджер корзины.
func New(st *store.Store, log *logx.Logger, cfg Config) *Trasher {
	return &Trasher{st: st, log: log, cfg: cfg}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// moveFile перемещает файл (rename; при EXDEV — копирование+удаление).
func moveFile(src, dst string) error {
	if fileExists(dst) {
		_ = os.Remove(dst)
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	// fallback: копирование между разными ФС
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		in.Close()
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		in.Close()
		out.Close()
		return err
	}
	in.Close()
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	out.Close()
	return os.Remove(src)
}

// versionPath — путь версии в корзине: TRASH_DIR/<dir>/.versions/<seq>/<base> (11.2).
func (t *Trasher) versionPath(relpath string, seq int) (string, error) {
	dir := path.Dir(relpath)
	if dir == "." {
		dir = ""
	}
	return protocol.SafeJoin(t.cfg.TrashDir, path.Join(dir, ".versions", strconv.Itoa(seq), path.Base(relpath)))
}

// trashPathFor — путь файла в корзине по версии (0 = канонический).
func (t *Trasher) trashPathFor(relpath string, version int) (string, error) {
	if version == 0 {
		return protocol.SafeJoin(t.cfg.TrashDir, relpath)
	}
	return t.versionPath(relpath, version)
}

// MoveDeleted переносит в корзину все файлы хранилища, отсутствующие у клиента.
// Возвращает число перенесённых. При превышении порога — событие mass_delete (11.5).
func (t *Trasher) MoveDeleted(cycleID int64, batchLimit int) (int, error) {
	total, err := t.st.CountToTrash()
	if err != nil {
		return 0, err
	}
	if total == 0 {
		return 0, nil
	}
	if int(total) > t.cfg.MassDeleteThreshold {
		_, _ = t.st.InsertEvent(cycleID, "WARN", "mass_delete", "",
			"массовое удаление: "+strconv.FormatInt(total, 10)+" файлов в корзину (порог "+strconv.Itoa(t.cfg.MassDeleteThreshold)+")",
			time.Now().UnixNano())
		t.log.Warn("trash", "массовое удаление: %d файлов > порога %d", total, t.cfg.MassDeleteThreshold)
	}

	moved := 0
	for {
		batch, err := t.st.SelectTrashBatch(batchLimit)
		if err != nil {
			return moved, err
		}
		if len(batch) == 0 {
			break
		}
		progress := 0
		for _, c := range batch {
			if err := t.moveOne(cycleID, c.Relpath, c.Size); err != nil {
				t.log.Error("trash", "перенос в корзину %s: %v", c.Relpath, err)
				// чтобы не зациклиться, удаляем из индекса (файл остаётся на месте, событие в логе)
				_ = t.st.DeleteFileByNorm(protocol.NormKey(c.Relpath))
				_, _ = t.st.InsertEvent(cycleID, "ERROR", "io", c.Relpath, "ошибка переноса в корзину: "+err.Error(), time.Now().UnixNano())
				continue
			}
			moved++
			progress++
		}
		if progress == 0 {
			break // нет прогресса — выходим, чтобы не зациклиться
		}
	}
	return moved, nil
}

func (t *Trasher) moveOne(cycleID int64, relpath string, size int64) error {
	now := time.Now().UnixNano()
	src, err := protocol.SafeJoin(t.cfg.StorageDir, relpath)
	if err != nil {
		return err
	}
	canonical, err := protocol.SafeJoin(t.cfg.TrashDir, relpath)
	if err != nil {
		return err
	}

	if !fileExists(src) {
		// файла в хранилище уже нет — просто чистим индекс
		_ = t.st.DeleteFileByNorm(protocol.NormKey(relpath))
		t.log.Warn("trash", "файл хранилища отсутствует, удалён из индекса: %s", relpath)
		return nil
	}

	hasCanon, err := t.st.HasCanonicalTrash(relpath)
	if err != nil {
		return err
	}
	if hasCanon || fileExists(canonical) {
		// прежняя версия → в .versions/<seq>
		maxV, err := t.st.MaxTrashVersion(relpath)
		if err != nil {
			return err
		}
		seq := maxV + 1
		vp, err := t.versionPath(relpath, seq)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(vp), 0o755); err != nil {
			return err
		}
		if fileExists(canonical) {
			if err := moveFile(canonical, vp); err != nil {
				return err
			}
		}
		if err := t.st.BumpCanonicalToVersion(relpath, seq); err != nil {
			return err
		}
	}

	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		return err
	}
	if err := moveFile(src, canonical); err != nil {
		return err
	}
	if err := t.st.DeleteFileByNorm(protocol.NormKey(relpath)); err != nil {
		return err
	}
	if _, err := t.st.InsertTrash(protocol.CleanRel(relpath), 0, size, now); err != nil {
		return err
	}
	t.log.Info("trash", "в корзину: %s (%d Б)", relpath, size)
	t.log.Audit("trash", relpath, size, "ok", 1, cycleID)
	return nil
}

// Cleanup безвозвратно удаляет записи корзины со сроком < now-retention (11.3).
// Возвращает число удалённых.
func (t *Trasher) Cleanup(cycleID, nowNS int64) (int, error) {
	cutoff := nowNS - int64(t.cfg.RetentionDays)*int64(24*time.Hour)
	purged := 0
	for {
		due, err := t.st.SelectTrashDue(cutoff, 1000)
		if err != nil {
			return purged, err
		}
		if len(due) == 0 {
			break
		}
		for _, it := range due {
			p, err := t.trashPathFor(it.Relpath, it.Version)
			if err == nil {
				if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
					t.log.Error("trash", "очистка корзины %s: %v", p, err)
				}
			}
			if err := t.st.DeleteTrash(it.ID); err != nil {
				return purged, err
			}
			purged++
			t.log.Audit("trash_purge", it.Relpath, it.Size, "ok", 1, cycleID)
		}
	}
	if purged > 0 {
		t.log.Info("trash", "очистка корзины: удалено %d файлов (срок %d дней)", purged, t.cfg.RetentionDays)
	}
	return purged, nil
}
