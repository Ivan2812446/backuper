// Package config — загрузка и валидация конфигурации .env для сервера и клиента
// (раздел 18 ТЗ). Длительности (30s/5m/1h), размеры (1MiB/100MB), права (0666).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"backuper/internal/logx"
)

// ServerConfig — конфигурация сервера (18.1).
type ServerConfig struct {
	ClientHost string
	ClientPort int

	TLSCertFile   string
	TLSKeyFile    string
	TLSCAFile     string
	TLSMinVersion uint16

	OwnPassword  string
	PeerPassword string

	StorageDir string
	TrashDir   string
	TempDir    string

	TrashRetentionDays   int
	TrashCleanupInterval time.Duration

	SyncInterval  time.Duration
	SyncMaxPasses int

	ParallelTransfers int
	BandwidthLimit    int64
	ChunkSize         int64
	MaxFrameBytes     int64
	ListBatchSize     int

	RetryCount int
	RetryDelay time.Duration

	MtimeTolerance time.Duration

	ConnectTimeout      time.Duration
	IOTimeout           time.Duration
	HealthcheckInterval time.Duration

	SaveFilePerms uint32
	SaveDirPerms  uint32

	AlertMassDeleteThreshold int
	DiskAlertThreshold       int
	AlertAggregationWindow   time.Duration

	SMTPHost     string
	SMTPPort     int
	SMTPUser     string
	SMTPPassword string
	SMTPFrom     string
	SMTPTo       []string
	SMTPSecurity string // none|starttls|tls

	LogLevel    logx.Level
	LogDir      string
	LogMaxSize  int // MiB
	LogMaxFiles int
	LogMaxAge   int // дни
	LogFormat   string
	AuditLog    string

	StateDB  string
	LockFile string

	Timezone string
	Loc      *time.Location

	parseProblems []string
}

// ClientConfig — конфигурация клиента (18.2).
type ClientConfig struct {
	ListenHost string
	ListenPort int

	TLSCertFile   string
	TLSKeyFile    string
	TLSCAFile     string
	TLSMinVersion uint16

	OwnPassword  string
	PeerPassword string

	BackupDir string

	IncludePatterns []string
	ExcludePatterns []string

	BandwidthLimit int64
	ChunkSize      int64
	MaxFrameBytes  int64
	IOTimeout      time.Duration
	MaxConnections int

	SaveFilePerms uint32
	SaveDirPerms  uint32

	LogLevel    logx.Level
	LogDir      string
	LogMaxSize  int
	LogMaxFiles int
	LogMaxAge   int
	LogFormat   string
	AuditLog    string

	Timezone string
	Loc      *time.Location

	parseProblems []string
}

func loadLocation(name string, g *getter) (string, *time.Location) {
	if name == "" {
		name = "Europe/Moscow"
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		g.errs = append(g.errs, fmt.Sprintf("TIMEZONE %q: %v", name, err))
		loc = time.UTC
	}
	return name, loc
}

func parseLevel(g *getter, key string) logx.Level {
	lvl, err := logx.ParseLevel(g.str(key, "INFO"))
	if err != nil {
		g.errs = append(g.errs, err.Error())
	}
	return lvl
}

