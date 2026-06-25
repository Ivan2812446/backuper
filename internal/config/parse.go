// Package config (parse.go) — разбор значений .env: длительности, размеры,
// права доступа, версии TLS, булевы и числовые поля (раздел 18 ТЗ).
package config

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// loadEnvFile читает KEY=VALUE из файла. Пустые строки и строки с '#' игнорируются.
// Окружающие кавычки снимаются. Значения из реального окружения процесса имеют
// приоритет (важно для systemd EnvironmentFile).
func loadEnvFile(path string) (map[string]string, error) {
	m := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		val = strings.Trim(val, `"'`)
		m[key] = val
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	// окружение процесса перекрывает файл
	for _, kv := range os.Environ() {
		if eq := strings.IndexByte(kv, '='); eq > 0 {
			k := kv[:eq]
			if _, ok := m[k]; ok {
				m[k] = kv[eq+1:]
			}
		}
	}
	return m, nil
}

// getter накапливает ошибки разбора, чтобы выдать их пачкой.
type getter struct {
	m    map[string]string
	errs []string
}

func (g *getter) str(key, def string) string {
	if v, ok := g.m[key]; ok && v != "" {
		return v
	}
	return def
}

func (g *getter) required(key string) string {
	v := strings.TrimSpace(g.m[key])
	if v == "" {
		g.errs = append(g.errs, fmt.Sprintf("обязательный параметр %s не задан", key))
	}
	return v
}

func (g *getter) intv(key string, def int) int {
	v := g.str(key, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		g.errs = append(g.errs, fmt.Sprintf("%s: некорректное число %q", key, v))
		return def
	}
	return n
}

func (g *getter) dur(key string, def time.Duration) time.Duration {
	v := g.str(key, "")
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		g.errs = append(g.errs, fmt.Sprintf("%s: некорректная длительность %q (пример: 30s, 5m, 1h)", key, v))
		return def
	}
	return d
}

func (g *getter) size(key string, def int64) int64 {
	v := g.str(key, "")
	if v == "" {
		return def
	}
	n, err := parseSize(v)
	if err != nil {
		g.errs = append(g.errs, fmt.Sprintf("%s: %v", key, err))
		return def
	}
	return n
}

func (g *getter) perms(key string, def uint32) uint32 {
	v := g.str(key, "")
	if v == "" {
		return def
	}
	n, err := strconv.ParseUint(strings.TrimPrefix(v, "0o"), 8, 32)
	if err != nil {
		g.errs = append(g.errs, fmt.Sprintf("%s: некорректные права %q (пример: 0666)", key, v))
		return def
	}
	return uint32(n)
}

func (g *getter) tlsVersion(key string) uint16 {
	v := g.str(key, "1.2")
	switch strings.TrimSpace(v) {
	case "1.0":
		return tls.VersionTLS10
	case "1.1":
		return tls.VersionTLS11
	case "1.2", "":
		return tls.VersionTLS12
	case "1.3":
		return tls.VersionTLS13
	default:
		g.errs = append(g.errs, fmt.Sprintf("%s: неизвестная версия TLS %q", key, v))
		return tls.VersionTLS12
	}
}

// parseSize разбирает размеры: 1MiB, 100MB, 1024, KiB/MiB/GiB и KB/MB/GB.
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("пустой размер")
	}
	upper := strings.ToUpper(s)
	type unit struct {
		suffix string
		mult   int64
	}
	units := []unit{
		{"KIB", 1 << 10}, {"MIB", 1 << 20}, {"GIB", 1 << 30}, {"TIB", 1 << 40},
		{"KB", 1000}, {"MB", 1000 * 1000}, {"GB", 1000 * 1000 * 1000}, {"TB", 1000 * 1000 * 1000 * 1000},
		{"B", 1},
	}
	for _, u := range units {
		if strings.HasSuffix(upper, u.suffix) {
			num := strings.TrimSpace(upper[:len(upper)-len(u.suffix)])
			f, err := strconv.ParseFloat(num, 64)
			if err != nil {
				return 0, fmt.Errorf("некорректный размер %q", s)
			}
			return int64(f * float64(u.mult)), nil
		}
	}
	n, err := strconv.ParseInt(upper, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("некорректный размер %q (пример: 1MiB, 100MB, 1048576)", s)
	}
	return n, nil
}

// splitList разбивает список масок/адресов по запятой, отбрасывая пустые.
func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
