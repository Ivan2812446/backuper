#!/usr/bin/env bash
#
# Backuper — установщик «всё в одном» для Linux (Debian-семейство).
# Сам скачивает готовый бинарник с GitHub Release и поднимает службу systemd.
#
# Использование:
#   sudo bash install.sh server
#   sudo bash install.sh client
# Или одной командой:
#   curl -fsSL https://raw.githubusercontent.com/Ivan2812446/backuper/main/install.sh | sudo bash -s server
#
# Необязательные переменные окружения (для полностью автоматической настройки):
#   OWN_PASSWORD, PEER_PASSWORD   пароли (server.OWN==client.PEER и наоборот, ≥24 симв.)
#   CLIENT_HOST                   (server) адрес клиента в LAN
#   SERVER_HOST                   (server) адрес сервера (для SAN сертификата)
#   BACKUP_DIR                    (client) каталог-источник бэкапа (по умолчанию /data)
#   VERSION                       тег релиза (по умолчанию latest)
#   NO_START=1                    не запускать службу автоматически
#
set -euo pipefail

REPO="Ivan2812446/backuper"
ROLE="${1:-}"
VERSION="${VERSION:-latest}"

CONF_DIR="/etc/backuper"
CERTS_DIR="$CONF_DIR/certs"
ENV_FILE="$CONF_DIR/.env"
BIN_DIR="/usr/local/bin"
LOG_DIR="/var/log/backuper"
LIB_DIR="/var/lib/backuper"
STORAGE_DIR="/srv/backuper/storage"
TRASH_DIR="/srv/backuper/trash"
TEMP_DIR="/srv/backuper/temp"
USER="backuper"

if [[ -t 1 ]]; then G=$'\033[32m'; Y=$'\033[33m'; R=$'\033[31m'; B=$'\033[36m'; N=$'\033[0m'; else G=""; Y=""; R=""; B=""; N=""; fi
info() { printf '%s[i]%s %s\n' "$B" "$N" "$*"; }
ok()   { printf '%s[+]%s %s\n' "$G" "$N" "$*"; }
warn() { printf '%s[!]%s %s\n' "$Y" "$N" "$*" >&2; }
die()  { printf '%s[x]%s %s\n' "$R" "$N" "$*" >&2; exit 1; }

[[ "$ROLE" == "server" || "$ROLE" == "client" ]] || die "укажите роль: server | client"
[[ "$(id -u)" -eq 0 ]] || die "запускайте от root (sudo)."

case "$(uname -m)" in
	x86_64|amd64) ARCH=amd64 ;;
	aarch64|arm64) ARCH=arm64 ;;
	*) die "неподдерживаемая архитектура: $(uname -m)" ;;
esac

ASSET="backuper-${ROLE}-linux-${ARCH}"
if [[ "$VERSION" == "latest" ]]; then
	URL="https://github.com/${REPO}/releases/latest/download/${ASSET}"
else
	URL="https://github.com/${REPO}/releases/download/${VERSION}/${ASSET}"
fi

dl() { # url dest
	if command -v curl >/dev/null 2>&1; then
		curl -fSL --retry 3 -o "$2" "$1"
	elif command -v wget >/dev/null 2>&1; then
		wget -O "$2" "$1"
	else
		die "нужен curl или wget для скачивания."
	fi
}

info "Установка Backuper ($ROLE, linux/$ARCH), версия $VERSION"

# 1. пользователь
if ! getent group "$USER" >/dev/null; then groupadd --system "$USER"; fi
if ! id -u "$USER" >/dev/null 2>&1; then
	useradd --system --gid "$USER" --no-create-home --home-dir "$LIB_DIR" --shell /usr/sbin/nologin --comment "Backuper" "$USER"
	ok "создан пользователь $USER"
fi

# 2. каталоги
install -d -m 0750 -o "$USER" -g "$USER" "$CONF_DIR" "$CERTS_DIR" "$LOG_DIR"
if [[ "$ROLE" == "server" ]]; then
	install -d -m 0750 -o "$USER" -g "$USER" "$LIB_DIR" "$STORAGE_DIR" "$TRASH_DIR" "$TEMP_DIR"
fi

# 3. бинарник
info "Скачивание $ASSET …"
TMP="$(mktemp)"
dl "$URL" "$TMP"
install -m 0755 -o root -g root "$TMP" "$BIN_DIR/backuper-${ROLE}"
rm -f "$TMP"
ok "бинарник установлен: $BIN_DIR/backuper-${ROLE}"

# 4. .env (не перезаписываем существующий)
OWN_PASSWORD="${OWN_PASSWORD:-}"
PEER_PASSWORD="${PEER_PASSWORD:-}"
have_pw=0; [[ -n "$OWN_PASSWORD" && -n "$PEER_PASSWORD" ]] && have_pw=1
[[ -n "$OWN_PASSWORD" ]]  || OWN_PASSWORD="ЗАМЕНИТЕ-собственный-пароль-не-короче-24"
[[ -n "$PEER_PASSWORD" ]] || PEER_PASSWORD="ЗАМЕНИТЕ-пароль-другой-стороны-не-короче-24"

if [[ -f "$ENV_FILE" ]]; then
	warn ".env уже существует — оставлен без изменений: $ENV_FILE"
else
	if [[ "$ROLE" == "server" ]]; then
		cat > "$ENV_FILE" <<EOF