// LoadServer читает и разбирает .env сервера. Ошибка — только если файл не читается;
// проблемы разбора/валидации возвращает Validate().
func LoadServer(path string) (*ServerConfig, error) {
	m, err := loadEnvFile(path)
	if err != nil {
		return nil, fmt.Errorf("чтение %s: %w", path, err)
	}
	g := &getter{m: m}
	c := &ServerConfig{
		ClientHost: g.required("CLIENT_HOST"),
		ClientPort: g.intv("CLIENT_PORT", 9000),

		TLSCertFile:   g.required("TLS_CERT_FILE"),
		TLSKeyFile:    g.required("TLS_KEY_FILE"),
		TLSCAFile:     g.required("TLS_CA_FILE"),
		TLSMinVersion: g.tlsVersion("TLS_MIN_VERSION"),

		OwnPassword:  g.required("OWN_PASSWORD"),
		PeerPassword: g.required("PEER_PASSWORD"),

		StorageDir: g.required("STORAGE_DIR"),
		TrashDir:   g.required("TRASH_DIR"),
		TempDir:    g.required("TEMP_DIR"),

		TrashRetentionDays:   g.intv("TRASH_RETENTION_DAYS", 10),
		TrashCleanupInterval: g.dur("TRASH_CLEANUP_INTERVAL", 24*time.Hour),

		SyncInterval:  g.dur("SYNC_INTERVAL", time.Hour),
		SyncMaxPasses: g.intv("SYNC_MAX_PASSES", 3),

		ParallelTransfers: g.intv("PARALLEL_TRANSFERS", 4),
		BandwidthLimit:    g.size("BANDWIDTH_LIMIT", 0),
		ChunkSize:         g.size("CHUNK_SIZE", 1<<20),
		MaxFrameBytes:     g.size("MAX_FRAME_BYTES", 16<<20),
		ListBatchSize:     g.intv("LIST_BATCH_SIZE", 10000),

		RetryCount: g.intv("RETRY_COUNT", 3),
		RetryDelay: g.dur("RETRY_DELAY", time.Minute),

		MtimeTolerance: g.dur("MTIME_TOLERANCE", 2*time.Second),

		ConnectTimeout:      g.dur("CONNECT_TIMEOUT", 30*time.Second),
		IOTimeout:           g.dur("IO_TIMEOUT", 60*time.Second),
		HealthcheckInterval: g.dur("HEALTHCHECK_INTERVAL", 30*time.Second),

		SaveFilePerms: g.perms("SAVE_FILE_PERMS", 0o666),
		SaveDirPerms:  g.perms("SAVE_DIR_PERMS", 0o777),

		AlertMassDeleteThreshold: g.intv("ALERT_MASS_DELETE_THRESHOLD", 100),
		DiskAlertThreshold:       g.intv("DISK_ALERT_THRESHOLD", 90),
		AlertAggregationWindow:   g.dur("ALERT_AGGREGATION_WINDOW", 60*time.Second),

		SMTPHost:     g.required("SMTP_HOST"),
		SMTPPort:     g.intv("SMTP_PORT", 587),
		SMTPUser:     g.str("SMTP_USER", ""),
		SMTPPassword: g.str("SMTP_PASSWORD", ""),
		SMTPFrom:     g.required("SMTP_FROM"),
		SMTPTo:       splitList(g.required("SMTP_TO")),
		SMTPSecurity: strings.ToLower(g.str("SMTP_SECURITY", "starttls")),

		LogLevel:    parseLevel(g, "LOG_LEVEL"),
		LogDir:      g.str("LOG_DIR", ""),
		LogMaxSize:  g.intv("LOG_MAX_SIZE", 100),
		LogMaxFiles: g.intv("LOG_MAX_FILES", 7),
		LogMaxAge:   g.intv("LOG_MAX_AGE", 0),
		LogFormat:   strings.ToLower(g.str("LOG_FORMAT", "text")),
		AuditLog:    g.str("AUDIT_LOG", ""),

		StateDB:  g.str("STATE_DB", "/var/lib/backuper/state.db"),
		LockFile: g.str("LOCK_FILE", "/var/run/backuper.lock"),
	}
	c.Timezone, c.Loc = loadLocation(g.str("TIMEZONE", "Europe/Moscow"), g)
	c.parseProblems = g.errs
	return c, nil
}

