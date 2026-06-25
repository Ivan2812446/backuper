package integration

import (
	"bytes"
	"context"
	"crypto/tls"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"backuper/internal/certs"
	"backuper/internal/client"
	"backuper/internal/logx"
	"backuper/internal/protocol"
	"backuper/internal/store"
	"backuper/internal/tlsconn"
	"backuper/internal/transfer"
)

const (
	serverPW = "server-password-aaaaaaaaaaaaaaaaaaaa"
	clientPW = "client-password-bbbbbbbbbbbbbbbbbbbb"
)

type itEnv struct {
	base, backup, storage, temp string
	dialer                      *tlsconn.Dialer
	st                          *store.Store
	log                         *logx.Logger
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return p
}

func waitPort(t *testing.T, port int) {
	t.Helper()
	addr := net.JoinHostPort("127.0.0.1", itoa(port))
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("слушатель на порту %d не поднялся", port)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [12]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}

func setupIT(t *testing.T, wrongDialerPW bool) *itEnv {
	t.Helper()
	base := t.TempDir()
	backup := filepath.Join(base, "backup")
	storage := filepath.Join(base, "storage")
	temp := filepath.Join(base, "temp")
	certsDir := filepath.Join(base, "certs")
	for _, d := range []string{backup, storage, temp} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := certs.WriteAll(certsDir, []string{"127.0.0.1"}, []string{"127.0.0.1"}, 3650); err != nil {
		t.Fatal(err)
	}
	cf := func(n string) string { return filepath.Join(certsDir, n) }

	log, err := logx.New(logx.Options{Actor: "test", Dir: filepath.Join(base, "logs"), Console: false, Level: logx.LevelError})
	if err != nil {
		t.Fatal(err)
	}
	port := freePort(t)

	ltls, err := tlsconn.ListenerTLS(cf("client.crt"), cf("client.key"), cf("ca.crt"), tls.VersionTLS12)
	if err != nil {
		t.Fatal(err)
	}
	csrv := client.New(client.Config{
		ListenHost: "127.0.0.1", ListenPort: port, BackupDir: backup,
		ChunkSize: 32 << 10, IOTimeout: 10 * time.Second, MaxFrame: 16 << 20, MaxConnections: 8,
		SaveFilePerms: 0o644, SaveDirPerms: 0o755,
		OwnPassword: clientPW, PeerPassword: serverPW,
		RestoreTempDir: filepath.Join(base, "rtmp"), MtimeToleranceNS: int64(2 * time.Second),
	}, ltls, log)

	ctx, cancel := context.WithCancel(context.Background())
	go csrv.Run(ctx)
	t.Cleanup(cancel)
	waitPort(t, port)

	dtls, err := tlsconn.DialerTLS(cf("server.crt"), cf("server.key"), cf("ca.crt"), tls.VersionTLS12, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	ownPW := serverPW
	if wrongDialerPW {
		ownPW = "totally-wrong-password-zzzzzzzzzzzz"
	}
	dialer := &tlsconn.Dialer{
		Host: "127.0.0.1", Port: port, TLSConfig: dtls,
		ConnectTimeout: 5 * time.Second, IOTimeout: 10 * time.Second, MaxFrame: 16 << 20,
		OwnPassword: ownPW, PeerPassword: clientPW,
	}

	st, err := store.Open(filepath.Join(base, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	return &itEnv{base: base, backup: backup, storage: storage, temp: temp, dialer: dialer, st: st, log: log}
}

func seed(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// driveList выполняет LIST и загружает список клиента в client_files.
func driveList(t *testing.T, e *itEnv, conn *tlsconn.Conn) {
	t.Helper()
	if err := conn.WriteMsg(protocol.MsgListReq, protocol.ListReq{}.Encode()); err != nil {
		t.Fatal(err)
	}
	if err := e.st.ResetClientFiles(); err != nil {
		t.Fatal(err)
	}
	w, err := e.st.BeginClientFiles()
	if err != nil {
		t.Fatal(err)
	}
	for {
		p, err := conn.ReadExpect(protocol.MsgListBatch)
		if err != nil {
			t.Fatal(err)
		}
		b, err := protocol.ParseListBatch(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, fe := range b.Entries {
			rel := protocol.CleanRel(fe.Path)
			w.Add(rel, protocol.NormKey(rel), int64(fe.Size), fe.Mtime)
		}
		if b.IsLast {
			break
		}
	}
	if err := w.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestHandshakeAndDisk(t *testing.T) {
	e := setupIT(t, false)
	conn, err := e.dialer.Dial()
	if err != nil {
		t.Fatalf("Dial (handshake/mTLS/пароли): %v", err)
	}
	defer conn.Close()

	if err := conn.WriteMsg(protocol.MsgDiskReq, nil); err != nil {
		t.Fatal(err)
	}
	p, err := conn.ReadExpect(protocol.MsgDiskResp)
	if err != nil {
		t.Fatal(err)
	}
	dr, err := protocol.ParseDiskResp(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(dr.Mounts) != 1 || dr.Mounts[0].Total == 0 {
		t.Fatalf("DISK_RESP некорректен: %+v", dr)
	}
}

func TestAuthFailure(t *testing.T) {
	e := setupIT(t, true) // неверный пароль инициатора
	if _, err := e.dialer.Dial(); err == nil {
		t.Fatal("ожидался отказ аутентификации (AUTH_FAILED)")
	} else if pe, ok := err.(*protocol.ProtoError); ok && pe.Code != protocol.ErrAuthFailed {
		t.Fatalf("ожидался AUTH_FAILED, получено %v", err)
	}
}

func TestListDownloadIntegrity(t *testing.T) {
	e := setupIT(t, false)
	seed(t, e.backup, "docs/a.txt", "alpha content")
	seed(t, e.backup, "docs/sub/b.bin", string(bytes.Repeat([]byte("X"), 100000)))
	seed(t, e.backup, "root.txt", "root")

	conn, err := e.dialer.Dial()
	if err != nil {
		t.Fatal(err)
	}
	driveList(t, e, conn)
	conn.Close()

	tol := int64(2 * time.Second)
	enq, err := e.st.EnqueueDiffDownloads(1, tol, time.Now().UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	if enq != 3 {
		t.Fatalf("в очередь поставлено %d, want 3", enq)
	}

	mgr := transfer.New(transfer.Deps{
		Store: e.st, Log: e.log,
		NewConn: func() (*tlsconn.Conn, error) { return e.dialer.Dial() },
		Cfg: transfer.Config{
			StorageDir: e.storage, TempDir: e.temp, Parallel: 3, ChunkSize: 32 << 10,
			RetryCount: 2, RetryDelay: 100 * time.Millisecond,
			SaveFilePerms: 0o644, SaveDirPerms: 0o755, MtimeToleranceNS: tol,
		},
	})
	res, err := mgr.RunDownloadQueue(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.DownloadedFiles != 3 || res.SkippedFiles != 0 {
		t.Fatalf("скачано=%d пропущено=%d, want 3/0 (ошибки: %+v)", res.DownloadedFiles, res.SkippedFiles, res.Errors)
	}
	// целостность
	for _, rel := range []string{"docs/a.txt", "docs/sub/b.bin", "root.txt"} {
		want, _ := os.ReadFile(filepath.Join(e.backup, filepath.FromSlash(rel)))
		got, err := os.ReadFile(filepath.Join(e.storage, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("нет файла в хранилище %s: %v", rel, err)
		}
		if !bytes.Equal(want, got) {
			t.Fatalf("содержимое %s не совпадает", rel)
		}
	}
	if n, _ := e.st.CountFiles(); n != 3 {
		t.Fatalf("индекс=%d, want 3", n)
	}
	// идемпотентность: повторный дифф не ставит задач
	conn2, _ := e.dialer.Dial()
	driveList(t, e, conn2)
	conn2.Close()
	enq2, _ := e.st.EnqueueDiffDownloads(2, tol, time.Now().UnixNano())
	if enq2 != 0 {
		t.Fatalf("повторный enqueue=%d, want 0", enq2)
	}
}

func readGet(t *testing.T, conn *tlsconn.Conn, path string, offset uint64) ([]byte, protocol.GetResp) {
	t.Helper()
	if err := conn.WriteMsg(protocol.MsgGetReq, protocol.GetReq{Path: path, Offset: offset}.Encode()); err != nil {
		t.Fatal(err)
	}
	p, err := conn.ReadExpect(protocol.MsgGetResp)
	if err != nil {
		t.Fatal(err)
	}
	resp, _ := protocol.ParseGetResp(p)
	var buf bytes.Buffer
	for {
		mt, data, err := conn.ReadMsg()
		if err != nil {
			t.Fatal(err)
		}
		if mt == protocol.MsgFileData {
			buf.Write(data)
		} else if mt == protocol.MsgFileEnd {
			break
		} else {
			t.Fatalf("неожиданный кадр %s", protocol.MsgName(mt))
		}
	}
	return buf.Bytes(), resp
}

func TestGetWithOffset(t *testing.T) {
	e := setupIT(t, false)
	content := "0123456789ABCDEFGHIJ"
	seed(t, e.backup, "f.txt", content)
	conn, err := e.dialer.Dial()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// полный файл
	full, resp := readGet(t, conn, "f.txt", 0)
	if resp.TotalSize != uint64(len(content)) || string(full) != content {
		t.Fatalf("полный GET неверен: total=%d got=%q", resp.TotalSize, full)
	}
	// с offset = 10 -> хвост
	tail, _ := readGet(t, conn, "f.txt", 10)
	if string(tail) != content[10:] {
		t.Fatalf("GET offset неверен: got %q, want %q", tail, content[10:])
	}
}

func TestGetNotFoundAndTraversal(t *testing.T) {
	e := setupIT(t, false)
	conn, err := e.dialer.Dial()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	conn.WriteMsg(protocol.MsgGetReq, protocol.GetReq{Path: "no/such.txt"}.Encode())
	_, err = conn.ReadExpect(protocol.MsgGetResp)
	if pe, ok := err.(*protocol.ProtoError); !ok || pe.Code != protocol.ErrNotFound {
		t.Fatalf("ожидался NOT_FOUND, получено %v", err)
	}

	conn.WriteMsg(protocol.MsgGetReq, protocol.GetReq{Path: "../../etc/passwd"}.Encode())
	_, err = conn.ReadExpect(protocol.MsgGetResp)
	if pe, ok := err.(*protocol.ProtoError); !ok || pe.Code != protocol.ErrProtocol {
		t.Fatalf("ожидался отказ обхода каталога (PROTOCOL_ERROR), получено %v", err)
	}
}

func TestRestorePUT(t *testing.T) {
	e := setupIT(t, false)
	content := bytes.Repeat([]byte("restore-data\n"), 5000)
	conn, err := e.dialer.Dial()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	rel := "restored/file.dat"
	mtime := time.Now().Add(-time.Hour).UnixNano()
	if err := conn.WriteMsg(protocol.MsgPutReq, protocol.PutReq{
		Path: rel, TotalSize: uint64(len(content)), Offset: 0, Mtime: mtime,
	}.Encode()); err != nil {
		t.Fatal(err)
	}
	p, err := conn.ReadExpect(protocol.MsgPutResp)
	if err != nil {
		t.Fatal(err)
	}
	pr, _ := protocol.ParsePutResp(p)
	resume := pr.ResumeOffset

	// отправляем данные чанками от resume
	const chunk = 32 << 10
	for off := int(resume); off < len(content); off += chunk {
		end := off + chunk
		if end > len(content) {
			end = len(content)
		}
		if err := conn.WriteMsg(protocol.MsgFileData, content[off:end]); err != nil {
			t.Fatal(err)
		}
	}
	if err := conn.WriteMsg(protocol.MsgFileEnd, nil); err != nil {
		t.Fatal(err)
	}
	// финальное подтверждение
	fp, err := conn.ReadExpect(protocol.MsgPutResp)
	if err != nil {
		t.Fatal(err)
	}
	fr, _ := protocol.ParsePutResp(fp)
	if fr.Status != 0 {
		t.Fatalf("restore финальный status=%d, want 0", fr.Status)
	}

	got, err := os.ReadFile(filepath.Join(e.backup, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("восстановленный файл не найден: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("содержимое восстановленного файла не совпадает (%d/%d байт)", len(got), len(content))
	}
}
