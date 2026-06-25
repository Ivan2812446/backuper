package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseSize(t *testing.T) {
	cases := map[string]int64{
		"1MiB":    1 << 20,
		"1KiB":    1 << 10,
		"100MB":   100_000_000,
		"1GB":     1_000_000_000,
		"1048576": 1048576,
		"0":       0,
		"2 KiB":   2048,
	}
	for in, want := range cases {
		got, err := parseSize(in)
		if err != nil {
			t.Errorf("parseSize(%q) err %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseSize(%q)=%d, want %d", in, got, want)
		}
	}
	if _, err := parseSize("abc"); err == nil {
		t.Error("parseSize(abc) должна вернуть ошибку")
	}
}

func writeEnv(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const minimalServerEnv = `
CLIENT_HOST=10.0.0.2
TLS_CERT_FILE=/tmp/x.crt
TLS_KEY_FILE=/tmp/x.key
TLS_CA_FILE=/tmp/ca.crt
OWN_PASSWORD=server-password-aaaaaaaaaaaaaaaaaaaa
PEER_PASSWORD=client-password-aaaaaaaaaaaaaaaaaaaa
STORAGE_DIR=/tmp/storage
TRASH_DIR=/tmp/trash
TEMP_DIR=/tmp/temp
SMTP_HOST=smtp.local
SMTP_FROM=a@b.c
SMTP_TO=x@y.z,q@w.e
`

func TestLoadServerDefaults(t *testing.T) {
	cfg, err := LoadServer(writeEnv(t, minimalServerEnv))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.parseProblems) != 0 {
		t.Fatalf("неожиданные проблемы разбора: %v", cfg.parseProblems)
	}
	if cfg.ClientPort != 9000 {
		t.Errorf("ClientPort=%d, want 9000", cfg.ClientPort)
	}
	if cfg.SyncInterval != time.Hour {
		t.Errorf("SyncInterval=%v, want 1h", cfg.SyncInterval)
	}
	if cfg.ChunkSize != 1<<20 {
		t.Errorf("ChunkSize=%d, want 1MiB", cfg.ChunkSize)
	}
	if cfg.SyncMaxPasses != 3 || cfg.ParallelTransfers != 4 || cfg.RetryCount != 3 {
		t.Errorf("дефолты passes/parallel/retry неверны: %d/%d/%d", cfg.SyncMaxPasses, cfg.ParallelTransfers, cfg.RetryCount)
	}
	if cfg.MtimeTolerance != 2*time.Second {
		t.Errorf("MtimeTolerance=%v", cfg.MtimeTolerance)
	}
	if cfg.SaveFilePerms != 0o666 || cfg.SaveDirPerms != 0o777 {
		t.Errorf("perms %o/%o", cfg.SaveFilePerms, cfg.SaveDirPerms)
	}
	if cfg.MaxFrameBytes != 16<<20 {
		t.Errorf("MaxFrameBytes=%d", cfg.MaxFrameBytes)
	}
	if len(cfg.SMTPTo) != 2 {
		t.Errorf("SMTPTo=%v", cfg.SMTPTo)
	}
	if cfg.Loc == nil || cfg.Timezone != "Europe/Moscow" {
		t.Errorf("timezone не загружен: %v %q", cfg.Loc, cfg.Timezone)
	}
}

func TestLoadServerMissingRequired(t *testing.T) {
	cfg, err := LoadServer(writeEnv(t, "CLIENT_PORT=9000\n"))
	if err != nil {
		t.Fatal(err)
	}
	probs := cfg.Validate()
	if len(probs) == 0 {
		t.Fatal("ожидались проблемы для отсутствующих обязательных полей")
	}
	// должны быть упомянуты ключевые обязательные поля
	joined := ""
	for _, p := range probs {
		joined += p + "\n"
	}
	for _, want := range []string{"CLIENT_HOST", "OWN_PASSWORD", "STORAGE_DIR", "SMTP_HOST"} {
		if !contains(joined, want) {
			t.Errorf("в проблемах нет упоминания %s:\n%s", want, joined)
		}
	}
}

func TestLoadServerWithBOM(t *testing.T) {
	// .env с UTF-8 BOM (как пишет Windows PowerShell/Блокнот) должен читаться
	cfg, err := LoadServer(writeEnv(t, "\ufeff"+minimalServerEnv))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClientHost != "10.0.0.2" {
		t.Fatalf("BOM сломал первую переменную: ClientHost=%q", cfg.ClientHost)
	}
	if len(cfg.parseProblems) != 0 {
		t.Fatalf("проблемы при BOM: %v", cfg.parseProblems)
	}
}

func TestEnvOverride(t *testing.T) {
	t.Setenv("CLIENT_PORT", "12345")
	cfg, err := LoadServer(writeEnv(t, minimalServerEnv+"CLIENT_PORT=9000\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClientPort != 12345 {
		t.Fatalf("окружение должно перекрывать файл: got %d", cfg.ClientPort)
	}
}

func TestValidateOKWithRealFiles(t *testing.T) {
	dir := t.TempDir()
	crt := filepath.Join(dir, "c.crt")
	key := filepath.Join(dir, "c.key")
	ca := filepath.Join(dir, "ca.crt")
	for _, f := range []string{crt, key, ca} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env := "CLIENT_HOST=h\n" +
		"TLS_CERT_FILE=" + crt + "\nTLS_KEY_FILE=" + key + "\nTLS_CA_FILE=" + ca + "\n" +
		"OWN_PASSWORD=aaaaaaaaaaaaaaaaaaaaaaaa\nPEER_PASSWORD=bbbbbbbbbbbbbbbbbbbbbbbb\n" +
		"STORAGE_DIR=" + dir + "/s\nTRASH_DIR=" + dir + "/t\nTEMP_DIR=" + dir + "/tmp\n" +
		"STATE_DB=" + dir + "/state.db\n" +
		"SMTP_HOST=h\nSMTP_FROM=a@b.c\nSMTP_TO=x@y.z\n"
	cfg, err := LoadServer(writeEnv(t, env))
	if err != nil {
		t.Fatal(err)
	}
	if probs := cfg.Validate(); len(probs) != 0 {
		t.Fatalf("ожидалась валидная конфигурация, проблемы: %v", probs)
	}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	for _, d := range []string{cfg.StorageDir, cfg.TrashDir, cfg.TempDir} {
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			t.Errorf("каталог не создан: %s", d)
		}
	}
}

func TestLoadClient(t *testing.T) {
	dir := t.TempDir()
	env := "TLS_CERT_FILE=a\nTLS_KEY_FILE=b\nTLS_CA_FILE=c\n" +
		"OWN_PASSWORD=aaaaaaaaaaaaaaaaaaaaaaaa\nPEER_PASSWORD=bbbbbbbbbbbbbbbbbbbbbbbb\n" +
		"BACKUP_DIR=" + dir + "\nEXCLUDE_PATTERNS=*.tmp, *.lock\nMAX_CONNECTIONS=16\nCHUNK_SIZE=2MiB\n"
	cfg, err := LoadClient(writeEnv(t, env))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenPort != 9000 || cfg.ListenHost != "0.0.0.0" {
		t.Errorf("listen defaults: %s:%d", cfg.ListenHost, cfg.ListenPort)
	}
	if cfg.MaxConnections != 16 || cfg.ChunkSize != 2<<20 {
		t.Errorf("maxconn/chunk: %d/%d", cfg.MaxConnections, cfg.ChunkSize)
	}
	if len(cfg.ExcludePatterns) != 2 {
		t.Errorf("exclude patterns: %v", cfg.ExcludePatterns)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (indexOf(haystack, needle) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