// LoadClient читает и разбирает .env клиента.
func LoadClient(path string) (*ClientConfig, error) {
	m, err := loadEnvFile(path)
	if err != nil {
		return nil, fmt.Errorf("чтение %s: %w", path, err)
	}
	g := &getter{m: m}
	c := &ClientConfig{
		ListenHost: g.str("LISTEN_HOST", "0.0.0.0"),
		ListenPort: g.intv("LISTEN_PORT", 9000),

		TLSCertFile:   g.required("TLS_CERT_FILE"),
		TLSKeyFile:    g.required("TLS_KEY_FILE"),
		TLSCAFile:     g.required("TLS_CA_FILE"),
		TLSMinVersion: g.tlsVersion("TLS_MIN_VERSION"),

		OwnPassword:  g.required("OWN_PASSWORD"),
		PeerPassword: g.required("PEER_PASSWORD"),

		BackupDir: g.required("BACKUP_DIR"),

		IncludePatterns: splitList(g.str("INCLUDE_PATTERNS", "")),
		ExcludePatterns: splitList(g.str("EXCLUDE_PATTERNS", "")),

		BandwidthLimit: g.size("BANDWIDTH_LIMIT", 0),
		ChunkSize:      g.size("CHUNK_SIZE", 1<<20),
		MaxFrameBytes:  g.size("MAX_FRAME_BYTES", 16<<20),
		IOTimeout:      g.dur("IO_TIMEOUT", 60*time.Second),
		MaxConnections: g.intv("MAX_CONNECTIONS", 8),

		SaveFilePerms: g.perms("SAVE_FILE_PERMS", 0o666),
		SaveDirPerms:  g.perms("SAVE_DIR_PERMS", 0o777),

		LogLevel:    parseLevel(g, "LOG_LEVEL"),
		LogDir:      g.str("LOG_DIR", ""),
		LogMaxSize:  g.intv("LOG_MAX_SIZE", 100),
		LogMaxFiles: g.intv("LOG_MAX_FILES", 7),
		LogMaxAge:   g.intv("LOG_MAX_AGE", 0),
		LogFormat:   strings.ToLower(g.str("LOG_FORMAT", "text")),
		AuditLog:    g.str("AUDIT_LOG", ""),
	}
	c.Timezone, c.Loc = loadLocation(g.str("TIMEZONE", "Europe/Moscow"), g)
	c.parseProblems = g.errs
	return c, nil
}

// LogOptions конвертирует поля логов в logx.Options.
func (c *ServerConfig) LogOptions() logx.Options {
	return logx.Options{
		Actor: "server", Dir: c.LogDir, FileName: "backuper-server.log",
		AuditFile: c.AuditLog, MaxSizeMB: c.LogMaxSize, MaxBackups: c.LogMaxFiles,
		MaxAgeDays: c.LogMaxAge, Level: c.LogLevel, Format: c.LogFormat,
		Loc: c.Loc, Console: true,
	}
}

func (c *ClientConfig) LogOptions() logx.Options {
	return logx.Options{
		Actor: "client", Dir: c.LogDir, FileName: "backuper-client.log",
		AuditFile: c.AuditLog, MaxSizeMB: c.LogMaxSize, MaxBackups: c.LogMaxFiles,
		MaxAgeDays: c.LogMaxAge, Level: c.LogLevel, Format: c.LogFormat,
		Loc: c.Loc, Console: true,
	}
}

// --- валидация ---

func fileReadable(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return f.Close()
}

