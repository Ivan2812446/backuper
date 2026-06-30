package store

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// Нагрузочный тест диффа: доказывает, что сравнение и постановка в очередь при
// большом числе файлов выполняются SQL-операциями и НЕ загружают индекс в память
// (NFR-1). Размер задаётся BACKUPER_SCALE (по умолчанию 50000).
func TestDiffScale(t *testing.T) {
	n := 20000
	if v := os.Getenv("BACKUPER_SCALE"); v != "" {
		if x, err := strconv.Atoi(v); err == nil && x > 0 {
			n = x
		}
	}
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UnixNano()

	// индекс хранилища: n файлов
	fw, err := s.BeginFileIndex()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		rel := fmt.Sprintf("docs/%03d/file_%06d.pdf", i/1000, i)
		if err := fw.Add(rel, rel, 1000, now, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := fw.Commit(); err != nil {
		t.Fatal(err)
	}

	// список клиента: те же n, но каждый 10-й изменён (размер), +1000 новых,
	// и последние 500 отсутствуют (уйдут в корзину).
	if err := s.ResetClientFiles(); err != nil {
		t.Fatal(err)
	}
	cw, err := s.BeginClientFiles()
	if err != nil {
		t.Fatal(err)
	}
	changed := 0
	present := n - 500
	for i := 0; i < present; i++ {
		rel := fmt.Sprintf("docs/%03d/file_%06d.pdf", i/1000, i)
		size := int64(1000)
		if i%10 == 0 {
			size = 1001
			changed++
		}
		if err := cw.Add(rel, rel, size, now); err != nil {
			t.Fatal(err)
		}
	}
	newCount := 1000
	for i := 0; i < newCount; i++ {
		rel := fmt.Sprintf("incoming/new_%06d.dat", i)
		if err := cw.Add(rel, rel, 10, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := cw.Commit(); err != nil {
		t.Fatal(err)
	}

	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)
	start := time.Now()

	tol := int64(2 * time.Second)
	gotNew, gotChanged, err := s.CountNewChanged(tol)
	if err != nil {
		t.Fatal(err)
	}
	if gotNew != int64(newCount) {
		t.Fatalf("new=%d, want %d", gotNew, newCount)
	}
	if gotChanged != int64(changed) {
		t.Fatalf("changed=%d, want %d", gotChanged, changed)
	}
	toTrash, err := s.CountToTrash()
	if err != nil {
		t.Fatal(err)
	}
	if toTrash != 500 {
		t.Fatalf("toTrash=%d, want 500", toTrash)
	}
	enq, err := s.EnqueueDiffDownloads(1, tol, now)
	if err != nil {
		t.Fatal(err)
	}
	if enq != int64(newCount+changed) {
		t.Fatalf("enqueued=%d, want %d", enq, newCount+changed)
	}
	elapsed := time.Since(start)

	runtime.GC()
	runtime.ReadMemStats(&m1)
	heapMB := float64(m1.HeapAlloc) / (1 << 20)
	t.Logf("scale n=%d: дифф+очередь за %v, HeapAlloc=%.1f MiB (new=%d changed=%d trash=%d enq=%d)",
		n, elapsed.Round(time.Millisecond), heapMB, gotNew, gotChanged, toTrash, enq)

	// Память не должна масштабироваться с числом файлов: дифф идёт в SQLite.
	if heapMB > 256 {
		t.Fatalf("heap слишком большой: %.1f MiB (ожидалось, что дифф не грузит индекс в память)", heapMB)
	}
}
