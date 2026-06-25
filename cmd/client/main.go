// Command backuper-client — точка входа клиента (раздел 22 ТЗ):
// run / check-config / status. Служба-слушатель источника бэкапа.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	_ "time/tzdata"

	"backuper/internal/client"
	"backuper/internal/config"
	"backuper/internal/logx"
	"backuper/internal/tlsconn"
)

func defaultConfigPath() string {
	if runtime.GOOS == "windows" {
		if exe, err := os.Executable(); err == nil {
			return filepath.Join(filepath.Dir(exe), ".env")
		}
		return ".env"
	}
	return "/etc/backuper/.env"
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "run":
		cmdRun(os.Args[2:])
	case "check-config":
		cmdCheckConfig(os.Args[2:])
	case "status":
		cmdStatus(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `backuper-client — система резервного копирования (клиент)

Использование:
  backuper-client run          [-config PATH]   запуск службы-слушателя
  backuper-client check-config [-config PATH]   проверка .env
  backuper-client status       [-config PATH]   состояние слушателя`)
}

func loadCfg(path string, requireValid bool) *config.ClientConfig {
	cfg, err := config.LoadClient(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ОШИБКА: %v\n", err)
		os.Exit(1)
	}
	if requireValid {
		if probs := cfg.Validate(); len(probs) > 0 {
			fmt.Fprintln(os.Stderr, "Конфигурация некорректна:")
			for _, p := range probs {
				fmt.Fprintf(os.Stderr, "  - %s\n", p)
			}
			os.Exit(1)
		}
	}
	return cfg
}

func baseDir(cfg *config.ClientConfig) string {
	if cfg.LogDir != "" {
		return cfg.LogDir
	}
	return os.TempDir()
}

func statusPath(cfg *config.ClientConfig) string {
	return filepath.Join(baseDir(cfg), "backuper-client-status.json")
}

func toClientConfig(cfg *config.ClientConfig) client.Config {
	return client.Config{
		ListenHost:       cfg.ListenHost,
		ListenPort:       cfg.ListenPort,
		BackupDir:        cfg.BackupDir,
		Include:          cfg.IncludePatterns,
		Exclude:          cfg.ExcludePatterns,
		ChunkSize:        cfg.ChunkSize,
		BandwidthLimit:   cfg.BandwidthLimit,
		IOTimeout:        cfg.IOTimeout,
		MaxFrame:         uint64(cfg.MaxFrameBytes),
		MaxConnections:   cfg.MaxConnections,
		SaveFilePerms:    os.FileMode(cfg.SaveFilePerms),
		SaveDirPerms:     os.FileMode(cfg.SaveDirPerms),
		OwnPassword:      cfg.OwnPassword,
		PeerPassword:     cfg.PeerPassword,
		StatusFile:       statusPath(cfg),
		RestoreTempDir:   filepath.Join(baseDir(cfg), "backuper-restore-tmp"),
		MtimeToleranceNS: (2 * time.Second).Nanoseconds(),
	}
}

func cmdRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "путь к .env")
	fs.Parse(args)

	cfg := loadCfg(*cfgPath, false)
	if err := cfg.EnsureDirs(); err != nil {
		fmt.Fprintf(os.Stderr, "ОШИБКА создания директорий: %v\n", err)
		os.Exit(1)
	}
	if probs := cfg.Validate(); len(probs) > 0 {
		fmt.Fprintln(os.Stderr, "Конфигурация некорректна:")
		for _, p := range probs {
			fmt.Fprintf(os.Stderr, "  - %s\n", p)
		}
		os.Exit(1)
	}
	log, err := logx.New(cfg.LogOptions())
	if err != nil {
		fmt.Fprintf(os.Stderr, "ОШИБКА логирования: %v\n", err)
		os.Exit(1)
	}
	tlsCfg, err := tlsconn.ListenerTLS(cfg.TLSCertFile, cfg.TLSKeyFile, cfg.TLSCAFile, cfg.TLSMinVersion)
	if err != nil {
		log.Error("service", "TLS: %v", err)
		os.Exit(1)
	}
	srv := client.New(toClientConfig(cfg), tlsCfg, log)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Info("service", "клиент запущен")
	if err := srv.Run(ctx); err != nil {
		log.Error("service", "завершение с ошибкой: %v", err)
		os.Exit(1)
	}
	log.Info("service", "клиент остановлен")
}

func cmdCheckConfig(args []string) {
	fs := flag.NewFlagSet("check-config", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "путь к .env")
	fs.Parse(args)

	cfg, err := config.LoadClient(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ОШИБКА: %v\n", err)
		os.Exit(1)
	}
	if probs := cfg.Validate(); len(probs) > 0 {
		fmt.Fprintln(os.Stderr, "Конфигурация некорректна:")
		for _, p := range probs {
			fmt.Fprintf(os.Stderr, "  - %s\n", p)
		}
		os.Exit(1)
	}
	fmt.Println("Конфигурация клиента корректна.")
}

func cmdStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "путь к .env")
	fs.Parse(args)

	cfg := loadCfg(*cfgPath, true)
	fmt.Println("== Backuper client status ==")
	fmt.Printf("Источник: %s\n", cfg.BackupDir)

	dialHost := cfg.ListenHost
	if dialHost == "0.0.0.0" || dialHost == "" || dialHost == "::" {
		dialHost = "127.0.0.1"
	}
	addr := net.JoinHostPort(dialHost, fmt.Sprintf("%d", cfg.ListenPort))
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		fmt.Printf("Порт %s: НЕДОСТУПЕН (%v)\n", addr, err)
	} else {
		conn.Close()
		fmt.Printf("Порт %s: слушает\n", addr)
	}

	if st, err := client.ReadStatus(statusPath(cfg)); err == nil {
		fmt.Printf("PID: %d, запущен: %s\n", st.PID, st.StartedAt)
		fmt.Printf("Принято соединений: %d\n", st.AcceptedTotal)
		if st.LastAt != "" {
			fmt.Printf("Последнее соединение: %s от %s (%s)\n", st.LastAt, st.LastRemote, st.LastResult)
		}
	} else {
		fmt.Println("Статус-файл недоступен (служба не запускалась или другой пользователь).")
	}
}
