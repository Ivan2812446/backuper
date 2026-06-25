package alert

import (
	"bufio"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"backuper/internal/logx"
)

func testLogger(t *testing.T) *logx.Logger {
	t.Helper()
	l, err := logx.New(logx.Options{Actor: "test", Dir: t.TempDir(), Console: false, Level: logx.LevelError})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func sampleReport() CycleReport {
	now := time.Now()
	return CycleReport{
		CycleID: 42, Status: "PARTIAL",
		StartedAt: now.Add(-2 * time.Minute), FinishedAt: now,
		DownloadedFiles: 7, DownloadedBytes: 3 << 20, ChangedFiles: 2,
		TrashedFiles: 1, PurgedFiles: 3, SkippedFiles: 1, Passes: 2,
		AvgSpeed: 1 << 20, PeakParallel: 4,
		ServerDisk: Disk{Name: "srv", Total: 100, Free: 5}, // 95% занято
		ClientDisk: Disk{Name: "cli", Total: 200, Free: 100},
		Skipped:    []SkippedItem{{Relpath: "x/y.bin", Reason: "SIZE_MISMATCH", Attempts: 3}},
		Errors:     []ErrorItem{{Code: "IO_ERROR", Relpath: "a.txt", Message: "boom"}},
		Notes:      []string{"[WARN] массовое удаление"},
	}
}

func TestReportRendering(t *testing.T) {
	r := sampleReport()

	subj := r.Subject()
	for _, want := range []string{"#42", "PARTIAL", "скачано 7"} {
		if !strings.Contains(subj, want) {
			t.Errorf("в теме нет %q: %s", want, subj)
		}
	}

	html := r.HTML()
	for _, want := range []string{"PARTIAL", "x/y.bin", "SIZE_MISMATCH", "IO_ERROR", "a.txt", "массовое удаление", "<table"} {
		if !strings.Contains(html, want) {
			t.Errorf("в HTML нет %q", want)
		}
	}
	// проверим, что свободно% дисков отрендерено
	if !strings.Contains(html, "5.0%") {
		t.Errorf("в HTML нет свободного %% диска сервера: %s", html)
	}

	text := r.Text()
	for _, want := range []string{"#42", "PARTIAL", "x/y.bin", "a.txt"} {
		if !strings.Contains(text, want) {
			t.Errorf("в тексте нет %q", want)
		}
	}
}

// mockSMTP принимает одно письмо и возвращает его тело.
func mockSMTP(t *testing.T) (addr string, got chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	got = make(chan string, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				w := func(s string) { c.Write([]byte(s)) }
				w("220 mock ESMTP\r\n")
				var body strings.Builder
				inData := false
				for {
					line, err := br.ReadString('\n')
					if err != nil {
						return
					}
					if inData {
						if line == ".\r\n" {
							inData = false
							got <- body.String()
							w("250 OK\r\n")
							continue
						}
						body.WriteString(line)
						continue
					}
					up := strings.ToUpper(line)
					switch {
					case strings.HasPrefix(up, "EHLO"), strings.HasPrefix(up, "HELO"):
						w("250 mock\r\n")
					case strings.HasPrefix(up, "MAIL"), strings.HasPrefix(up, "RCPT"):
						w("250 OK\r\n")
					case strings.HasPrefix(up, "DATA"):
						w("354 end with .\r\n")
						inData = true
					case strings.HasPrefix(up, "QUIT"):
						w("221 bye\r\n")
						return
					default:
						w("250 OK\r\n")
					}
				}
			}(c)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().String(), got
}

func TestSendCycleReportViaMockSMTP(t *testing.T) {
	addr, got := mockSMTP(t)
	host, portStr, _ := net.SplitHostPort(addr)
	port := 0
	for _, ch := range portStr {
		port = port*10 + int(ch-'0')
	}
	m := New(testLogger(t), SMTPConfig{
		Host: host, Port: port, From: "from@b.c", To: []string{"to@b.c"}, Security: "none",
	}, time.UTC, time.Minute)

	if err := m.SendCycleReport(sampleReport()); err != nil {
		t.Fatalf("SendCycleReport: %v", err)
	}
	select {
	case body := <-got:
		if !strings.Contains(body, "multipart/alternative") {
			t.Errorf("письмо не multipart/alternative: %s", body[:min(200, len(body))])
		}
		if !strings.Contains(body, "text/html") || !strings.Contains(body, "text/plain") {
			t.Error("в письме нет обеих частей text/plain и text/html")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("письмо не получено mock-SMTP")
	}
}

func TestSendImmediateAggregationWindow(t *testing.T) {
	addr, got := mockSMTP(t)
	host, portStr, _ := net.SplitHostPort(addr)
	port := 0
	for _, ch := range portStr {
		port = port*10 + int(ch-'0')
	}
	m := New(testLogger(t), SMTPConfig{
		Host: host, Port: port, From: "f@x", To: []string{"t@x"}, Security: "none",
	}, time.UTC, time.Hour) // большое окно

	if err := m.SendImmediate("disk", "Диск переполнен", []string{"line"}); err != nil {
		t.Fatal(err)
	}
	// второй того же типа в окне — подавляется (письмо не уходит)
	if err := m.SendImmediate("disk", "Диск переполнен снова", []string{"line2"}); err != nil {
		t.Fatal(err)
	}

	count := 0
	var mu sync.Mutex
	deadline := time.After(1 * time.Second)
loop:
	for {
		select {
		case <-got:
			mu.Lock()
			count++
			mu.Unlock()
		case <-deadline:
			break loop
		}
	}
	if count != 1 {
		t.Fatalf("ожидалось ровно 1 письмо (второе подавлено окном), получено %d", count)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