// dirUsable проверяет, что dir существует и доступен на запись, либо что его
// родитель существует (тогда dir можно создать через EnsureDirs).
func dirUsable(dir string) error {
	if dir == "" {
		return nil
	}
	st, err := os.Stat(dir)
	if err == nil {
		if !st.IsDir() {
			return fmt.Errorf("%s не является директорией", dir)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(dir)
	if _, err := os.Stat(parent); err != nil {
		return fmt.Errorf("ни %s, ни родитель %s не существуют", dir, parent)
	}
	return nil
}

// Validate возвращает список проблем конфигурации (для check-config).
func (c *ServerConfig) Validate() []string {
	probs := append([]string{}, c.parseProblems...)
	for _, f := range []struct{ name, path string }{
		{"TLS_CERT_FILE", c.TLSCertFile}, {"TLS_KEY_FILE", c.TLSKeyFile}, {"TLS_CA_FILE", c.TLSCAFile},
	} {
		if f.path == "" {
			continue
		}
		if err := fileReadable(f.path); err != nil {
			probs = append(probs, fmt.Sprintf("%s: %v", f.name, err))
		}
	}
	for _, d := range []struct{ name, path string }{
		{"STORAGE_DIR", c.StorageDir}, {"TRASH_DIR", c.TrashDir}, {"TEMP_DIR", c.TempDir},
		{"STATE_DB(dir)", filepath.Dir(c.StateDB)}, {"LOG_DIR", c.LogDir},
	} {
		if d.path == "" {
			continue
		}
		if err := dirUsable(d.path); err != nil {
			probs = append(probs, fmt.Sprintf("%s: %v", d.name, err))
		}
	}
	if c.ParallelTransfers < 1 {
		probs = append(probs, "PARALLEL_TRANSFERS должен быть ≥ 1")
	}
	if c.SyncMaxPasses < 1 {
		probs = append(probs, "SYNC_MAX_PASSES должен быть ≥ 1")
	}
	if c.ChunkSize <= 0 {
		probs = append(probs, "CHUNK_SIZE должен быть > 0")
	}
	if c.RetryCount < 1 {
		probs = append(probs, "RETRY_COUNT должен быть ≥ 1")
	}
	switch c.SMTPSecurity {
	case "none", "starttls", "tls":
	default:
		probs = append(probs, fmt.Sprintf("SMTP_SECURITY: неизвестное значение %q (none|starttls|tls)", c.SMTPSecurity))
	}
	return probs
}

// EnsureDirs создаёт рабочие директории сервера, если их нет.
func (c *ServerConfig) EnsureDirs() error {
	dirs := []string{c.StorageDir, c.TrashDir, c.TempDir, filepath.Dir(c.StateDB)}
	if c.LogDir != "" {
		dirs = append(dirs, c.LogDir)
	}
	if c.AuditLog != "" {
		dirs = append(dirs, filepath.Dir(c.AuditLog))
	}
	if c.LockFile != "" {
		dirs = append(dirs, filepath.Dir(c.LockFile))
	}
	for _, d := range dirs {
		if d == "" || d == "." {
			continue
		}
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("создание %s: %w", d, err)
		}
	}
	return nil
}

// Validate возвращает список проблем конфигурации клиента.
func (c *ClientConfig) Validate() []string {
	probs := append([]string{}, c.parseProblems...)
	for _, f := range []struct{ name, path string }{
		{"TLS_CERT_FILE", c.TLSCertFile}, {"TLS_KEY_FILE", c.TLSKeyFile}, {"TLS_CA_FILE", c.TLSCAFile},
	} {
		if f.path == "" {
			continue
		}
		if err := fileReadable(f.path); err != nil {
			probs = append(probs, fmt.Sprintf("%s: %v", f.name, err))
		}
	}
	if c.BackupDir != "" {
		if st, err := os.Stat(c.BackupDir); err != nil {
			probs = append(probs, fmt.Sprintf("BACKUP_DIR: %v", err))
		} else if !st.IsDir() {
			probs = append(probs, "BACKUP_DIR не является директорией")
		}
	}
	if err := dirUsable(c.LogDir); err != nil {
		probs = append(probs, fmt.Sprintf("LOG_DIR: %v", err))
	}
	if c.MaxConnections < 1 {
		probs = append(probs, "MAX_CONNECTIONS должен быть ≥ 1")
	}
	if c.ChunkSize <= 0 {
		probs = append(probs, "CHUNK_SIZE должен быть > 0")
	}
	return probs
}

// EnsureDirs создаёт директории логов клиента.
func (c *ClientConfig) EnsureDirs() error {
	dirs := []string{}
	if c.LogDir != "" {
		dirs = append(dirs, c.LogDir)
	}
	if c.AuditLog != "" {
		dirs = append(dirs, filepath.Dir(c.AuditLog))
	}
	for _, d := range dirs {
		if d == "" || d == "." {
			continue
		}
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("создание %s: %w", d, err)
		}
	}
	return nil
}
