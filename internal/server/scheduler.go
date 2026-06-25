// Package server (scheduler.go) — планировщик циклов по SYNC_INTERVAL, обработка
// наложения (очередь из 1), алерт о перезапуске, graceful shutdown (разделы 7.1, 15.2, 19 ТЗ).
package server

import (
	"context"
	"sync"
	"time"
)

// Run запускает планировщик до отмены ctx.
func (s *Server) Run(ctx context.Context) error {
	// алерт о перезапуске после аварийного завершения (15.2, 19.1)
	if v, ok, _ := s.st.GetMeta("running"); ok && v == "1" {
		s.log.Warn("service", "обнаружен аварийный предыдущий выход — отправка алерта о перезапуске")
		_ = s.alert.SendImmediate("restart", "Служба Backuper перезапущена после сбоя",
			[]string{"Предыдущий процесс сервера завершился нештатно.",
				"Время: " + time.Now().In(s.cfg.Loc).Format("2006-01-02 15:04:05 MST")})
	}
	_ = s.st.SetMeta("running", "1")
	_ = s.st.ResetStaleInProgress(time.Now().UnixNano()) // возобновление прерванных задач (9.3)
	defer s.st.SetMeta("running", "0")                   // чистое завершение

	var mu sync.Mutex
	running := false
	pending := false
	var wg sync.WaitGroup

	runChain := func() {
		for {
			s.RunCycle(ctx)
			mu.Lock()
			if pending && ctx.Err() == nil {
				pending = false
				mu.Unlock()
				continue
			}
			running = false
			mu.Unlock()
			return
		}
	}

	trigger := func() {
		mu.Lock()
		if running {
			if !pending {
				pending = true
				mu.Unlock()
				s.log.Warn("scheduler", "цикл уже выполняется — новый поставлен в очередь")
				_ = s.alert.SendImmediate("overlap", "Наложение циклов Backuper",
					[]string{"Запуск нового цикла отложен: предыдущий ещё выполняется."})
			} else {
				mu.Unlock()
			}
			return
		}
		running = true
		mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			runChain()
		}()
	}

	trigger() // первый цикл сразу при старте

	ticker := time.NewTicker(s.cfg.SyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.log.Info("scheduler", "остановка: ожидание завершения активного цикла")
			wg.Wait()
			return nil
		case <-ticker.C:
			trigger()
		}
	}
}
