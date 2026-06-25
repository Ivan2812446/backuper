// Package protocol (paths.go) — правила относительных путей (раздел 5.6 ТЗ):
// нормализация relpath к '/', ключ сравнения (нижний регистр), безопасное
// объединение с корнем (запрет выхода за корень через '..').
package protocol

import (
	"path"
	"path/filepath"
	"strings"
)

// CleanRel приводит относительный путь к каноническому виду: разделитель '/',
// без ведущего слэша, без '.'/'..'-сегментов (сохраняя оригинальный регистр).
func CleanRel(rel string) string {
	rel = filepath.ToSlash(rel)
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		return ""
	}
	return path.Clean(rel)
}

// NormKey — ключ сравнения путей: CleanRel в нижнем регистре (5.6,
// минимальная чувствительность к регистру для связки Windows↔Linux).
func NormKey(rel string) string {
	return strings.ToLower(CleanRel(rel))
}

// ToRel вычисляет relpath файла относительно корня (для сканера).
func ToRel(root, full string) (string, error) {
	r, err := filepath.Rel(root, full)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(r), nil
}

// SafeJoin объединяет корень и relpath, гарантируя, что результат не выходит
// за пределы корня (защита от '..', 5.6). Возвращает абсолютный путь ФС.
func SafeJoin(root, rel string) (string, error) {
	clean := CleanRel(rel)
	if clean == "" {
		return "", Errorf(ErrProtocol, "пустой относительный путь")
	}
	if strings.HasPrefix(clean, "../") || clean == ".." || filepath.IsAbs(filepath.FromSlash(clean)) {
		return "", Errorf(ErrProtocol, "путь выходит за корень: %q", rel)
	}
	full := filepath.Join(root, filepath.FromSlash(clean))
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	fullAbs, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	if fullAbs != rootAbs && !strings.HasPrefix(fullAbs, rootAbs+string(filepath.Separator)) {
		return "", Errorf(ErrProtocol, "путь выходит за корень: %q", rel)
	}
	return full, nil
}
