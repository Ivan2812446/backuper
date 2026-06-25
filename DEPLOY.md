# Backuper — руководство по развёртыванию

Система резервного копирования одной директории клиента на сервер: периодическая
сверка (инициатор — сервер, модель pull), корзина, проверка целостности по размеру,
алерты на почту, работа как системная служба. Канал — собственный бинарный протокол
поверх TLS 1.2+/mTLS с обменом паролями.

> Все даты/время — в часовом поясе **Europe/Moscow (МСК)**.
> Формат поставки: **исходники + установочные скрипты**.

---

## Содержание
1. Требования
2. Сборка
3. Генерация сертификатов
4. Установка на Linux (сервер и клиент)
5. Установка на Windows
6. Первичный ручной перенос (~1 млн файлов / 4 ТБ)
7. Проверка после деплоя
8. Эксплуатация (статус, логи, восстановление)
9. Обновление и ротация паролей/ключей
10. Типовые проблемы

---

## 1. Требования

- **Сервер** и **клиент** — в одной локальной сети; сервер должен иметь сетевой доступ
  к клиенту (по умолчанию TCP-порт `9000`). NAT/проброс портов не требуется.
- ОС: Debian-семейство Linux **или** Windows 10+.
- Для сборки: Go (любая актуальная версия; проект собран на Go 1.26). CGO **не нужен**
  (драйвер SQLite — `modernc.org/sqlite`, чистый Go), поэтому возможна простая
  кросс-компиляция под Windows.
- На сервере достаточно места под копии (`STORAGE_DIR`), корзину (`TRASH_DIR`) и
  временные файлы докачки (`TEMP_DIR`).

---

## 2. Сборка

На машине сборки (с установленным Go):

```bash
make build            # бинарники в ./bin: backuper-server, backuper-client
# либо вручную:
go build -o bin/backuper-server ./cmd/server
go build -o bin/backuper-client ./cmd/client
```

Кросс-компиляция под Windows:

```bash
make build-windows    # ./bin/backuper-server.exe, ./bin/backuper-client.exe
# либо вручную:
GOOS=windows GOARCH=amd64 go build -o bin/backuper-server.exe ./cmd/server
GOOS=windows GOARCH=amd64 go build -o bin/backuper-client.exe ./cmd/client
```

Бинарники статические, без рантайм-зависимостей; база часовых поясов встроена.

---

## 3. Генерация сертификатов (один раз)

Используется **mTLS**: обе стороны предъявляют сертификаты и проверяют друг друга по
общему корневому CA. Самоподписанные сертификаты, RSA-2048, срок ~3650 дней.

```bash
# через установочный скрипт-обёртку (вызовет бинарник или openssl):
deploy/gen-certs.sh -o certs -c <IP_КЛИЕНТА_в_LAN> -s <IP_СЕРВЕРА>

# либо напрямую через бинарник:
bin/backuper-server gen-certs -out certs -client-host <IP_КЛИЕНТА> -server-host <IP_СЕРВЕРА>
```

Создаются 6 файлов:

| Файл | Назначение | Куда положить |
|---|---|---|
| `ca.crt` | корневой CA | **на обе стороны** |
| `ca.key` | приватный ключ CA | хранить отдельно/надёжно, на узлы не нужен |
| `server.crt`, `server.key` | сертификат сервера (TLS-инициатор) | **только на сервер** |
| `client.crt`, `client.key` | сертификат клиента (TLS-слушатель) | **только на клиент** |

> **Важно про SAN.** Клиент — это TLS-**слушатель**, и сервер при подключении сверяет
> SAN сертификата клиента с `CLIENT_HOST`. Поэтому в `client.crt` (флаг `-client-host`)
> обязательно должен быть указан **адрес клиента в LAN** (IP или hostname, как он задан
> в `CLIENT_HOST` на сервере). Приватные ключи `*.key` чужой стороне не передаются.

---

## 4. Установка на Linux (systemd)

Скопируйте на узел каталог проекта с собранными `./bin` и каталогом `deploy/`.

### 4.1 Сервер

```bash
sudo deploy/install.sh server
```

Скрипт создаёт пользователя `backuper`, рабочие каталоги
(`/srv/backuper/{storage,trash,temp}`, `/var/lib/backuper`, `/var/log/backuper`,
`/etc/backuper/certs`), копирует бинарник в `/usr/local/bin`, ставит `.env` из примера
в `/etc/backuper/.env` (права `600`, без перезаписи существующего), устанавливает
`backuper-server.service` и выполняет `daemon-reload`.

Далее:

```bash
sudo nano /etc/backuper/.env        # пароли, CLIENT_HOST, STORAGE_DIR, SMTP и т.п.
# положите ca.crt, server.crt, server.key в /etc/backuper/certs
sudo chown -R backuper:backuper /etc/backuper/certs && sudo chmod 600 /etc/backuper/certs/*.key

sudo -u backuper backuper-server check-config
sudo systemctl enable --now backuper-server.service
```

### 4.2 Клиент

```bash
sudo deploy/install.sh client
sudo nano /etc/backuper/.env        # пароли (зеркально серверу), BACKUP_DIR, фильтры
# положите ca.crt, client.crt, client.key в /etc/backuper/certs
sudo chown -R backuper:backuper /etc/backuper/certs && sudo chmod 600 /etc/backuper/certs/*.key

sudo -u backuper backuper-client check-config
sudo systemctl enable --now backuper-client.service
```

