package logx

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := map[string]Level{
		"":      LevelInfo,
		"info":  LevelInfo,
		"DEBUG": LevelDebug,
		"warn":  LevelWarn,
		"ERROR": LevelError,
	}
	for in, want := range cases {
		got, err := ParseLevel(in)
		if err != nil || got != want {
			t.Errorf("ParseLevel(%q)=%v err=%v, want %v", in, got, err, want)
		}
	}
	if _, err := ParseLevel("verbose"); err == nil {
		t.Error("неизвестный уровень должен давать ошибку")
	}
}

func TestLevelFiltering(t *testing.T) {
	dir := t.TempDir()
	l, err := New(Options{Actor: "server", Dir: dir, FileName: "x.log", Level: LevelInfo, Console: false})
	if err != nil {
		t.Fatal(err)
	}
	l.Debug("c", "debug-line")
	l.Info("c", "info-line")
	data, _ := os.ReadFile(filepath.Join(dir, "x.log"))
	s := string(data)
	if strings.Contains(s, "debug-line") {
		t.Error("DEBUG не должен попадать в лог уровня INFO")
	}
	if !strings.Contains(s, "info-line") {
		t.Error("INFO должен быть в логе")
	}
}

func TestAuditJSON(t *testing.T) {
	dir := t.TempDir()
	audit := filepath.Join(dir, "audit.jsonl")
	l, err := New(Options{Actor: "server", Dir: dir, AuditFile: audit, Level: LevelInfo, Console: false})
	if err != nil {
		t.Fatal(err)
	}
	l.Audit("download", "docs/a.txt", 1048576, "ok", 1, 42)
	data, err := os.ReadFile(audit)
	if err != nil {
		t.Fatal(err)
	}
	var e AuditEntry
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &e); err != nil {
		t.Fatalf("аудит не JSON: %v (%s)", err, data)
	}
	if e.Op != "download" || e.Relpath != "docs/a.txt" || e.Size != 1048576 || e.Result != "ok" || e.Attempt != 1 || e.Cycle != 42 || e.Actor != "server" {
		t.Fatalf("поля аудита неверны: %+v", e)
	}
	if e.Ts == "" {
		t.Error("ts пуст")
	}
}

func TestJSONMainFormat(t *testing.T) {
	dir := t.TempDir()
	l, _ := New(Options{Actor: "client", Dir: dir, FileName: "j.log", Level: LevelInfo, Format: "json", Console: false})
	l.Info("comp", "hello %d", 7)
	data, _ := os.ReadFile(filepath.Join(dir, "j.log"))
	var m map[string]string
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &m); err != nil {
		t.Fatalf("основной лог не JSON: %v (%s)", err, data)
	}
	if m["msg"] != "hello 7" || m["level"] != "INFO" || m["component"] != "comp" || m["actor"] != "client" {
		t.Fatalf("JSON-поля неверны: %v", m)
	}
}
