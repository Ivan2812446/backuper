package protocol

import (
	"path/filepath"
	"testing"
)

func TestCleanRel(t *testing.T) {
	// На Linux '\' — обычный символ имени; кросс-ОС нормализацию '\'→'/'
	// делает сканер клиента (ToRel) на своей ОС, поэтому здесь не проверяется.
	cases := map[string]string{
		"/a/b":  "a/b",
		"a/./b": "a/b",
		"a//b":  "a/b",
		"":      "",
		"a/b/":  "a/b",
	}
	for in, want := range cases {
		if got := CleanRel(in); got != want {
			t.Errorf("CleanRel(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestNormKey(t *testing.T) {
	if NormKey("Docs/Scan_001.PDF") != "docs/scan_001.pdf" {
		t.Fatalf("NormKey case fold failed: %q", NormKey("Docs/Scan_001.PDF"))
	}
	if NormKey("A/B") != NormKey("a/b") {
		t.Fatal("case-insensitive equality failed")
	}
}

func TestSafeJoinValid(t *testing.T) {
	root := t.TempDir()
	full, err := SafeJoin(root, "docs/sub/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "docs", "sub", "file.txt")
	if full != want {
		t.Fatalf("SafeJoin=%q, want %q", full, want)
	}
}

func TestSafeJoinEscapes(t *testing.T) {
	root := t.TempDir()
	bad := []string{
		"../escape",
		"../../etc/passwd",
		"a/../../b",
	}
	for _, p := range bad {
		if _, err := SafeJoin(root, p); err == nil {
			t.Errorf("SafeJoin(%q) должен отклоняться", p)
		}
	}
}

func TestSafeJoinAbsoluteCoercedInsideRoot(t *testing.T) {
	root := t.TempDir()
	// ведущий слэш срезается -> путь становится относительным внутри корня (безопасно)
	full, err := SafeJoin(root, "/etc/passwd")
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if full != filepath.Join(root, "etc", "passwd") {
		t.Fatalf("got %q", full)
	}
}

func TestSafeJoinInternalDotDotResolved(t *testing.T) {
	root := t.TempDir()
	// a/b/../c -> a/c, остаётся внутри корня
	full, err := SafeJoin(root, "a/b/../c")
	if err != nil {
		t.Fatal(err)
	}
	if full != filepath.Join(root, "a", "c") {
		t.Fatalf("got %q", full)
	}
}