CLIENT_HOST=${CLIENT_HOST:-ЗАМЕНИТЕ-адрес-клиента}
CLIENT_PORT=9000
TLS_CERT_FILE=$CERTS_DIR/server.crt
TLS_KEY_FILE=$CERTS_DIR/server.key
TLS_CA_FILE=$CERTS_DIR/ca.crt
OWN_PASSWORD=$OWN_PASSWORD
PEER_PASSWORD=$PEER_PASSWORD
STORAGE_DIR=$STORAGE_DIR
TRASH_DIR=$TRASH_DIR
TEMP_DIR=$TEMP_DIR
SYNC_INTERVAL=1h
PARALLEL_TRANSFERS=4
TRASH_RETENTION_DAYS=10
SMTP_HOST=${SMTP_HOST:-smtp.example.com}
SMTP_PORT=${SMTP_PORT:-587}
SMTP_FROM=${SMTP_FROM:-backuper@example.com}
SMTP_TO=${SMTP_TO:-admin@example.com}
SMTP_SECURITY=${SMTP_SECURITY:-starttls}
SMTP_USER=${SMTP_USER:-}
SMTP_PASSWORD=${SMTP_PASSWORD:-}
LOG_DIR=$LOG_DIR
AUDIT_LOG=$LOG_DIR/audit.jsonl
STATE_DB=$LIB_DIR/state.db
LOCK_FILE=/var/run/backuper.lock
LOG_LEVEL=INFO
TIMEZONE=Europe/Moscow
EOF
	else
		cat > "$ENV_FILE" <<EOF
LISTEN_HOST=0.0.0.0
LISTEN_PORT=9000
TLS_CERT_FILE=$CERTS_DIR/client.crt
TLS_KEY_FILE=$CERTS_DIR/client.key
TLS_CA_FILE=$CERTS_DIR/ca.crt
OWN_PASSWORD=$OWN_PASSWORD
PEER_PASSWORD=$PEER_PASSWORD
BACKUP_DIR=${BACKUP_DIR:-/data}
EXCLUDE_PATTERNS=*.tmp,*.lock,~\$*
LOG_DIR=$LOG_DIR
AUDIT_LOG=$LOG_DIR/audit.jsonl
LOG_LEVEL=INFO
TIMEZONE=Europe/Moscow
EOF
	fi
	chown "$USER:$USER" "$ENV_FILE"; chmod 600 "$ENV_FILE"
	ok "создан $ENV_FILE"
fi

# 5. сертификаты: на сервере можем сгенерировать весь набор, если задан CLIENT_HOST
if [[ "$ROLE" == "server" && -n "${CLIENT_HOST:-}" && ! -f "$CERTS_DIR/ca.crt" ]]; then
	SH="${SERVER_HOST:-$(hostname -I 2>/dev/null | awk '{print $1}')}"
	info "Генерация сертификатов (client-host=$CLIENT_HOST server-host=${SH:-localhost}) …"
	"$BIN_DIR/backuper-server" gen-certs -out "$CERTS_DIR" -client-host "$CLIENT_HOST" ${SH:+-server-host "$SH"} >/dev/null
	chown -R "$USER:$USER" "$CERTS_DIR"; chmod 600 "$CERTS_DIR"/*.key
	ok "сертификаты созданы в $CERTS_DIR"
	warn "СКОПИРУЙТЕ на клиента: $CERTS_DIR/{ca.crt,client.crt,client.key} → его $CERTS_DIR/"
fi

# 6. systemd-юнит
cat > "/etc/systemd/system/backuper-${ROLE}.service" <<EOF
[Unit]
Description=Backuper ${ROLE}
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$USER
Group=$USER
EnvironmentFile=-$ENV_FILE
WorkingDirectory=$([[ "$ROLE" == server ]] && echo "$LIB_DIR" || echo "$LOG_DIR")
ExecStart=$BIN_DIR/backuper-${ROLE} run
Restart=always
RestartSec=5
TimeoutStopSec=120
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
ok "systemd-юнит установлен: backuper-${ROLE}.service"

# 7. проверка и запуск
echo
have_certs=0; [[ -f "$CERTS_DIR/ca.crt" && -f "$CERTS_DIR/${ROLE}.crt" && -f "$CERTS_DIR/${ROLE}.key" ]] && have_certs=1
if "$BIN_DIR/backuper-${ROLE}" check-config >/dev/null 2>&1 && [[ "$have_pw" -eq 1 && "$have_certs" -eq 1 && "${NO_START:-0}" != "1" ]]; then
	systemctl enable --now "backuper-${ROLE}.service"
	ok "служба backuper-${ROLE} запущена и добавлена в автозапуск."
	info "Статус: systemctl status backuper-${ROLE}"
else
	warn "Служба НЕ запущена автоматически — нужно завершить настройку:"
	[[ "$have_pw" -ne 1 ]] && warn "  • задайте пароли OWN_PASSWORD/PEER_PASSWORD в $ENV_FILE (server.OWN==client.PEER и наоборот)"
	if [[ "$ROLE" == "server" ]]; then
		[[ -z "${CLIENT_HOST:-}" ]] && warn "  • укажите CLIENT_HOST в $ENV_FILE"
		[[ "$have_certs" -ne 1 ]] && warn "  • создайте сертификаты: backuper-server gen-certs -out $CERTS_DIR -client-host <IP_клиента> -server-host <IP_сервера>"
	else
		warn "  • положите в $CERTS_DIR файлы ca.crt, client.crt, client.key (с сервера) и chmod 600 *.key"
		warn "  • проверьте BACKUP_DIR в $ENV_FILE"
	fi
	info "После настройки: backuper-${ROLE} check-config && systemctl enable --now backuper-${ROLE}"
fi
echo
ok "Готово (роль $ROLE)."
