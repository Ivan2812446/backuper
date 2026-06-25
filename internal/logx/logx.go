// Package logx — логирование и аудит (раздел 16 ТЗ).
// Основной лог: человекочитаемый текст или JSON, ротация по размеру (lumberjack).
// Аудит-лог: JSON по строке на операцию с файлом.
package logx

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	lumberjack "gopkg.in/natefinch/lumberjack.v2"
)

// Level — уровень логирования.
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	default:
		return "INFO"
	}
}

// ParseLevel разбирает строковый уровень из .env.
func ParseLevel(s string) (Level, error) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "", "INFO":
		return LevelInfo, nil
	case "DEBUG":
		return LevelDebug, nil
	case "WARN", "WARNING":
		return LevelWarn, nil
	case "ERROR":
		return LevelError, nil
	default:
		return LevelInfo, fmt.Errorf("неизвестный LOG_LEVEL %q", s)
	}
}

// Options — параметры логгера (из конфигурации).
type Options struct {
	Actor      string // "server" | "client"
	Dir        string // LOG_DIR (если пусто — только stderr)
	FileName   string // имя основного лог-файла
	AuditFile  string // полный путь к аудит-логу (если пусто — аудит выкл.)
	MaxSizeMB  int
	MaxBackups int
	MaxAgeDays int
	Level      Level
	Format     string // "text" | "json"
	Loc        *time.Location
	Console    bool // дублировать в stderr
}

// Logger — основной + аудит логгер.
type Logger struct {
	mu    sync.Mutex
	out   io.Writer
	audit io.Writer
	level Level
	json  bool
	loc   *time.Location
	actor string
}

// New создаёт логгер согласно Options.
func New(o Options) (*Logger, error) {
	loc := o.Loc
	if loc == nil {
		loc = time.UTC
	}
	l := &Logger{
		level: o.Level,
		json:  strings.EqualFold(o.Format, "json"),
		loc:   loc,
		actor: o.Actor,
	}

	var writers []io.Writer
	if o.Dir != "" {
		if err := os.MkdirAll(o.Dir, 0o755); err != nil {
			return nil, fmt.Errorf("создание LOG_DIR: %w", err)
		}
		name := o.FileName
		if name == "" {
			name = "backuper-" + o.Actor + ".log"
		}
		writers = append(writers, &lumberjack.Logger{
			Filename:   filepath.Join(o.Dir, name),
			MaxSize:    maxOr(o.MaxSizeMB, 100),
			MaxBackups: maxOr(o.MaxBackups, 7),
			MaxAge:     o.MaxAgeDays,
			Compress:   false,
		})
	}
	if o.Console || o.Dir == "" {
		writers = append(writers, os.Stderr)
	}
	if len(writers) == 1 {
		l.out = writers[0]
	} else {
		l.out = io.MultiWriter(writers...)
	}

	if o.AuditFile != "" {
		if dir := filepath.Dir(o.AuditFile); dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("создание каталога аудита: %w", err)
			}
		}
		l.audit = &lumberjack.Logger{
			Filename:   o.AuditFile,
			MaxSize:    maxOr(o.MaxSizeMB, 100),
			MaxBackups: maxOr(o.MaxBackups, 7),
			MaxAge:     o.MaxAgeDays,
			Compress:   false,
		}
	}
	return l, nil
}

func maxOr(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

func (l *Logger) write(level Level, component, msg string) {
	if level < l.level {
		return
	}
	now := time.Now().In(l.loc)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.json {
		rec := map[string]string{
			"ts":        now.Format(time.RFC3339),
			"level":     level.String(),
			"actor":     l.actor,
			"component": component,
			"msg":       msg,
		}
		b, _ := json.Marshal(rec)
		fmt.Fprintln(l.out, string(b))
		return
	}
	// формат: 2006-01-02 15:04:05 LEVEL component message
	fmt.Fprintf(l.out, "%s %-5s %-9s %s\n", now.Format("2006-01-02 15:04:05"), level.String(), component, msg)
}

func (l *Logger) Debug(component, format string, a ...any) {
	l.write(LevelDebug, component, fmt.Sprintf(format, a...))
}
func (l *Logger) Info(component, format string, a ...any) {
	l.write(LevelInfo, component, fmt.Sprintf(format, a...))
}
func (l *Logger) Warn(component, format string, a ...any) {
	l.write(LevelWarn, component, fmt.Sprintf(format, a...))
}
func (l *Logger) Error(component, format string, a ...any) {
	l.write(LevelError, component, fmt.Sprintf(format, a...))
}

// AuditEntry — запись аудит-лога (раздел 16).
type AuditEntry struct {
	Ts      string `json:"ts"`
	Actor   string `json:"actor"`
	Op      string `json:"op"` // download|change|trash|trash_purge|restore|skip
	Relpath string `json:"relpath"`
	Size    int64  `json:"size"`
	Result  string `json:"result"` // ok|error
	Attempt int    `json:"attempt"`
	Cycle   int64  `json:"cycle"`
}

// Audit пишет одну строку в аудит-лог (если он включён).
func (l *Logger) Audit(op, relpath string, size int64, result string, attempt int, cycle int64) {
	if l.audit == nil {
		return
	}
	e := AuditEntry{
		Ts:      time.Now().In(l.loc).Format(time.RFC3339),
		Actor:   l.actor,
		Op:      op,
		Relpath: relpath,
		Size:    size,
		Result:  result,
		Attempt: attempt,
		Cycle:   cycle,
	}
	b, _ := json.Marshal(e)
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintln(l.audit, string(b))
}
