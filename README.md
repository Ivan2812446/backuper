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

## Быстрый старт

```bash
# 1. Сборка (нужен Go; CGO не требуется)
make build                      # бинарники в ./bin
# кросс под Windows:
make build-windows

# 2. Сертификаты (один раз). SAN client.crt = адрес клиента в LAN.
bin/backuper-server gen-certs -out certs -client-host 192.168.1.50 -server-host 192.168.1.10
# серверу: ca.crt + server.crt + server.key ; клиенту: ca.crt + client.crt + client.key

# 3. Конфигурация — заполните по примерам
cp .env.server.example /etc/backuper/.env   # на сервере
cp .env.client.example /etc/backuper/.env   # на клиенте

# 4. Проверка и запуск
backuper-server check-config && backuper-server dry-run
backuper-client run    # на клиенте (служба-слушатель)
backuper-server run    # на сервере (планировщик циклов)
```

Установка как службы (Linux): `sudo deploy/install.sh server|client`.
Windows: `deploy/install.ps1 -Role server|client`. Подробно — [DEPLOY.md](DEPLOY.md).

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

## Quick start

```bash
# 1. Build (Go required; no CGO)
make build                      # binaries in ./bin
make build-windows              # cross-compile for Windows

# 2. Certificates (once). client.crt SAN = the client's LAN address.
bin/backuper-server gen-certs -out certs -client-host 192.168.1.50 -server-host 192.168.1.10
# server gets: ca.crt + server.crt + server.key ; client gets: ca.crt + client.crt + client.key

# 3. Configuration — fill in from the examples
cp .env.server.example /etc/backuper/.env   # on the server
cp .env.client.example /etc/backuper/.env   # on the client

# 4. Verify and run
backuper-server check-config && backuper-server dry-run
backuper-client run     # on the client (listener service)
backuper-server run     # on the server (cycle scheduler)
```

Install as a service (Linux): `sudo deploy/install.sh server|client`.
Windows: `deploy/install.ps1 -Role server|client`. See [DEPLOY.md](DEPLOY.md).

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