// Command backuper-server — точка входа сервера (раздел 22 ТЗ):
// run / check-config / dry-run / restore / status / test / gen-certs.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	_ "time/tzdata" // встроенная база часовых поясов (Europe/Moscow на любой ОС)

	"backuper/internal/config"
	"backuper/internal/logx"
	"backuper/internal/server"
)

func defaultConfigPath() string {
	if runtime.GOOS == "windows" {
		if exe, err := os.Executable(); err == nil {
			return exe[:len(exe)-len(baseName(exe))] + ".env"
		}
		return ".env"
	}
	return "/etc/backuper/.env"
}

func baseName(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[i+1:]
		}
	}
	return p
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
	case "dry-run":
		cmdDryRun(os.Args[2:])
	case "restore":
		cmdRestore(os.Args[2:])
	case "status":
		cmdStatus(os.Args[2:])
	case "test":
		cmdTest(os.Args[2:])
	case "gen-certs":
		cmdGenCerts(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `backuper-server — система резервного копирования (сервер)

Использование:
  backuper-server run            [-config PATH]   запуск службы
  backuper-server check-config   [-config PATH]   проверка .env и ресурсов
  backuper-server dry-run        [-config PATH] [-mail]  проход без передачи
  backuper-server restore        [-config PATH] -path REL|-all [-force]
  backuper-server status         [-config PATH]   состояние и последний цикл
  backuper-server test           [-config PATH]   проверка функций (config+связь+SMTP)
  backuper-server gen-certs      -out DIR -client-host HOST[,HOST] [-server-host HOST]`)
}

func loadServerCfg(path string, requireValid bool) *config.ServerConfig {
	cfg, err := config.LoadServer(path)
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

func newLogger(cfg *config.ServerConfig) *logx.Logger {
	log, err := logx.New(cfg.LogOptions())
	if err != nil {
		fmt.Fprintf(os.Stderr, "ОШИБКА логирования: %v\n", err)
		os.Exit(1)
	}
	return log
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func cmdRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "путь к .env")
	fs.Parse(args)

	cfg := loadServerCfg(*cfgPath, false)
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
	log := newLogger(cfg)

	lock, err := server.AcquireLock(cfg.LockFile)
	if err != nil {
		log.Error("service", "блокировка: %v", err)
		fmt.Fprintf(os.Stderr, "ОШИБКА: %v\n", err)
		os.Exit(1)
	}
	defer lock.Release()

	srv, err := server.New(cfg, log)
	if err != nil {
		log.Error("service", "инициализация: %v", err)
		os.Exit(1)
	}
	defer srv.Close()

	ctx, stop := signalContext()
	defer stop()
	log.Info("service", "сервер запущен (интервал сверки %s)", cfg.SyncInterval)
	if err := srv.Run(ctx); err != nil {
		log.Error("service", "завершение с ошибкой: %v", err)
		os.Exit(1)
	}
	log.Info("service", "сервер остановлен")
}

func cmdCheckConfig(args []string) {
	fs := flag.NewFlagSet("check-config", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "путь к .env")
	fs.Parse(args)

	cfg, err := config.LoadServer(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ОШИБКА: %v\n", err)
		os.Exit(1)
	}
	probs := cfg.Validate()
	if len(probs) > 0 {
		fmt.Fprintln(os.Stderr, "Конфигурация некорректна:")
		for _, p := range probs {
			fmt.Fprintf(os.Stderr, "  - %s\n", p)
		}
		os.Exit(1)
	}
	fmt.Println("Конфигурация сервера корректна.")
}

func cmdDryRun(args []string) {
	fs := flag.NewFlagSet("dry-run", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "путь к .env")
	mail := fs.Bool("mail", false, "отправить тестовое письмо SMTP")
	fs.Parse(args)

	cfg := loadServerCfg(*cfgPath, true)
	_ = cfg.EnsureDirs()
	log := newLogger(cfg)
	srv, err := server.New(cfg, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ОШИБКА: %v\n", err)
		os.Exit(1)
	}
	defer srv.Close()
	ctx, stop := signalContext()
	defer stop()
	if err := srv.DryRun(ctx, *mail); err != nil {
		fmt.Fprintf(os.Stderr, "ОШИБКА: %v\n", err)
		os.Exit(1)
	}
}

func cmdRestore(args []string) {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "путь к .env")
	path := fs.String("path", "", "relpath файла или поддерева")
	all := fs.Bool("all", false, "восстановить весь набор")
	force := fs.Bool("force", false, "перезаписывать более новые файлы на клиенте")
	fs.Parse(args)

	cfg := loadServerCfg(*cfgPath, true)
	_ = cfg.EnsureDirs()
	log := newLogger(cfg)
	srv, err := server.New(cfg, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ОШИБКА: %v\n", err)
		os.Exit(1)
	}
	defer srv.Close()
	ctx, stop := signalContext()
	defer stop()
	if err := srv.Restore(ctx, server.RestoreOptions{Path: *path, All: *all, Force: *force}); err != nil {
		fmt.Fprintf(os.Stderr, "ОШИБКА: %v\n", err)
		os.Exit(1)
	}
}

func cmdStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "путь к .env")
	fs.Parse(args)

	cfg := loadServerCfg(*cfgPath, true)
	log := newLogger(cfg)
	srv, err := server.New(cfg, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ОШИБКА: %v\n", err)
		os.Exit(1)
	}
	defer srv.Close()
	if err := srv.PrintStatus(os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "ОШИБКА: %v\n", err)
		os.Exit(1)
	}
}

func cmdTest(args []string) {
	fs := flag.NewFlagSet("test", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "путь к .env")
	fs.Parse(args)

	cfg := loadServerCfg(*cfgPath, true)
	_ = cfg.EnsureDirs()
	log := newLogger(cfg)
	srv, err := server.New(cfg, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ОШИБКА: %v\n", err)
		os.Exit(1)
	}
	defer srv.Close()
	ctx, stop := signalContext()
	defer stop()
	fmt.Println("Тестовый прогон: проверка конфигурации, связи с клиентом и SMTP.")
	fmt.Println("(полный сценарий всех функций — scripts/test-all)")
	if err := srv.DryRun(ctx, true); err != nil {
		fmt.Fprintf(os.Stderr, "ОШИБКА: %v\n", err)
		os.Exit(1)
	}
}
