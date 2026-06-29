package transfer

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"backuper/internal/logx"
	"backuper/internal/store"
	"backuper/internal/tlsconn"
)

// При устойчивой невозможности подключиться очередь должна прерываться
// (ErrConnectionLost), а не долбить каждый файл бесконечно (раздел 14).
func TestRunDownloadQueueAbortsOnConnLoss(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UnixNano()
	for _, rel := range []string{"a.txt", "b.txt", "c.txt"} {
		if _, err := st.InsertTask(rel, "download", 10, 0, "pending", 1, now); err != nil {
			t.Fatal(err)
		}
	}
	log, _ := logx.New(logx.Options{Actor: "test", Dir: t.TempDir(), Console: false, Level: logx.LevelError})

	var dials int32
	m := New(Deps{
		Store: st, Log: log,
		NewConn: func() (*tlsconn.Conn, error) {
			atomic.AddInt32(&dials, 1)
			return nil, errors.New("клиент недоступен")
		},
		Cfg: Config{
			StorageDir: t.TempDir(), TempDir: t.TempDir(),
			Parallel: 2, ChunkSize: 1 << 20,
			RetryCount: 2, RetryDelay: 5 * time.Millisecond,
		},
	})

	start := time.Now()
	res, err := m.RunDownloadQueue(context.Background(), 1)
	if !errors.Is(err, ErrConnectionLost) {
		t.Fatalf("ожидался ErrConnectionLost, получено %v", err)
	}
	if res.DownloadedFiles != 0 {
		t.Fatalf("ничего не должно скачаться, got %d", res.DownloadedFiles)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("очередь не прервалась быстро: %v", time.Since(start))
	}
	if atomic.LoadInt32(&dials) < 2 {
		t.Fatalf("ожидались повторные попытки подключения, dials=%d", atomic.LoadInt32(&dials))
	}
	// задачи не должны массово помечаться skipped — они остаются на следующий цикл
	if n, _ := st.CountTasks("skipped"); n == 3 {
		t.Fatalf("все задачи пропущены — должно было прерваться раньше")
	}
}
