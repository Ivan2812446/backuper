package store

import (
	"path/filepath"
	"testing"
	"time"

	"backuper/internal/protocol"
)

const tol = int64(2 * time.Second)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func addClient(t *testing.T, s *Store, entries map[string]struct {
	size  int64
	mtime int64
}) {
	t.Helper()
	if err := s.ResetClientFiles(); err != nil {
		t.Fatal(err)
	}
	w, err := s.BeginClientFiles()
	if err != nil {
		t.Fatal(err)
	}
	for rel, e := range entries {
		if err := w.Add(rel, protocol.NormKey(rel), e.size, e.mtime); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestFilesUpsertGet(t *testing.T) {
	s := openTemp(t)
	now := time.Now().UnixNano()
	if err := s.UpsertFile("Docs/A.txt", "docs/a.txt", 100, 50, now); err != nil {
		t.Fatal(err)
	}
	rel, size, mtime, ok, err := s.GetFile("docs/a.txt")
	if err != nil || !ok {
		t.Fatalf("GetFile ok=%v err=%v", ok, err)
	}
	if rel != "Docs/A.txt" || size != 100 || mtime != 50 {
		t.Fatalf("got %s/%d/%d", rel, size, mtime)
	}
	// upsert обновляет
	if err := s.UpsertFile("Docs/A.txt", "docs/a.txt", 200, 60, now+1); err != nil {
		t.Fatal(err)
	}
	_, size, _, _, _ = s.GetFile("docs/a.txt")
	if size != 200 {
		t.Fatalf("upsert не обновил размер: %d", size)
	}
	if n, _ := s.CountFiles(); n != 1 {
		t.Fatalf("CountFiles=%d", n)
	}
	if err := s.DeleteFileByNorm("docs/a.txt"); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountFiles(); n != 0 {
		t.Fatalf("после удаления CountFiles=%d", n)
	}
}

type fe = struct {
	size  int64
	mtime int64
}

func TestDiff(t *testing.T) {
	s := openTemp(t)
	now := time.Now().UnixNano()
	// индекс хранилища
	s.UpsertFile("same.txt", "same.txt", 10, 1000, now)
	s.UpsertFile("changed_size.txt", "changed_size.txt", 10, 1000, now)
	s.UpsertFile("changed_mtime.txt", "changed_mtime.txt", 10, 1000, now)
	s.UpsertFile("deleted.txt", "deleted.txt", 10, 1000, now)

	// список клиента
	addClient(t, s, map[string]fe{
		"same.txt":          {10, 1000},                        // не изменился
		"changed_size.txt":  {20, 1000},                        // изменился размер
		"changed_mtime.txt": {10, 1000 + 5*int64(time.Second)}, // mtime > tol
		"new.txt":           {5, 2000},                         // новый
	})

	newC, changedC, err := s.CountNewChanged(tol)
	if err != nil {
		t.Fatal(err)
	}
	if newC != 1 {
		t.Errorf("new=%d, want 1", newC)
	}
	if changedC != 2 {
		t.Errorf("changed=%d, want 2", changedC)
	}

	trash, err := s.CountToTrash()
	if err != nil {
		t.Fatal(err)
	}
	if trash != 1 { // deleted.txt
		t.Errorf("toTrash=%d, want 1", trash)
	}

	enq, err := s.EnqueueDiffDownloads(1, tol, now)
	if err != nil {
		t.Fatal(err)
	}
	if enq != 3 { // 1 new + 2 changed
		t.Errorf("enqueued=%d, want 3", enq)
	}
	// повторный enqueue не дублирует (уже pending)
	enq2, _ := s.EnqueueDiffDownloads(1, tol, now)
	if enq2 != 0 {
		t.Errorf("повторный enqueue=%d, want 0", enq2)
	}

	batch, err := s.SelectTrashBatch(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 1 || batch[0].Relpath != "deleted.txt" {
		t.Fatalf("trash batch=%+v", batch)
	}
}

func TestCollisions(t *testing.T) {
	s := openTemp(t)
	if err := s.ResetClientFiles(); err != nil {
		t.Fatal(err)
	}
	w, _ := s.BeginClientFiles()
	w.Add("Docs/File.txt", protocol.NormKey("Docs/File.txt"), 1, 1)
	w.Add("docs/file.txt", protocol.NormKey("docs/file.txt"), 2, 2) // та же норма
	w.Add("unique.txt", protocol.NormKey("unique.txt"), 3, 3)
	w.Commit()

	n, err := s.CountClientCollisions()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("collisions=%d, want 1", n)
	}
	ex, _ := s.ListClientCollisionExamples(10)
	if len(ex) != 2 {
		t.Fatalf("examples=%v", ex)
	}
	removed, _ := s.DedupClientFiles()
	if removed != 1 {
		t.Fatalf("removed=%d, want 1", removed)
	}
	if cnt, _ := s.CountClientFiles(); cnt != 2 {
		t.Fatalf("после dedup client_files=%d, want 2", cnt)
	}
}

func TestTasksLifecycle(t *testing.T) {
	s := openTemp(t)
	now := time.Now().UnixNano()
	id, err := s.InsertTask("a.txt", "download", 100, 0, "pending", 1, now)
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.ListRunnableTasksPage("download", 0, 10)
	if err != nil || len(page) != 1 || page[0].ID != id {
		t.Fatalf("runnable=%+v err=%v", page, err)
	}
	if err := s.MarkTaskInProgress(id, now); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateTaskOffset(id, 50, now); err != nil {
		t.Fatal(err)
	}
	// неудача -> attempts++
	att, err := s.IncTaskAttempt(id, "boom", now)
	if err != nil || att != 1 {
		t.Fatalf("attempts=%d err=%v", att, err)
	}
	s.SetTaskStatus(id, "pending", now)
	att2, _ := s.IncTaskAttempt(id, "boom2", now)
	if att2 != 2 {
		t.Fatalf("attempts=%d, want 2", att2)
	}
	// после неудачи статус 'failed'; возвращаем в pending для повтора и проверяем,
	// что offset докачки сохранён через все переходы статусов
	s.SetTaskStatus(id, "pending", now)
	page, _ = s.ListRunnableTasksPage("download", 0, 10)
	if len(page) != 1 || page[0].Offset != 50 {
		t.Fatalf("offset не сохранён: %+v", page)
	}
	s.SetTaskStatus(id, "skipped", now)
	if c, _ := s.CountTasks("skipped"); c != 1 {
		t.Fatalf("skipped=%d", c)
	}
	if c, _ := s.CountTasks("pending"); c != 0 {
		t.Fatalf("pending=%d", c)
	}

	// done cleanup
	id2, _ := s.InsertTask("b.txt", "download", 1, 0, "pending", 1, now)
	s.MarkTaskDone(id2, now)
	s.DeleteDoneTasks()
	if c, _ := s.CountTasks("done"); c != 0 {
		t.Fatalf("done после очистки=%d", c)
	}
	s.DeleteTasksByStatus("skipped")
	if c, _ := s.CountTasks("skipped"); c != 0 {
		t.Fatalf("skipped после очистки=%d", c)
	}
}

func TestResetStaleInProgress(t *testing.T) {
	s := openTemp(t)
	now := time.Now().UnixNano()
	id, _ := s.InsertTask("a", "download", 1, 0, "in_progress", 1, now)
	if err := s.ResetStaleInProgress(now); err != nil {
		t.Fatal(err)
	}
	page, _ := s.ListRunnableTasksPage("download", 0, 10)
	if len(page) != 1 || page[0].Status != "pending" || page[0].ID != id {
		t.Fatalf("stale не сброшен: %+v", page)
	}
}

func TestTrashStore(t *testing.T) {
	s := openTemp(t)
	now := time.Now().UnixNano()
	if has, _ := s.HasCanonicalTrash("x/y.txt"); has {
		t.Fatal("не должно быть канонической записи")
	}
	if _, err := s.InsertTrash("x/y.txt", 0, 100, now); err != nil {
		t.Fatal(err)
	}
	if has, _ := s.HasCanonicalTrash("x/y.txt"); !has {
		t.Fatal("должна быть каноническая запись")
	}
	if v, _ := s.MaxTrashVersion("x/y.txt"); v != 0 {
		t.Fatalf("maxver=%d", v)
	}
	// поднять канон в версию 1, добавить новый канон
	if err := s.BumpCanonicalToVersion("x/y.txt", 1); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.MaxTrashVersion("x/y.txt"); v != 1 {
		t.Fatalf("maxver после bump=%d", v)
	}
	s.InsertTrash("x/y.txt", 0, 200, now)
	if c, _ := s.CountTrash(); c != 2 {
		t.Fatalf("CountTrash=%d", c)
	}
	// просрочка
	old := now - int64(20*24*time.Hour)
	s.InsertTrash("old.txt", 0, 1, old)
	due, _ := s.SelectTrashDue(now-int64(10*24*time.Hour), 100)
	if len(due) != 1 || due[0].Relpath != "old.txt" {
		t.Fatalf("due=%+v", due)
	}
	s.DeleteTrash(due[0].ID)
	if c, _ := s.CountTrash(); c != 2 {
		t.Fatalf("после удаления due CountTrash=%d", c)
	}
}

func TestCycles(t *testing.T) {
	s := openTemp(t)
	now := time.Now().UnixNano()
	id, err := s.CreateCycle(now)
	if err != nil {
		t.Fatal(err)
	}
	c := Cycle{ID: id, StartedAt: now, FinishedAt: now + 1000, Status: "OK",
		PassesUsed: 2, DownloadedFiles: 5, DownloadedBytes: 999, ChangedFiles: 1,
		TrashedFiles: 2, PurgedFiles: 3, SkippedFiles: 0, ErrorSummary: ""}
	if err := s.FinalizeCycle(c); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetLastCycle()
	if err != nil || !ok {
		t.Fatalf("GetLastCycle ok=%v err=%v", ok, err)
	}
	if got.Status != "OK" || got.DownloadedFiles != 5 || got.PassesUsed != 2 || got.PurgedFiles != 3 {
		t.Fatalf("cycle mismatch: %+v", got)
	}
}

func TestEvents(t *testing.T) {
	s := openTemp(t)
	now := time.Now().UnixNano()
	s.InsertEvent(7, "WARN", "disk", "", "диск переполнен", now)
	s.InsertEvent(7, "ERROR", "io", "a.txt", "ошибка", now)
	s.InsertEvent(0, "WARN", "overlap", "", "наложение", now)

	un, _ := s.ListUnsentEvents()
	if len(un) != 3 {
		t.Fatalf("unsent=%d", len(un))
	}
	forCycle, _ := s.ListEventsForCycle(7)
	if len(forCycle) != 2 {
		t.Fatalf("forCycle=%d", len(forCycle))
	}
	var ids []int64
	for _, e := range forCycle {
		ids = append(ids, e.ID)
	}
	s.MarkEventsSent(ids)
	if c, _ := s.CountUnsentEvents(); c != 1 {
		t.Fatalf("после отметки unsent=%d, want 1", c)
	}
}

func TestMeta(t *testing.T) {
	s := openTemp(t)
	if init, _ := s.Initialized(); init {
		t.Fatal("новый стор не должен быть initialized")
	}
	if err := s.SetInitialized(); err != nil {
		t.Fatal(err)
	}
	if init, _ := s.Initialized(); !init {
		t.Fatal("после SetInitialized должно быть true")
	}
	s.SetMeta("k", "v")
	if v, ok, _ := s.GetMeta("k"); !ok || v != "v" {
		t.Fatalf("meta get %q ok=%v", v, ok)
	}
	if _, ok, _ := s.GetMeta("missing"); ok {
		t.Fatal("missing не должен существовать")
	}
}

func TestListFilesPageAndClientMtime(t *testing.T) {
	s := openTemp(t)
	now := time.Now().UnixNano()
	s.UpsertFile("docs/a.txt", "docs/a.txt", 1, 11, now)
	s.UpsertFile("docs/b.txt", "docs/b.txt", 2, 22, now)
	s.UpsertFile("other/c.txt", "other/c.txt", 3, 33, now)

	page, err := s.ListFilesPage("docs", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 {
		t.Fatalf("page subtree docs=%d, want 2", len(page))
	}
	all, _ := s.ListFilesPage("", "", 10)
	if len(all) != 3 {
		t.Fatalf("page all=%d", len(all))
	}

	addClient(t, s, map[string]fe{"docs/a.txt": {1, 99}})
	mt, ok, _ := s.ClientFileMtime("docs/a.txt")
	if !ok || mt != 99 {
		t.Fatalf("ClientFileMtime ok=%v mt=%d", ok, mt)
	}
	if _, ok, _ := s.ClientFileMtime("docs/b.txt"); ok {
		t.Fatal("b.txt не в списке клиента")
	}
}
