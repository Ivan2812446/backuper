# Backuper

**Русский** | [English](#english)

Система резервного копирования одной директории клиента на сервер: периодическая
сверка (инициатор — **сервер**, модель *pull*), корзина с версиями, проверка
целостности по размеру, алерты на почту и работа в роли системной службы.
Связь — собственный бинарный протокол поверх сырых **TLS 1.2+/mTLS**-сокетов с
взаимной аутентификацией и обменом паролями.

Рассчитан на большие объёмы: ~**1 млн файлов / 4 ТБ** без загрузки всего индекса в
память (потоковый обход + дифф в SQLite).

> Полное техническое задание — в [тз.md](тз.md). Развёртывание — в [DEPLOY.md](DEPLOY.md).

## Возможности

- **Pull-модель:** сервер сам подключается к клиенту, забирает новые/изменённые файлы.
- **Сравнение** по `relpath + размер + mtime` (без хэшей; допуск `MTIME_TOLERANCE`).
- **Корзина:** удалённые на клиенте файлы переносятся в `TRASH_DIR` с сохранением пути,
  прежние версии — в `.versions/<seq>/`, авто-очистка через N дней.
- **Докачка** с места обрыва, персистентная очередь задач (переживает перезапуск).
- **Дельта-передача** изменённых файлов по блокам (передаются только изменённые блоки; экономит трафик при дозаписи/правках), с откатом на полное скачивание.
- **Проверка целостности** по совпадению размера в байтах; повтор и пропуск с алертом.
- **TLS 1.2+/mTLS** по общему CA + обмен паролями.
- **Параллельная передача** пулом соединений, общий лимит скорости (token-bucket).
- **Восстановление** (restore) сервер → клиент: файл, поддерево или весь набор, с `--force`.
- **Алерты по e-mail** (HTML + текст): отчёт по циклу, ошибки, переполнение диска,
  массовое удаление, перезапуск службы, наложение циклов.
- **Службы** systemd / Windows Service с автозапуском и автоперезапуском.
- **Логи и аудит** (JSON) с обеих сторон; dry-run и полный тестовый прогон.
- **Кроссплатформенность:** Linux (Debian-семейство) и Windows 10+. Единый статический
  бинарник, **без CGO** (драйвер SQLite — чистый Go), простая кросс-компиляция.

## Архитектура

```
            (сервер инициирует подключение, одна LAN)
   ┌──────────────────┐   TLS 1.2+, mTLS, свой протокол   ┌──────────────────┐
   │      СЕРВЕР       │ ─────────  control conn  ───────► │      КЛИЕНТ       │
   │   (служба)        │  AUTH/LIST/DISK/PING               │   (служба)        │
   │  storage_dir      │ ◄──────────────────────────────── │   backup_dir      │
   │  trash_dir        │   N× data conn (пул)              │    (/data)        │
   │  temp_dir         │ ─────────  GET(offset)  ─────────► │                   │
   │  SQLite (WAL)     │ ◄────────  file data    ────────── │                   │
   │  SMTP alerts      │   PUT (restore)                    │                   │
   └──────────────────┘                                    └──────────────────┘
```

Стек: **Go**, SQLite (`modernc.org/sqlite`, WAL), ротация логов (`lumberjack`),
почта через стандартный `net/smtp`.

## Установка одной командой

Установщик сам скачивает готовый бинарник из
[GitHub Release](https://github.com/Ivan2812446/backuper/releases/latest)
(под нужную ОС/архитектуру), создаёт пользователя и каталоги, пишет `.env`,
регистрирует службу (systemd / Windows Service) и запускает её. По одному скрипту
на ОС, каждый — для обеих ролей (`server` и `client`).

### Linux (Debian-семейство), от root

```bash
# СЕРВЕР — сразу с паролями и адресом клиента: сгенерирует сертификаты и запустится
curl -fsSL https://raw.githubusercontent.com/Ivan2812446/backuper/main/install.sh \
  | sudo OWN_PASSWORD='пароль-сервера-не-короче-24' PEER_PASSWORD='пароль-клиента-не-короче-24' \
         CLIENT_HOST=192.168.1.50 bash -s server

# КЛИЕНТ
curl -fsSL https://raw.githubusercontent.com/Ivan2812446/backuper/main/install.sh \
  | sudo OWN_PASSWORD='пароль-клиента-не-короче-24' PEER_PASSWORD='пароль-сервера-не-короче-24' \
         BACKUP_DIR=/data bash -s client
```

После установки сервера скопируйте на клиента три файла:
`/etc/backuper/certs/{ca.crt,client.crt,client.key}` → в тот же каталог на клиенте,
затем `systemctl enable --now backuper-client`.
Можно запускать и без переменных — установщик всё поставит и подскажет, что
дописать в `/etc/backuper/.env`. (`wget` тоже поддерживается вместо `curl`.)

### Windows 10+ (PowerShell от администратора)

```powershell
iwr https://raw.githubusercontent.com/Ivan2812446/backuper/main/install.ps1 -OutFile "$env:TEMP\binst.ps1"
# СЕРВЕР
& "$env:TEMP\binst.ps1" -Role server -ClientHost 192.168.1.50 -OwnPassword 'пароль-сервера' -PeerPassword 'пароль-клиента'
# КЛИЕНТ
& "$env:TEMP\binst.ps1" -Role client -BackupDir C:\Data -OwnPassword 'пароль-клиента' -PeerPassword 'пароль-сервера'
```

### Сборка из исходников (альтернатива)

```bash
make build                  # бинарники в ./bin (нужен Go; CGO не требуется)
make build-windows          # кросс под Windows
bin/backuper-server gen-certs -out certs -client-host 192.168.1.50 -server-host 192.168.1.10
cp .env.server.example /etc/backuper/.env   # заполнить
backuper-server check-config && backuper-server dry-run
```

### Обновление и удаление

```bash
# обновить бинарник из последнего релиза (.env и сертификаты сохраняются):
curl -fsSL https://raw.githubusercontent.com/Ivan2812446/backuper/main/install.sh | sudo bash -s update
# удалить (службы и бинарники; данные сохранить):
curl -fsSL https://raw.githubusercontent.com/Ivan2812446/backuper/main/install.sh | sudo bash -s uninstall
# удалить полностью (вместе с конфигом, данными и пользователем):
curl -fsSL https://raw.githubusercontent.com/Ivan2812446/backuper/main/install.sh | sudo bash -s uninstall --purge
```
Windows: `install.ps1 -Update` | `-Uninstall [-Purge]`.

Подробное руководство (ротация ключей, ручной перенос 4 ТБ) — [DEPLOY.md](DEPLOY.md).

## CLI

**Сервер** (`-config PATH`, по умолчанию `/etc/backuper/.env`):

| Команда | Назначение |
|---|---|
| `run` | основной режим (служба) |
| `check-config` | валидация `.env` и доступности ресурсов |
| `dry-run [-mail]` | цикл без передачи: что было бы скачано/в корзину (+тест SMTP) |
| `restore -path REL\|-all [-force]` | восстановление на клиент |
| `status` | последний цикл, очередь, индекс, корзина |
| `test` | тестовый прогон (config + связь + SMTP) |
| `gen-certs -out DIR -client-host H [-server-host H]` | генерация CA/сертификатов |

**Клиент:** `run`, `check-config`, `status`.

## Конфигурация

Всё в `.env` (отдельно для сервера и клиента), длительности `30s/5m/1h`, размеры
`1MiB/100MB`. Полные таблицы параметров — в [.env.server.example](.env.server.example),
[.env.client.example](.env.client.example) и разделе 18 [ТЗ](тз.md).

Соответствие паролей: `server.OWN_PASSWORD == client.PEER_PASSWORD` и наоборот.

## Тестирование

```bash
make test-all      # локальный E2E всех функций (рукопожатие, дифф, докачка,
                   # корзина+версии, очистка, массовое удаление, restore, SMTP-mock, lock)
```

## Структура проекта

```
cmd/{server,client}        точки входа (CLI)
internal/
  protocol   кадры, сообщения, кодеки, коды ошибок, пути
  tlsconn    TLS/mTLS, обмен паролями, кадрирование
  config     загрузка и валидация .env
  logx       логи + аудит (JSON)
  diskinfo   свободное место (Linux/Windows)
  store      SQLite: индекс, очередь, корзина, циклы, события
  transfer   пул, докачка, проверка размера, лимит скорости
  trash      перенос, версии, очистка по сроку
  alert      SMTP, HTML-отчёт, агрегация
  client     listener, сканер, отдача/приём файлов
  server     планировщик, диффер, цикл, первый запуск, restore, lock
deploy/      install.sh, gen-certs.sh, install.ps1, systemd/
scripts/     test-all
```

## Лицензия

MIT (см. [LICENSE](LICENSE)).

---

<a name="english"></a>
# Backuper (English)

[Русский](#backuper) | **English**

A backup system that copies a single client directory to a server: periodic
reconciliation (the **server** is the initiator — *pull* model), a versioned trash,
size-based integrity checks, e-mail alerts, and operation as a system service.
Transport is a custom binary protocol over raw **TLS 1.2+/mTLS** sockets with mutual
authentication and password exchange.

Designed for scale: ~**1M files / 4 TB** without loading the whole index into memory
(streaming walk + diff in SQLite).

> Full spec (Russian): [тз.md](тз.md). Deployment guide: [DEPLOY.md](DEPLOY.md).

## Features

- **Pull model:** the server connects to the client and fetches new/changed files.
- **Comparison** by `relpath + size + mtime` (no hashes; `MTIME_TOLERANCE` slack).
- **Trash:** files deleted on the client are moved to `TRASH_DIR` keeping their path,
  previous versions go to `.versions/<seq>/`, auto-purged after N days.
- **Resumable** transfers with a persistent task queue (survives restarts).
- **Delta sync** for changed files (only changed fixed-size blocks are sent; saves bandwidth on appends/in-place edits), with fallback to full download.
- **Integrity** by byte-size match; retry then skip-with-alert on mismatch.
- **TLS 1.2+/mTLS** via a shared CA + password exchange.
- **Parallel transfers** over a connection pool with a shared bandwidth token-bucket.
- **Restore** server → client: a file, a subtree or everything, with `--force`.
- **E-mail alerts** (HTML + text): per-cycle report, errors, disk pressure, mass
  deletion, service restart, overlapping cycles.
- **Services:** systemd / Windows Service with auto-start and auto-restart.
- **Logs & JSON audit** on both sides; dry-run and a full test run.
- **Cross-platform:** Linux (Debian family) and Windows 10+. Single static binary,
  **no CGO** (pure-Go SQLite driver) — trivial cross-compilation.

## Architecture

```
            (server initiates the connection, single LAN)
   ┌──────────────────┐   TLS 1.2+, mTLS, custom protocol  ┌──────────────────┐
   │      SERVER       │ ─────────  control conn  ───────► │      CLIENT       │
   │   (service)       │  AUTH/LIST/DISK/PING               │   (service)       │
   │  storage_dir      │ ◄──────────────────────────────── │   backup_dir      │
   │  trash_dir        │   N× data conn (pool)             │    (/data)        │
   │  temp_dir         │ ─────────  GET(offset)  ─────────► │                   │
   │  SQLite (WAL)     │ ◄────────  file data    ────────── │                   │
   │  SMTP alerts      │   PUT (restore)                    │                   │
   └──────────────────┘                                    └──────────────────┘
```

Stack: **Go**, SQLite (`modernc.org/sqlite`, WAL), log rotation (`lumberjack`),
mail via the standard `net/smtp`.

## One-command install

The installer downloads the right prebuilt binary from the
[GitHub Release](https://github.com/Ivan2812446/backuper/releases/latest), creates
the user and directories, writes `.env`, registers the service (systemd / Windows
Service) and starts it. One script per OS, each handling both roles.

### Linux (Debian family), as root

```bash
# SERVER — with passwords and the client address: also generates certs and starts
curl -fsSL https://raw.githubusercontent.com/Ivan2812446/backuper/main/install.sh \
  | sudo OWN_PASSWORD='server-pass-min-24' PEER_PASSWORD='client-pass-min-24' \
         CLIENT_HOST=192.168.1.50 bash -s server

# CLIENT
curl -fsSL https://raw.githubusercontent.com/Ivan2812446/backuper/main/install.sh \
  | sudo OWN_PASSWORD='client-pass-min-24' PEER_PASSWORD='server-pass-min-24' \
         BACKUP_DIR=/data bash -s client
```

After the server install, copy `/etc/backuper/certs/{ca.crt,client.crt,client.key}`
to the same directory on the client, then `systemctl enable --now backuper-client`.
You may also run it without env vars — the installer sets everything up and tells
you what to fill into `/etc/backuper/.env`. (`wget` works too if `curl` is absent.)

### Windows 10+ (PowerShell as Administrator)

```powershell
iwr https://raw.githubusercontent.com/Ivan2812446/backuper/main/install.ps1 -OutFile "$env:TEMP\binst.ps1"
& "$env:TEMP\binst.ps1" -Role server -ClientHost 192.168.1.50 -OwnPassword 'server-pass' -PeerPassword 'client-pass'
& "$env:TEMP\binst.ps1" -Role client -BackupDir C:\Data -OwnPassword 'client-pass' -PeerPassword 'server-pass'
```

### Build from source (alternative)

```bash
make build                  # binaries in ./bin (Go required; no CGO)
make build-windows
bin/backuper-server gen-certs -out certs -client-host 192.168.1.50 -server-host 192.168.1.10
cp .env.server.example /etc/backuper/.env   # fill in
backuper-server check-config && backuper-server dry-run
```

### Update and uninstall

```bash
# update the binary from the latest release (.env and certs are kept):
curl -fsSL https://raw.githubusercontent.com/Ivan2812446/backuper/main/install.sh | sudo bash -s update
# remove services and binaries (keep data):
curl -fsSL https://raw.githubusercontent.com/Ivan2812446/backuper/main/install.sh | sudo bash -s uninstall
# remove everything (config, data, user):
curl -fsSL https://raw.githubusercontent.com/Ivan2812446/backuper/main/install.sh | sudo bash -s uninstall --purge
```
Windows: `install.ps1 -Update` | `-Uninstall [-Purge]`.

Full guide (key rotation, the 4 TB manual seed) — [DEPLOY.md](DEPLOY.md).

## CLI

**Server** (`-config PATH`, default `/etc/backuper/.env`):

| Command | Purpose |
|---|---|
| `run` | main service mode |
| `check-config` | validate `.env` and resources |
| `dry-run [-mail]` | reconcile without transfer; show plan (+ SMTP test) |
| `restore -path REL\|-all [-force]` | restore to the client |
| `status` | last cycle, queue, index, trash |
| `test` | smoke run (config + connectivity + SMTP) |
| `gen-certs -out DIR -client-host H [-server-host H]` | generate CA/certs |

**Client:** `run`, `check-config`, `status`.

## Configuration

Everything lives in `.env` (separate for server and client); durations `30s/5m/1h`,
sizes `1MiB/100MB`. Full parameter tables are in the example files and section 18 of
the spec. Password matching: `server.OWN_PASSWORD == client.PEER_PASSWORD` and vice versa.

## Testing

```bash
make test-all   # local end-to-end of all features (handshake, diff, resume,
                # trash+versions, purge, mass-delete, restore, mock-SMTP, lock)
```