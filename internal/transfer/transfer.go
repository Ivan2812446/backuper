// Package transfer — параллельное скачивание клиент→сервер: пул соединений,
// докачка с offset, проверка размера в байтах, лимит скорости, повторные
// попытки и атомарное сохранение (разделы 9, 10, 14 ТЗ).
package transfer

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"backuper/internal/logx"
	"backuper/internal/protocol"
	"backuper/internal/store"
	"backuper/internal/tlsconn"
)

// ErrConnectionLost — связь с клиентом потеряна во время фазы передачи:
// очередь прерывается, цикл продолжится со следующего планового (раздел 14 ТЗ).
var ErrConnectionLost = errors.New("связь с клиентом потеряна во время передачи")

// Config — параметры передачи.
type Config struct {
	StorageDir       string
	TempDir          string
	Parallel         int
	ChunkSize        int64
	BandwidthLimit   int64
	RetryCount       int
	RetryDelay       time.Duration
	SaveFilePerms    os.FileMode
	SaveDirPerms     os.FileMode
	MtimeToleranceNS int64
	MaxFrame         uint64
	DeltaMinSize     int64 // дельта для изменённых файлов ≥ этого размера (0 — выкл.)
	DeltaBlockSize   int64
}

// Deps — зависимости менеджера передачи.
type Deps struct {
	Store   *store.Store
	Log     *logx.Logger
	NewConn func() (*tlsconn.Conn, error) // фабрика авторизованных data-соединений
	Cfg     Config
}

// Skip — пропущенный файл (после исчерпания попыток, 9.4).
type Skip struct {
	Relpath  string
	Reason   string
	Attempts int
}

// Err — ошибка передачи для агрегированного алерта (5.5, 15).
type Err struct {
	Code    uint16
	Relpath string
	Message string
}

// Result — итог обработки очереди.
type Result struct {
	DownloadedFiles int64
	DownloadedBytes int64
	SkippedFiles    int64
	PeakParallel    int
	Skipped         []Skip
	Errors          []Err
}

// Manager — менеджер передачи.
type Manager struct {
	d   Deps
	lim *limiter

	mu     sync.Mutex
	res    Result
	active int32
	peak   int32

	cancel   context.CancelFunc // отмена текущего прогона очереди
	connLost int32              // 1 = связь с клиентом потеряна (очередь прервана)
}

// New создаёт менеджер передачи.
func New(d Deps) *Manager {
	return &Manager{d: d, lim: newLimiter(d.Cfg.BandwidthLimit, d.Cfg.ChunkSize)}
}

func partPaths(tempDir, relpath string) (part, meta string) {
	h := sha1.Sum([]byte(protocol.CleanRel(relpath)))
	base := filepath.Join(tempDir, hex.EncodeToString(h[:]))
	return base + ".part", base + ".meta"
}

type partMeta struct {
	Relpath string `json:"relpath"`
	Total   uint64 `json:"total"`
	Mtime   int64  `json:"mtime"`
}

func readMeta(path string) (partMeta, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return partMeta{}, false
	}
	var m partMeta
	if json.Unmarshal(b, &m) != nil {
		return partMeta{}, false
	}
	return m, true
}

func writeMeta(path string, m partMeta) error {
	b, _ := json.Marshal(m)
	return os.WriteFile(path, b, 0o600)
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// RunDownloadQueue обрабатывает задачи скачивания (pending/in_progress) пулом из
// Parallel соединений. Задачи подаются постранично (NFR-1). cycleID — для аудита.
func (m *Manager) RunDownloadQueue(parent context.Context, cycleID int64) (Result, error) {
	ctx, cancel := context.WithCancel(parent)
	m.cancel = cancel
	defer cancel()

	ch := make(chan store.Task)
	var wg sync.WaitGroup
	workers := m.d.Cfg.Parallel
	if workers < 1 {
		workers = 1
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.worker(ctx, cycleID, ch)
		}()
	}

	const pageSize = 2000
	var afterID int64
	var produceErr error
	for {
		page, err := m.d.Store.ListRunnableTasksPage("download", afterID, pageSize)
		if err != nil {
			produceErr = err
			break
		}
		if len(page) == 0 {
			break
		}
		stop := false
		for _, t := range page {
			afterID = t.ID
			select {
			case <-ctx.Done():
				stop = true
			case ch <- t:
			}
			if stop {
				break
			}
		}
		if stop {
			break
		}
	}
	close(ch)
	wg.Wait()
	m.res.PeakParallel = int(m.peak)
	if atomic.LoadInt32(&m.connLost) == 1 {
		return m.res, ErrConnectionLost
	}
	if produceErr != nil {
		return m.res, produceErr
	}
	return m.res, parent.Err()
}

