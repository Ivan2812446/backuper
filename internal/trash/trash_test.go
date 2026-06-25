package trash

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"backuper/internal/logx"
	"backuper/internal/protocol"
	"backuper/internal/store"
)

func testLogger(t *testing.T) *logx.Logger {
	t.Helper()
	l, err := logx.New(logx.Options{Actor: "test", Dir: t.TempDir(), Console: false, Level: logx.LevelError})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

type env struct {
	st       *store.Store
	tr       *Trasher
	storage  string
	trashDir string
}

func setup(t *testing.T, massThreshold int) env {
	t.Helper()
	base := t.TempDir()
	storage := filepath.Join(base, "storage")
	trashDir := filepath.Join(base, "trash")
	os.MkdirAll(storage, 0o755)
	os.MkdirAll(trashDir, 0o755)
	st, err := store.Open(filepath.Join(base, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	tr := New(st, testLogger(t), Config{
		StorageDir: storage, TrashDir: trashDir,
		RetentionDays: 10, MassDeleteThreshold: massThreshold,
	})
	return env{st, tr, storage, trashDir}
}

// makeStorageFile создаёт файл в хранилище и индексирует его (как будто скачан).
func (e env) makeStorageFile(t *testing.T, rel, content string) {
	t.Helper()
	full := filepath.Join(e.storage, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(full)
	e.st.UpsertFile(protocol.CleanRel(rel), protocol.NormKey(rel), int64(len(content)), st.ModTime().UnixNano(), time.Now().UnixNano())
}

func TestMoveDeletedKeepsPath(t *testing.T) {
	e := setup(t, 100)
	e.makeStorageFile(t, "docs/a.txt", "hello")
	e.st.ResetClientFiles() // клиент пуст -> файл удалён у клиента

	moved, err := e.tr.MoveDeleted(1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if moved != 1 {
		t.Fatalf("moved=%d, want 1", moved)
	}
	if _, err := os.Stat(filepath.Join(e.trashDir, "docs", "a.txt")); err != nil {
		t.Fatalf("файл не в корзине с путём: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.storage, "docs", "a.txt")); !os.IsNotExist(err) {
		t.Fatal("файл должен исчезнуть из хранилища")
	}
	if n, _ := e.st.CountFiles(); n != 0 {
		t.Fatalf("индекс должен опустеть: %d", n)
	}
	if n, _ := e.st.CountTrash(); n != 1 {
		t.Fatalf("CountTrash=%d", n)
	}
}

func TestVersions(t *testing.T) {
	e := setup(t, 100)
	// первая версия -> канонический путь
	e.makeStorageFile(t, "report.txt", "v1")
	e.st.ResetClientFiles()
	if _, err := e.tr.MoveDeleted(1, 100); err != nil {
		t.Fatal(err)
	}
	canonical := filepath.Join(e.trashDir, "report.txt")
	if b, _ := os.ReadFile(canonical); string(b) != "v1" {
		t.Fatalf("каноническая версия неверна: %q", b)
	}

	// вторая версия -> прежняя уходит в .versions/1, новая занимает канонический путь
	e.makeStorageFile(t, "report.txt", "v2")
	e.st.ResetClientFiles()
	if _, err := e.tr.MoveDeleted(2, 100); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(canonical); string(b) != "v2" {
		t.Fatalf("новая каноническая версия неверна: %q", b)
	}
	verFile := filepath.Join(e.trashDir, ".versions", "1", "report.txt")
	if b, _ := os.ReadFile(verFile); string(b) != "v1" {
		t.Fatalf("старая версия не в .versions/1: %q (путь %s)", b, verFile)
	}
	if n, _ := e.st.CountTrash(); n != 2 {
		t.Fatalf("CountTrash=%d, want 2", n)
	}
}

func TestMassDeleteEvent(t *testing.T) {
	e := setup(t, 2) // порог = 2
	for _, n := range []string{"a", "b", "c"} {
		e.makeStorageFile(t, n+".txt", n)
	}
	e.st.ResetClientFiles()
	if _, err := e.tr.MoveDeleted(5, 100); err != nil {
		t.Fatal(err)
	}
	evs, _ := e.st.ListEventsForCycle(5)
	found := false
	for _, ev := range evs {
		if ev.Type == "mass_delete" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ожидалось событие mass_delete, события: %+v", evs)
	}
}

func TestCleanupRetention(t *testing.T) {
	e := setup(t, 100)
	now := time.Now().UnixNano()
	// каноническая запись с просроченным сроком + файл на диске
	canonical := filepath.Join(e.trashDir, "old.txt")
	os.WriteFile(canonical, []byte("x"), 0o644)
	old := now - int64(20*24*time.Hour)
	e.st.InsertTrash("old.txt", 0, 1, old)
	// версия с просрочкой
	verFile := filepath.Join(e.trashDir, ".versions", "3", "old.txt")
	os.MkdirAll(filepath.Dir(verFile), 0o755)
	os.WriteFile(verFile, []byte("y"), 0o644)
	e.st.InsertTrash("old.txt", 3, 1, old)
	// свежая запись — не должна удаляться
	fresh := filepath.Join(e.trashDir, "fresh.txt")
	os.WriteFile(fresh, []byte("z"), 0o644)
	e.st.InsertTrash("fresh.txt", 0, 1, now)

	purged, err := e.tr.Cleanup(7, now)
	if err != nil {
		t.Fatal(err)
	}
	if purged != 2 {
		t.Fatalf("purged=%d, want 2", purged)
	}
	if _, err := os.Stat(canonical); !os.IsNotExist(err) {
		t.Fatal("просроченный канонический файл должен быть удалён")
	}
	if _, err := os.Stat(verFile); !os.IsNotExist(err) {
		t.Fatal("просроченная версия должна быть удалена")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("свежий файл не должен удаляться")
	}
	if n, _ := e.st.CountTrash(); n != 1 {
		t.Fatalf("в корзине должна остаться 1 запись, got %d", n)
	}
}
