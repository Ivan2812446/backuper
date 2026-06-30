// Command backuper-setup — графический-консольный установщик для Windows.
// Запускается двойным кликом (.exe): спрашивает роль и параметры, скачивает
// нужный бинарник из GitHub Release, генерирует сертификаты (для сервера), пишет
// .env, регистрирует и запускает службу Windows. Сборка кросс-компиляцией из любой
// ОС: GOOS=windows GOARCH=amd64 go build ./cmd/setup
package main

import (
	"bufio"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"backuper/internal/certs"
)

const (
	repo       = "Ivan2812446/backuper"
	installDir = `C:\Backuper`
)

var in = bufio.NewReader(os.Stdin)

func pause() {
	fmt.Print("\nНажмите Enter для выхода…")
	_, _ = in.ReadString('\n')
}

func fail(format string, a ...any) {
	fmt.Printf("\nОШИБКА: "+format+"\n", a...)
	pause()
	os.Exit(1)
}

func prompt(line string) string {
	fmt.Print(line)
	s, _ := in.ReadString('\n')
	return strings.TrimRight(s, "\r\n")
}

// ask — вопрос со значением по умолчанию (Enter — принять).
func ask(desc, def string) string {
	fmt.Println()
	fmt.Println(desc)
	v := prompt(fmt.Sprintf("  [по умолчанию: %s] (Enter — принять): ", def))
	if v == "" {
		return def
	}
	return v
}

// askRequired — обязательный вопрос (повторяем, пока не введут).
func askRequired(desc string) string {
	for {
		fmt.Println()
		fmt.Println(desc)
		v := prompt("  значение (обязательно): ")
		if v != "" {
			return v
		}
		fmt.Println("  это поле обязательно")
	}
}

func askOpt(desc string) string {
	fmt.Println()
	fmt.Println(desc)
	return prompt("  (Enter — пропустить): ")
}

func genPassword() string {
	const cs = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = cs[int(b[i])%len(cs)]
	}
	return string(b)
}

func askPassword(desc string) string {
	fmt.Println()
	fmt.Println(desc)
	v := prompt("  введите пароль (Enter — сгенерировать случайный): ")
	if v == "" {
		v = genPassword()
		fmt.Printf("  сгенерирован: %s\n", v)
	}
	return v
}

func askChoice(desc, def string, opts []string) string {
	fmt.Println()
	fmt.Println(desc)
	for i, o := range opts {
		fmt.Printf("  %d. %s\n", i+1, o)
	}
	fmt.Printf("  %d. другое значение\n", len(opts)+1)
	v := prompt(fmt.Sprintf("  выберите номер или впишите своё [по умолчанию: %s] (Enter — принять): ", def))
	if v == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err == nil && fmt.Sprintf("%d", n) == v {
		if n >= 1 && n <= len(opts) {
			return opts[n-1]
		}
		if n == len(opts)+1 {
			c := prompt("  введите своё значение: ")
			if c == "" {
				return def
			}
			return c
		}
	}
	return v
}

func yesno(q string, def bool) bool {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	v := strings.ToLower(prompt(fmt.Sprintf("\n%s [%s]: ", q, hint)))
	if v == "" {
		return def
	}
	return strings.HasPrefix(v, "y") || strings.HasPrefix(v, "д")
}

func download(url, dest string) error {
	fmt.Printf("Скачивание %s …\n", url)
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, resp.Body)
	f.Close()
	if err != nil {
		return err
	}
	fmt.Printf("  загружено %d Б → %s\n", n, dest)
	return nil
}