func (m *Manager) worker(ctx context.Context, cycleID int64, ch <-chan store.Task) {
	var conn *tlsconn.Conn
	defer func() {
		if conn != nil {
			conn.Close()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case t, ok := <-ch:
			if !ok {
				return
			}
			conn = m.processTask(ctx, cycleID, t, conn)
		}
	}
}

// outcome — исход одной попытки скачивания файла.
type outcome struct {
	ok       bool
	bytes    int64
	dropConn bool // соединение в неопределённом состоянии — пересоздать
	code     uint16
	message  string
	skipNow  bool // не повторять (NOT_FOUND)
}

// processTask выполняет задачу с повторными попытками (9.4, 14) и возвращает
// соединение для повторного использования следующей задачей (или nil).
func (m *Manager) processTask(ctx context.Context, cycleID int64, t store.Task, conn *tlsconn.Conn) *tlsconn.Conn {
	cur := atomic.AddInt32(&m.active, 1)
	for { // lock-free обновление пика параллельности
		p := atomic.LoadInt32(&m.peak)
		if cur <= p || atomic.CompareAndSwapInt32(&m.peak, p, cur) {
			break
		}
	}
	defer atomic.AddInt32(&m.active, -1)

	now := func() int64 { return time.Now().UnixNano() }
	_ = m.d.Store.MarkTaskInProgress(t.ID, now())

	for {
		if conn == nil {
			c, err := m.dial(ctx, cycleID)
			if err != nil {
				// связь с клиентом потеряна (или отмена) — очередь прервана
				return nil
			}
			conn = c
		}
		oc := m.downloadOne(ctx, cycleID, conn, t)
		if oc.dropConn {
			conn.Close()
			conn = nil
		}
		if oc.ok {
			m.mu.Lock()
			m.res.DownloadedFiles++
			m.res.DownloadedBytes += oc.bytes
			m.mu.Unlock()
			return conn
		}
		if oc.skipNow {
			m.skip(cycleID, &t, oc.code, oc.message, t.Attempts)
			return conn
		}
		if m.failAttempt(ctx, cycleID, &t, oc.code, oc.message) {
			return conn
		}
		// повтор: ctx мог быть отменён внутри failAttempt
		if ctx.Err() != nil {
			return conn
		}
	}
}