### 4.3 Соответствие паролей

`server.OWN_PASSWORD == client.PEER_PASSWORD` и `client.OWN_PASSWORD == server.PEER_PASSWORD`.
Длина каждого пароля — не менее 24 символов.

---

## 5. Установка на Windows 10+

1. Скопируйте на узел `backuper-<role>.exe`, `.env` (или пример), каталог `certs\`,
   а также `deploy\install.ps1`.
2. Запустите PowerShell **от имени Администратора**:

```powershell
# сервер:
.\install.ps1 -Role server
# клиент:
.\install.ps1 -Role client
```

Скрипт раскладывает файлы в `C:\Backuper\`, ужесточает права на `.env` и ключи,
выполняет `check-config`, регистрирует службу `backuper-<role>` с автозапуском и
recovery-действиями (автоперезапуск при сбое).

3. Отредактируйте `C:\Backuper\.env` и положите сертификаты в `C:\Backuper\certs`.
4. Запустите службу:

```powershell
sc.exe start backuper-server   # или Start-Service backuper-server
& 'C:\Backuper\backuper-server.exe' status -config 'C:\Backuper\.env'
```

---

## 6. Первичный ручной перенос (~1 млн файлов / 4 ТБ)

1. По возможности приостановите запись в `BACKUP_DIR` (или примите, что часть файлов
   изменится — это штатно обрабатывается).
2. Физически перенесите данные (диск/копирование) в `STORAGE_DIR` на сервере,
   **сохранив структуру путей 1 в 1** (относительные пути должны совпадать).
3. Установите и запустите клиент и сервер.
4. При **первом запуске** сервер проиндексирует `STORAGE_DIR` по фактическим
   размеру/mtime (без чтения содержимого), запросит список клиента и догонит
   расхождения: новые/изменённые — скачает, отсутствующие у клиента — перенесёт в корзину.
5. Дождитесь письма-отчёта со статусом `OK` или `PARTIAL`.

---

## 7. Проверка после деплоя

```bash
backuper-server check-config && backuper-client check-config   # на своих узлах
backuper-server dry-run            # план без передачи: что будет скачано/в корзину
backuper-server dry-run -mail      # то же + тестовое письмо SMTP
backuper-server status             # последний цикл, очередь, индекс, корзина
backuper-client status             # слушатель и последние соединения
```

Полный локальный прогон всех функций (для машины разработки):

```bash
scripts/test-all
```

---

## 8. Эксплуатация

**Логи и аудит** (Linux): `/var/log/backuper/` (основной лог + `audit.jsonl`),
а также `journalctl -u backuper-server -f`.

**Состояние службы:**

```bash
systemctl status backuper-server
backuper-server status
```

**Восстановление файлов на клиент** (сервер → клиент):

```bash
# один файл или поддерево (relpath относительно BACKUP_DIR/STORAGE_DIR):
backuper-server restore -path docs/scan_001.pdf
backuper-server restore -path docs
# весь набор:
backuper-server restore -all
# перезаписывать даже более новые файлы на клиенте:
backuper-server restore -all -force
```

По умолчанию restore **не перезаписывает** более новый файл на клиенте (защита от
затирания свежих данных); используйте `-force`, чтобы перезаписать.

**Корзина.** Удалённые на клиенте файлы переносятся в `TRASH_DIR` с сохранением пути и
хранятся `TRASH_RETENTION_DAYS` дней (по умолчанию 10), затем удаляются безвозвратно.
Прежние версии лежат в скрытой подпапке `.versions/<seq>/` рядом.

---

## 9. Обновление и ротация

**Обновление бинарника:**

```bash
sudo systemctl stop backuper-server
sudo install -m0755 bin/backuper-server /usr/local/bin/backuper-server
sudo systemctl start backuper-server
```

**Ротация паролей/ключей (вручную):** замените значения `OWN_PASSWORD`/`PEER_PASSWORD`
в `.env` на обеих сторонах (сохраняя соответствие) и/или файлы сертификатов, затем
перезапустите обе службы. Несовпадение паролей или сертификатов даёт `AUTH_FAILED`
и письмо-алерт.

---

## 10. Типовые проблемы

| Симптом | Причина / решение |
|---|---|
| `AUTH_FAILED` в логах/письме | Не совпали пароли (`OWN`/`PEER`) или сертификаты не от общего CA. Проверьте соответствие паролей и `ca.crt` на обеих сторонах. |
| TLS-рукопожатие отклонено | SAN в `client.crt` не содержит `CLIENT_HOST`; перевыпустите `gen-certs -client-host <IP_клиента>`. |
| `check-config` падает | Не заполнены обязательные поля, нет каталогов или файлов сертификатов. Сообщение укажет конкретную проблему. |
| Клиент недоступен | Сервер делает `RETRY_COUNT` попыток, шлёт алерт и продолжает со следующего планового цикла. Проверьте сеть/порт/службу клиента. |
| Письма не приходят | Проверьте `SMTP_*` и `SMTP_SECURITY` (`none`/`starttls`/`tls`); сбой отправки пишется в лог уровня ERROR. |
| Диск > 90% | Немедленный алерт вне расписания; освободите место (`STORAGE_DIR`/корзина). |
| «Экземпляр уже запущен» | Сработала защита от двойного запуска (`LOCK_FILE`). Убедитесь, что не запущено два процесса. |