func sc(args ...string) error {
	cmd := exec.Command("sc.exe", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("sc %v: %v: %s", args, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func registerService(role, exe, envFile string) {
	svc := "backuper-" + role
	// удалить старую версию службы, если есть
	_ = exec.Command("sc.exe", "stop", svc).Run()
	time.Sleep(time.Second)
	_ = exec.Command("sc.exe", "delete", svc).Run()
	time.Sleep(time.Second)

	binPath := fmt.Sprintf("\"%s\" run -config \"%s\"", exe, envFile)
	if err := sc("create", svc, "binPath=", binPath, "start=", "auto", "DisplayName=", "Backuper "+role); err != nil {
		fail("регистрация службы не удалась: %v\n(запустите установщик от имени Администратора)", err)
	}
	_ = sc("failure", svc, "reset=", "86400", "actions=", "restart/5000/restart/5000/restart/5000")
	_ = exec.Command("sc.exe", "failureflag", svc, "1").Run()
	fmt.Printf("Служба %s зарегистрирована (автозапуск + автоперезапуск).\n", svc)
}

func writeEnv(path, content string) {
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		fail("запись %s: %v", path, err)
	}
	// ужесточаем доступ к .env (только SYSTEM и администраторы)
	_ = exec.Command("icacls.exe", path, "/inheritance:r", "/grant:r", "SYSTEM:(F)", "Administrators:(F)").Run()
}

func releaseURL(asset string) string {
	return fmt.Sprintf("https://github.com/%s/releases/latest/download/%s", repo, asset)
}

func main() {
	fmt.Println("============================================")
	fmt.Println("  Backuper — установка для Windows")
	fmt.Println("============================================")

	role := ""
	switch askChoice("Что устанавливаем?", "1", []string{"СЕРВЕР (инициирует бэкап, хранит копии)", "КЛИЕНТ (отдаёт файлы серверу)"}) {
	case "СЕРВЕР (инициирует бэкап, хранит копии)", "1":
		role = "server"
	case "КЛИЕНТ (отдаёт файлы серверу)", "2":
		role = "client"
	default:
		role = "server"
	}

	certsDir := filepath.Join(installDir, "certs")
	exe := filepath.Join(installDir, "backuper-"+role+".exe")
	envFile := filepath.Join(installDir, ".env")
	if err := os.MkdirAll(certsDir, 0o755); err != nil {
		fail("создание %s: %v (запустите от Администратора)", installDir, err)
	}

	if err := download(releaseURL("backuper-"+role+"-windows-amd64.exe"), exe); err != nil {
		fail("скачивание бинарника: %v", err)
	}

	if role == "server" {
		setupServer(exe, envFile, certsDir)
	} else {
		setupClient(exe, envFile, certsDir)
	}

	// проверка конфигурации
	fmt.Println("\nПроверка конфигурации…")
	cc := exec.Command(exe, "check-config", "-config", envFile)
	cc.Stdout = os.Stdout
	cc.Stderr = os.Stderr
	cfgOK := cc.Run() == nil

	registerService(role, exe, envFile)

	haveCerts := fileExists(filepath.Join(certsDir, "ca.crt")) &&
		fileExists(filepath.Join(certsDir, role+".crt")) && fileExists(filepath.Join(certsDir, role+".key"))
	if cfgOK && haveCerts {
		if err := sc("start", "backuper-"+role); err != nil {
			fmt.Printf("Не удалось запустить службу: %v\n", err)
		} else {
			fmt.Println("\nСлужба запущена. Готово!")
		}
	} else {
		fmt.Println("\nСлужба зарегистрирована, но НЕ запущена — завершите настройку:")
		if role == "client" && !haveCerts {
			fmt.Printf("  • положите в %s файлы ca.crt, client.crt, client.key (с сервера)\n", certsDir)
		}
		fmt.Printf("  • затем: sc start backuper-%s\n", role)
	}
	fmt.Printf("\nБинарник: %s\nКонфиг:   %s\nСертификаты: %s\n", exe, envFile, certsDir)
	pause()
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func setupServer(exe, envFile, certsDir string) {
	clientHost := askRequired("Адрес КЛИЕНТА в локальной сети (IP/hostname), к которому сервер подключается за файлами (CLIENT_HOST).")
	ownPw := askPassword("Пароль ЭТОГО сервера (на клиенте — как PEER_PASSWORD).")
	peerPw := askPassword("Пароль КЛИЕНТА (то, что у клиента в OWN_PASSWORD).")
	storage := ask("Куда складывать копии (STORAGE_DIR).", installDir+`\storage`)
	trash := ask("Корзина для удалённых файлов (TRASH_DIR).", installDir+`\trash`)
	temp := ask("Временные файлы докачки (TEMP_DIR).", installDir+`\temp`)
	syncIv := askChoice("Как часто запускать сверку/бэкап (SYNC_INTERVAL).", "1h", []string{"15m", "1h", "6h", "24h"})
	retention := ask("Сколько дней хранить файлы в корзине (TRASH_RETENTION_DAYS).", "10")
	parallel := askChoice("Сколько файлов качать параллельно (PARALLEL_TRANSFERS).", "4", []string{"2", "4", "8"})
	bw := ask("Лимит скорости, байт/с (0 — без лимита; можно 10MB) (BANDWIDTH_LIMIT).", "0")
	deltaMin, deltaBlk := "0", "1MiB"
	if yesno("Включить дельта-передачу изменённых файлов (экономит трафик)?", true) {
		deltaMin = askChoice("Применять дельту для изменённых файлов не меньше размера (DELTA_MIN_SIZE).", "1MiB", []string{"512KiB", "1MiB", "10MiB", "100MiB"})
		deltaBlk = askChoice("Размер блока сравнения (DELTA_BLOCK_SIZE).", "1MiB", []string{"256KiB", "1MiB", "4MiB"})
	}
	tlsMin := askChoice("Минимальная версия TLS (TLS_MIN_VERSION).", "1.2", []string{"1.2", "1.3"})
	logLevel := askChoice("Уровень логирования (LOG_LEVEL).", "INFO", []string{"INFO", "DEBUG", "WARN", "ERROR"})

	var smtpHost, smtpPort, smtpSec, smtpFrom, smtpTo, smtpUser, smtpPass string
	if yesno("Настроить отправку e-mail алертов сейчас?", true) {
		smtpHost = askRequired("Адрес SMTP-сервера (SMTP_HOST), напр. smtp.gmail.com.")
		smtpPort = ask("Порт SMTP (SMTP_PORT).", "587")
		smtpSec = askChoice("Шифрование SMTP (SMTP_SECURITY).", "starttls", []string{"starttls", "tls", "none"})
		smtpFrom = askRequired("Адрес отправителя (SMTP_FROM).")
		smtpTo = askRequired("Получатели алертов через запятую (SMTP_TO).")
		smtpUser = askOpt("Логин SMTP (SMTP_USER), если нужен.")
		smtpPass = askOpt("Пароль SMTP (SMTP_PASSWORD).")
	} else {
		smtpHost, smtpPort, smtpSec = "ЗАМЕНИТЕ-smtp.example.com", "587", "starttls"
		smtpFrom, smtpTo = "ЗАМЕНИТЕ-backuper@example.com", "ЗАМЕНИТЕ-admin@example.com"
		fmt.Println("SMTP пропущен — заполните позже в " + envFile)
	}

	for _, d := range []string{storage, trash, temp, installDir + `\logs`} {
		_ = os.MkdirAll(d, 0o755)
	}

	// сертификаты
	if !fileExists(filepath.Join(certsDir, "ca.crt")) && yesno("Сгенерировать TLS-сертификаты сейчас?", true) {
		serverHost := ask("Адрес ЭТОГО сервера для SAN сертификата (SERVER_HOST).", "127.0.0.1")
		if err := certs.WriteAll(certsDir, []string{serverHost}, []string{clientHost}, 3650); err != nil {
			fail("генерация сертификатов: %v", err)
		}
		fmt.Println("Сертификаты созданы в " + certsDir)
		fmt.Printf("СКОПИРУЙТЕ на клиента: %s\\{ca.crt, client.crt, client.key}\n", certsDir)
	}

	env := fmt.Sprintf(`CLIENT_HOST=%s
CLIENT_PORT=9000
TLS_CERT_FILE=%s\server.crt
TLS_KEY_FILE=%s\server.key
TLS_CA_FILE=%s\ca.crt
TLS_MIN_VERSION=%s
OWN_PASSWORD=%s
PEER_PASSWORD=%s
STORAGE_DIR=%s
TRASH_DIR=%s
TEMP_DIR=%s
SYNC_INTERVAL=%s
PARALLEL_TRANSFERS=%s
BANDWIDTH_LIMIT=%s
DELTA_MIN_SIZE=%s
DELTA_BLOCK_SIZE=%s
TRASH_RETENTION_DAYS=%s
SMTP_HOST=%s
SMTP_PORT=%s
SMTP_SECURITY=%s
SMTP_FROM=%s
SMTP_TO=%s
SMTP_USER=%s
SMTP_PASSWORD=%s
LOG_DIR=%s\logs
AUDIT_LOG=%s\logs\audit.jsonl
STATE_DB=%s\state.db
LOCK_FILE=%s\backuper.lock
LOG_LEVEL=%s
TIMEZONE=Europe/Moscow
`, clientHost, certsDir, certsDir, certsDir, tlsMin, ownPw, peerPw, storage, trash, temp,
		syncIv, parallel, bw, deltaMin, deltaBlk, retention,
		smtpHost, smtpPort, smtpSec, smtpFrom, smtpTo, smtpUser, smtpPass,
		installDir, installDir, installDir, installDir, logLevel)
	writeEnv(envFile, env)
	fmt.Println("Конфигурация записана: " + envFile)
}

func setupClient(exe, envFile, certsDir string) {
	listenHost := ask("Интерфейс прослушивания (LISTEN_HOST; 0.0.0.0 — все).", "0.0.0.0")
	listenPort := ask("Порт прослушивания (LISTEN_PORT).", "9000")
	ownPw := askPassword("Пароль ЭТОГО клиента (на сервере — как PEER_PASSWORD).")
	peerPw := askPassword("Пароль СЕРВЕРА (то, что у сервера в OWN_PASSWORD).")
	backupDir := askRequired(`Каталог-источник, который бэкапим, напр. C:\Data (BACKUP_DIR).`)
	excl := askChoice("Какие файлы НЕ бэкапить (EXCLUDE_PATTERNS).", "*.tmp,*.lock", []string{"*.tmp,*.lock", "*.tmp", "(ничего)"})
	if excl == "(ничего)" {
		excl = ""
	}
	tlsMin := askChoice("Минимальная версия TLS (TLS_MIN_VERSION).", "1.2", []string{"1.2", "1.3"})
	logLevel := askChoice("Уровень логирования (LOG_LEVEL).", "INFO", []string{"INFO", "DEBUG", "WARN", "ERROR"})
	_ = os.MkdirAll(installDir+`\logs`, 0o755)

	env := fmt.Sprintf(`LISTEN_HOST=%s
LISTEN_PORT=%s
TLS_CERT_FILE=%s\client.crt
TLS_KEY_FILE=%s\client.key
TLS_CA_FILE=%s\ca.crt
TLS_MIN_VERSION=%s
OWN_PASSWORD=%s
PEER_PASSWORD=%s
BACKUP_DIR=%s
EXCLUDE_PATTERNS=%s
LOG_DIR=%s\logs
AUDIT_LOG=%s\logs\audit.jsonl
LOG_LEVEL=%s
TIMEZONE=Europe/Moscow
`, listenHost, listenPort, certsDir, certsDir, certsDir, tlsMin, ownPw, peerPw, backupDir, excl,
		installDir, installDir, logLevel)
	writeEnv(envFile, env)
	fmt.Println("Конфигурация записана: " + envFile)
	fmt.Printf("Положите в %s файлы ca.crt, client.crt, client.key (с сервера).\n", certsDir)
}