// dial создаёт data-соединение с повторами. Если связь не восстановилась за
// RetryCount попыток — это сбой уровня соединения: помечаем потерю связи,
// прерываем всю очередь (раздел 14) и возвращаем ошибку.
func (m *Manager) dial(ctx context.Context, cycleID int64) (*tlsconn.Conn, error) {
	var lastErr error
	for attempt := 1; attempt <= m.d.Cfg.RetryCount; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		c, err := m.d.NewConn()
		if err == nil {
			return c, nil
		}
		lastErr = err
		m.d.Log.Warn("transfer", "подключение к клиенту не удалось (попытка %d/%d): %v",
			attempt, m.d.Cfg.RetryCount, err)
		if attempt < m.d.Cfg.RetryCount {
			timer := time.NewTimer(m.d.Cfg.RetryDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
	}
	// устойчивый сбой соединения — прерываем очередь, цикл продолжится со следующего планового
	if atomic.CompareAndSwapInt32(&m.connLost, 0, 1) {
		msg := "связь с клиентом потеряна во время передачи: " + lastErr.Error()
		m.mu.Lock()
		m.res.Errors = append(m.res.Errors, Err{Code: protocol.ErrIOError, Message: msg})
		m.mu.Unlock()
		_, _ = m.d.Store.InsertEvent(cycleID, "ERROR", "io", "", msg, time.Now().UnixNano())
		m.d.Log.Error("transfer", "%s — очередь прервана", msg)
		if m.cancel != nil {
			m.cancel()
		}
	}
	return nil, lastErr
}

// failAttempt фиксирует неудачу и решает, повторять или пропустить (9.4).
// Возвращает true, если дальнейших попыток не будет (skip или отмена).
func (m *Manager) failAttempt(ctx context.Context, cycleID int64, t *store.Task, code uint16, msg string) (stop bool) {
	now := time.Now().UnixNano()
	attempts, _ := m.d.Store.IncTaskAttempt(t.ID, protocol.ErrName(code)+": "+msg, now)
	t.Attempts = attempts
	m.mu.Lock()
	m.res.Errors = append(m.res.Errors, Err{Code: code, Relpath: t.Relpath, Message: msg})
	m.mu.Unlock()
	m.d.Log.Warn("transfer", "ошибка %s %s (попытка %d/%d): %s",
		protocol.ErrName(code), t.Relpath, attempts, m.d.Cfg.RetryCount, msg)
	if attempts >= m.d.Cfg.RetryCount {
		m.skip(cycleID, t, code, msg, attempts)
		return true
	}
	_ = m.d.Store.SetTaskStatus(t.ID, "pending", now)
	// пауза RETRY_DELAY (прерываемая)
	timer := time.NewTimer(m.d.Cfg.RetryDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return true
	case <-timer.C:
	}
	_ = m.d.Store.MarkTaskInProgress(t.ID, time.Now().UnixNano())
	return false
}

func (m *Manager) skip(cycleID int64, t *store.Task, code uint16, msg string, attempts int) {
	now := time.Now().UnixNano()
	_ = m.d.Store.SetTaskStatus(t.ID, "skipped", now)
	reason := protocol.ErrName(code) + ": " + msg
	m.mu.Lock()
	m.res.SkippedFiles++
	m.res.Skipped = append(m.res.Skipped, Skip{Relpath: t.Relpath, Reason: reason, Attempts: attempts})
	m.mu.Unlock()
	_, _ = m.d.Store.InsertEvent(cycleID, "WARN", "io", t.Relpath, "пропущен: "+reason, now)
	m.d.Log.Error("transfer", "файл пропущен %s: %s (попыток %d)", t.Relpath, reason, attempts)
	m.d.Log.Audit("skip", t.Relpath, t.Size, "error", attempts, cycleID)
}

// downloadOne выполняет одну попытку скачивания файла по соединению.
func (m *Manager) downloadOne(ctx context.Context, cycleID int64, conn *tlsconn.Conn, t store.Task) outcome {
	rel := t.Relpath
	target, err := protocol.SafeJoin(m.d.Cfg.StorageDir, rel)
	if err != nil {
		return outcome{code: protocol.ErrProtocol, message: err.Error(), skipNow: true}
	}

	// Дельта-передача для изменённых файлов (есть старая копия, размер ≥ порога).
	if m.d.Cfg.DeltaMinSize > 0 && m.d.Cfg.DeltaBlockSize > 0 {
		if st, serr := os.Stat(target); serr == nil && !st.IsDir() && st.Size() >= m.d.Cfg.DeltaMinSize {
			if oc, fallback := m.deltaDownload(ctx, cycleID, conn, t, target, st.Size()); !fallback {
				return oc
			}
		}
	}

	part, meta := partPaths(m.d.Cfg.TempDir, rel)

	var offset int64
	if st, err := os.Stat(part); err == nil {
		offset = st.Size()
	}
	prevMeta, hasMeta := readMeta(meta)

	// GET_REQ
	if err := conn.WriteMsg(protocol.MsgGetReq, protocol.GetReq{Path: rel, Offset: uint64(offset)}.Encode()); err != nil {
		return outcome{dropConn: true, code: protocol.ErrIOError, message: err.Error()}
	}
	payload, err := conn.ReadExpect(protocol.MsgGetResp)
	if err != nil {
		if pe, ok := err.(*protocol.ProtoError); ok {
			skip := pe.Code == protocol.ErrNotFound
			return outcome{code: pe.Code, message: pe.Msg, skipNow: skip}
		}
		return outcome{dropConn: true, code: protocol.ErrIOError, message: err.Error()}
	}
	resp, err := protocol.ParseGetResp(payload)
	if err != nil {
		return outcome{dropConn: true, code: protocol.ErrProtocol, message: err.Error()}
	}
	if resp.Status != 0 {
		return outcome{code: protocol.ErrIOError, message: "GET_RESP status != 0"}
	}

	// детект изменения файла во время докачки (9.3)
	if offset > 0 {
		changed := !hasMeta || prevMeta.Total != resp.TotalSize || absInt64(prevMeta.Mtime-resp.Mtime) > m.d.Cfg.MtimeToleranceNS
		if changed {
			os.Remove(part)
			os.Remove(meta)
			_ = m.d.Store.SetTaskResetOffset(t.ID, time.Now().UnixNano())
			m.d.Log.Info("transfer", "файл изменился при докачке, перекачка с 0: %s", rel)
			// поток уже идёт от offset — соединение прервать
			return outcome{dropConn: true, code: protocol.ErrBadOffset, message: "changed during resume"}
		}
	}
	_ = writeMeta(meta, partMeta{Relpath: rel, Total: resp.TotalSize, Mtime: resp.Mtime})

	if err := os.MkdirAll(m.d.Cfg.TempDir, 0o755); err != nil {
		return outcome{code: protocol.ErrIOError, message: err.Error()}
	}
	f, err := os.OpenFile(part, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return outcome{code: protocol.ErrIOError, message: err.Error()}
	}
	if err := f.Truncate(offset); err != nil {
		f.Close()
		return outcome{code: protocol.ErrIOError, message: err.Error()}
	}
	if _, err := f.Seek(offset, 0); err != nil {
		f.Close()
		return outcome{code: protocol.ErrIOError, message: err.Error()}
	}

	written := offset
	lastPersist := offset
	const persistEvery = 16 << 20
	for {
		mt, data, err := conn.ReadMsg()
		if err != nil {
			f.Close()
			_ = m.d.Store.UpdateTaskOffset(t.ID, written, time.Now().UnixNano())
			return outcome{dropConn: true, code: protocol.ErrIOError, message: err.Error()}
		}
		switch mt {
		case protocol.MsgFileData:
			if err := m.lim.wait(ctx, len(data)); err != nil {
				f.Close()
				_ = m.d.Store.UpdateTaskOffset(t.ID, written, time.Now().UnixNano())
				return outcome{dropConn: true, code: protocol.ErrIOError, message: "отменено"}
			}
			n, werr := f.Write(data)
			written += int64(n)
			if werr != nil {
				f.Close()
				return outcome{code: protocol.ErrIOError, message: werr.Error()}
			}
			if uint64(written) > resp.TotalSize {
				f.Close()
				os.Remove(part)
				os.Remove(meta)
				_ = m.d.Store.SetTaskResetOffset(t.ID, time.Now().UnixNano())
				return outcome{dropConn: true, code: protocol.ErrSizeMismatch, message: "превышение total_size"}
			}
			if written-lastPersist >= persistEvery {
				_ = m.d.Store.UpdateTaskOffset(t.ID, written, time.Now().UnixNano())
				lastPersist = written
			}
		case protocol.MsgFileEnd:
			goto done
		case protocol.MsgError:
			em, _ := protocol.ParseErrorMsg(data)
			f.Close()
			return outcome{code: em.Code, message: em.Message}
		default:
			f.Close()
			return outcome{dropConn: true, code: protocol.ErrProtocol, message: "неожиданный кадр " + protocol.MsgName(mt)}
		}
	}
done:
	if err := f.Sync(); err != nil {
		f.Close()
		return outcome{code: protocol.ErrIOError, message: err.Error()}
	}
	f.Close()

	// проверка целостности по размеру (10)
	st, err := os.Stat(part)
	if err != nil {
		return outcome{code: protocol.ErrIOError, message: err.Error()}
	}
	if uint64(st.Size()) != resp.TotalSize {
		os.Remove(part)
		os.Remove(meta)
		_ = m.d.Store.SetTaskResetOffset(t.ID, time.Now().UnixNano())
		return outcome{code: protocol.ErrSizeMismatch,
			message: "размер после передачи не совпал"}
	}

	// различаем новый файл и изменение для аудита (16)
	_, _, _, existed, _ := m.d.Store.GetFile(protocol.NormKey(rel))

	// атомарное завершение (9.2)
	if err := os.MkdirAll(filepath.Dir(target), m.d.Cfg.SaveDirPerms); err != nil {
		return outcome{code: protocol.ErrIOError, message: err.Error()}
	}
	if err := os.Rename(part, target); err != nil {
		return outcome{code: protocol.ErrIOError, message: "rename: " + err.Error()}
	}
	_ = os.Chmod(target, m.d.Cfg.SaveFilePerms)
	mt := time.Unix(0, resp.Mtime)
	_ = os.Chtimes(target, mt, mt) // mtime копии = mtime клиента (7.2)
	os.Remove(meta)

	now := time.Now().UnixNano()
	if err := m.d.Store.UpsertFile(protocol.CleanRel(rel), protocol.NormKey(rel), int64(resp.TotalSize), resp.Mtime, now); err != nil {
		return outcome{code: protocol.ErrIOError, message: "индекс: " + err.Error()}
	}
	_ = m.d.Store.MarkTaskDone(t.ID, now)
	op := "download"
	if existed {
		op = "change"
	}
	m.d.Log.Info("transfer", "%s %s (%d Б)", map[bool]string{false: "скачан", true: "обновлён"}[existed], rel, resp.TotalSize)
	m.d.Log.Audit(op, rel, int64(resp.TotalSize), "ok", t.Attempts+1, cycleID)
	return outcome{ok: true, bytes: written - offset}
}

// deltaDownload пытается получить изменённый файл дельтой (по блокам фикс. размера):
// сервер шлёт хэши блоков СТАРОЙ копии, клиент присылает только изменённые блоки,
// неизменённые копируются из старого файла. Возвращает (исход, fallback). Если
// fallback=true — нужно перейти на обычное полное скачивание (клиент не поддержал
// дельту, не читается старый файл или кадр хэшей слишком велик).
func (m *Manager) deltaDownload(ctx context.Context, cycleID int64, conn *tlsconn.Conn, t store.Task, oldPath string, oldSize int64) (outcome, bool) {
	rel := t.Relpath
	bs := m.d.Cfg.DeltaBlockSize
	nOld := (oldSize + bs - 1) / bs
	// кадр хэшей должен помещаться в MAX_FRAME_BYTES, иначе — полное скачивание
	if uint64(nOld)*32+uint64(len(rel))+16 > m.d.Cfg.MaxFrame {
		return outcome{}, true
	}
	oldF, err := os.Open(oldPath)
	if err != nil {
		return outcome{}, true
	}
	defer oldF.Close()

	hashes := make([][32]byte, 0, nOld)
	hbuf := make([]byte, bs)
	for i := int64(0); i < nOld; i++ {
		bl := bs
		if oldSize-i*bs < bl {
			bl = oldSize - i*bs
		}
		n, _ := oldF.ReadAt(hbuf[:bl], i*bs)
		if int64(n) != bl {
			return outcome{}, true // старый файл изменился под нами — на полное скачивание
		}
		hashes = append(hashes, sha256.Sum256(hbuf[:bl]))
	}

	if err := conn.WriteMsg(protocol.MsgGetDelta,
		protocol.DeltaReq{Path: rel, BlockSize: uint32(bs), Hashes: hashes}.Encode()); err != nil {
		return outcome{dropConn: true, code: protocol.ErrIOError, message: err.Error()}, false
	}
	mt, payload, err := conn.ReadMsg()
	if err != nil {
		return outcome{dropConn: true, code: protocol.ErrIOError, message: err.Error()}, false
	}
	if mt == protocol.MsgError {
		em, _ := protocol.ParseErrorMsg(payload)
		if em.Code == protocol.ErrUnsupported {
			return outcome{}, true // старый клиент без дельты — полное скачивание
		}
		return outcome{code: em.Code, message: em.Message, skipNow: em.Code == protocol.ErrNotFound}, false
	}
	if mt != protocol.MsgDeltaResp {
		return outcome{dropConn: true, code: protocol.ErrProtocol, message: "ожидался DELTA_RESP"}, false
	}
	resp, perr := protocol.ParseDeltaResp(payload)
	if perr != nil || resp.Status != 0 {
		return outcome{dropConn: true, code: protocol.ErrProtocol, message: "DELTA_RESP"}, false
	}

	part, meta := partPaths(m.d.Cfg.TempDir, rel)
	os.Remove(meta)
	if err := os.MkdirAll(m.d.Cfg.TempDir, 0o755); err != nil {
		return outcome{code: protocol.ErrIOError, message: err.Error()}, false
	}
	f, err := os.OpenFile(part, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		return outcome{code: protocol.ErrIOError, message: err.Error()}, false
	}
	total := int64(resp.TotalSize)
	nNew := (total + bs - 1) / bs
	var written, literal int64
	rbuf := make([]byte, bs)
	failClose := func(o outcome) (outcome, bool) { f.Close(); return o, false }
	for i := int64(0); i < nNew; i++ {
		bl := bs
		if total-i*bs < bl {
			bl = total - i*bs
		}
		bt, data, rerr := conn.ReadMsg()
		if rerr != nil {
			return failClose(outcome{dropConn: true, code: protocol.ErrIOError, message: rerr.Error()})
		}
		switch bt {
		case protocol.MsgBlockKeep:
			n, _ := oldF.ReadAt(rbuf[:bl], i*bs)
			if int64(n) != bl {
				return failClose(outcome{code: protocol.ErrIOError, message: "чтение старого блока"})
			}
			if _, werr := f.Write(rbuf[:bl]); werr != nil {
				return failClose(outcome{code: protocol.ErrIOError, message: werr.Error()})
			}
			written += bl
		case protocol.MsgFileData:
			if int64(len(data)) != bl {
				return failClose(outcome{dropConn: true, code: protocol.ErrProtocol, message: "размер литерального блока"})
			}
			if err := m.lim.wait(ctx, len(data)); err != nil {
				return failClose(outcome{dropConn: true, code: protocol.ErrIOError, message: "отменено"})
			}
			if _, werr := f.Write(data); werr != nil {
				return failClose(outcome{code: protocol.ErrIOError, message: werr.Error()})
			}
			written += bl
			literal += bl
		case protocol.MsgError:
			em, _ := protocol.ParseErrorMsg(data)
			return failClose(outcome{code: em.Code, message: em.Message})
		default:
			return failClose(outcome{dropConn: true, code: protocol.ErrProtocol, message: "неожиданный кадр " + protocol.MsgName(bt)})
		}
	}
	mtEnd, _, eerr := conn.ReadMsg()
	if eerr != nil {
		return failClose(outcome{dropConn: true, code: protocol.ErrIOError, message: eerr.Error()})
	}
	if mtEnd != protocol.MsgFileEnd {
		return failClose(outcome{dropConn: true, code: protocol.ErrProtocol, message: "ожидался FILE_END"})
	}
	if err := f.Sync(); err != nil {
		return failClose(outcome{code: protocol.ErrIOError, message: err.Error()})
	}
	f.Close()

	st, serr := os.Stat(part)
	if serr != nil || st.Size() != total {
		os.Remove(part)
		return outcome{code: protocol.ErrSizeMismatch, message: "размер после дельты не совпал"}, false
	}
	if err := os.Rename(part, oldPath); err != nil {
		return outcome{code: protocol.ErrIOError, message: "rename: " + err.Error()}, false
	}
	_ = os.Chmod(oldPath, m.d.Cfg.SaveFilePerms)
	tm := time.Unix(0, resp.Mtime)
	_ = os.Chtimes(oldPath, tm, tm)

	now := time.Now().UnixNano()
	if err := m.d.Store.UpsertFile(protocol.CleanRel(rel), protocol.NormKey(rel), total, resp.Mtime, now); err != nil {
		return outcome{code: protocol.ErrIOError, message: "индекс: " + err.Error()}, false
	}
	_ = m.d.Store.MarkTaskDone(t.ID, now)
	saved := total - literal
	if saved < 0 {
		saved = 0
	}
	m.d.Log.Info("transfer", "обновлён (delta) %s: передано %d из %d Б (сэкономлено %d)", rel, literal, total, saved)
	m.d.Log.Audit("change", rel, total, "ok", t.Attempts+1, cycleID)
	return outcome{ok: true, bytes: literal}, false
}
