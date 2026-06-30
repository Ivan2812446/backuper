#!/usr/bin/env bash
#
# Backuper — интерактивный установщик для Linux (Debian-семейство).
# Сам скачивает готовый бинарник с GitHub Release, задаёт вопросы по каждому
# параметру .env (с пояснением, значением по умолчанию и вариантами выбора),
# создаёт службу systemd и (если всё готово) запускает её.
#
# Использование:
#   sudo bash install.sh server
#   sudo bash install.sh client
# Одной командой:
#   curl -fsSL https://raw.githubusercontent.com/Ivan2812446/backuper/main/install.sh | sudo bash -s server
#
# Неинтерактивный режим (без вопросов): задайте NONINTERACTIVE=1 и/или передайте
# значения через переменные окружения (OWN_PASSWORD, PEER_PASSWORD, CLIENT_HOST,
# BACKUP_DIR, …). Любая уже заданная переменная окружения не спрашивается.
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
USER="backuper"

if [[ -t 1 ]]; then G=$'\033[32m'; Y=$'\033[33m'; R=$'\033[31m'; B=$'\033[36m'; BOLD=$'\033[1m'; N=$'\033[0m'; else G=""; Y=""; R=""; B=""; BOLD=""; N=""; fi
info() { printf '%s[i]%s %s\n' "$B" "$N" "$*"; }
ok()   { printf '%s[+]%s %s\n' "$G" "$N" "$*"; }
warn() { printf '%s[!]%s %s\n' "$Y" "$N" "$*" >&2; }
die()  { printf '%s[x]%s %s\n' "$R" "$N" "$*" >&2; exit 1; }

[[ "$(id -u)" -eq 0 ]] || die "запускайте от root (sudo)."

case "$(uname -m)" in
	x86_64|amd64) ARCH=amd64 ;;
	aarch64|arm64) ARCH=arm64 ;;
	*) ARCH="" ;; # для uninstall арх не важен; для install/update проверим ниже
esac

# Обновление бинарника из последнего релиза: sudo bash install.sh update [server|client]
if [[ "$ROLE" == "update" ]]; then
	[[ -n "$ARCH" ]] || die "неподдерживаемая архитектура: $(uname -m)"
	target="${2:-}"; found=0
	for r in server client; do
		[[ -n "$target" && "$target" != "$r" ]] && continue
		[[ -f "$BIN_DIR/backuper-$r" ]] || continue
		found=1
		url="https://github.com/${REPO}/releases/latest/download/backuper-$r-linux-$ARCH"
		info "Обновление backuper-$r из релиза …"
		tmp="$(mktemp)"
		if command -v curl >/dev/null 2>&1; then curl -fSL --retry 3 -o "$tmp" "$url"
		elif command -v wget >/dev/null 2>&1; then wget -O "$tmp" "$url"
		else rm -f "$tmp"; die "нужен curl или wget"; fi
		[[ "$(wc -c <"$tmp")" -gt 1000000 ]] || { rm -f "$tmp"; die "скачанный файл повреждён/не найден"; }
		install -m 0755 -o root -g root "$tmp" "$BIN_DIR/backuper-$r"; rm -f "$tmp"
		ok "обновлён $BIN_DIR/backuper-$r"
		if systemctl is-active "backuper-$r" >/dev/null 2>&1; then
			systemctl restart "backuper-$r" && ok "служба backuper-$r перезапущена" || warn "не удалось перезапустить backuper-$r"
		fi
	done
	[[ $found -eq 1 ]] || die "не найдено установленных бинарников для обновления в $BIN_DIR"
	ok ".env и сертификаты сохранены без изменений."
	exit 0
fi

# Удаление: sudo bash install.sh uninstall [--purge]
if [[ "$ROLE" == "uninstall" ]]; then
	for r in server client; do
		if [[ -f "/etc/systemd/system/backuper-$r.service" ]]; then
			systemctl disable --now "backuper-$r" >/dev/null 2>&1 || true
			rm -f "/etc/systemd/system/backuper-$r.service"
			ok "удалена служба backuper-$r"
		fi
		[[ -f "$BIN_DIR/backuper-$r" ]] && rm -f "$BIN_DIR/backuper-$r" && ok "удалён $BIN_DIR/backuper-$r"
	done
	systemctl daemon-reload >/dev/null 2>&1 || true
	if [[ "${2:-}" == "--purge" || "${PURGE:-0}" == "1" ]]; then
		rm -rf "$CONF_DIR" "$LIB_DIR" "$LOG_DIR" /srv/backuper
		userdel "$USER" >/dev/null 2>&1 || true
		groupdel "$USER" >/dev/null 2>&1 || true
		ok "полностью удалено: бинарники, службы, конфиг, данные и пользователь $USER."
	else
		ok "удалены бинарники и службы. Данные СОХРАНЕНЫ: $CONF_DIR, $LIB_DIR, $LOG_DIR, /srv/backuper."
		info "Удалить вместе с данными: sudo bash install.sh uninstall --purge"
	fi
	exit 0
fi

[[ "$ROLE" == "server" || "$ROLE" == "client" ]] || die "укажите: server | client | uninstall | update"
[[ -n "$ARCH" ]] || die "неподдерживаемая архитектура: $(uname -m)"

# ---------------------------------------------------------------------------
# Интерактивный ввод. При curl|bash stdin занят скриптом, поэтому читаем из
# /dev/tty (или из файла BACKUPER_INPUT — для тестов). Нет терминала → режим
# без вопросов (значения берём из окружения/по умолчанию).
# ---------------------------------------------------------------------------
IN_SRC="${BACKUPER_INPUT:-/dev/tty}"
interactive=0
if [[ "${NONINTERACTIVE:-0}" != "1" ]] && exec 3<"$IN_SRC" 2>/dev/null; then
	interactive=1
fi

gen_password() { tr -dc 'A-Za-z0-9' </dev/urandom | head -c 32; }

# envset VAR — true, если переменная окружения VAR непустая (тогда не спрашиваем).
envset() { [[ -n "${!1:-}" ]]; }

# ask VAR "пояснение" "по_умолчанию"  — обычный вопрос со значением по умолчанию.
ask() {
	local var=$1 desc=$2 def=${3-} ans
	if envset "$var"; then return; fi
	if [[ $interactive -ne 1 ]]; then printf -v "$var" '%s' "$def"; return; fi
	printf '\n%s%s%s\n' "$BOLD" "$desc" "$N" >/dev/tty
	printf '  [по умолчанию: %s] (Enter — принять): ' "$def" >/dev/tty
	IFS= read -r ans <&3 || ans=""
	[[ -z "$ans" ]] && ans=$def
	printf -v "$var" '%s' "$ans"
}

# ask_opt VAR "пояснение"  — необязательный вопрос (Enter — пропустить, пусто).
ask_opt() {
	local var=$1 desc=$2 ans
	if envset "$var"; then return; fi
	if [[ $interactive -ne 1 ]]; then printf -v "$var" '%s' ""; return; fi
	printf '\n%s%s%s\n' "$BOLD" "$desc" "$N" >/dev/tty
	printf '  (Enter — пропустить): ' >/dev/tty
	IFS= read -r ans <&3 || ans=""
	printf -v "$var" '%s' "$ans"
}

# ask_required VAR "пояснение"  — обязательный, спрашиваем пока не введут.
ask_required() {
	local var=$1 desc=$2 ans
	if envset "$var"; then return; fi
	if [[ $interactive -ne 1 ]]; then printf -v "$var" '%s' ""; return; fi
	while :; do
		printf '\n%s%s%s\n' "$BOLD" "$desc" "$N" >/dev/tty
		printf '  значение (обязательно): ' >/dev/tty
		IFS= read -r ans <&3 || ans=""
		[[ -n "$ans" ]] && break
		warn "это поле обязательно"
	done
	printf -v "$var" '%s' "$ans"
}

# ask_password VAR "пояснение"  — пароль; Enter генерирует случайный (32 симв.).
ask_password() {
	local var=$1 desc=$2 ans
	if envset "$var"; then return; fi
	if [[ $interactive -ne 1 ]]; then printf -v "$var" '%s' "$(gen_password)"; return; fi
	printf '\n%s%s%s\n' "$BOLD" "$desc" "$N" >/dev/tty
	printf '  введите пароль (Enter — сгенерировать случайный): ' >/dev/tty
	IFS= read -rs ans <&3 || ans=""
	printf '\n' >/dev/tty
	[[ -z "$ans" ]] && { ans=$(gen_password); printf '  сгенерирован: %s\n' "$ans" >/dev/tty; }
	printf -v "$var" '%s' "$ans"
}

# ask_choice VAR "пояснение" "по_умолчанию" вариант...  — выбор номера/своего значения.
ask_choice() {
	local var=$1 desc=$2 def=$3; shift 3
	local opts=("$@") ans n
	if envset "$var"; then return; fi
	if [[ $interactive -ne 1 ]]; then printf -v "$var" '%s' "$def"; return; fi
	printf '\n%s%s%s\n' "$BOLD" "$desc" "$N" >/dev/tty
	for n in "${!opts[@]}"; do printf '  %d. %s\n' "$((n+1))" "${opts[n]}" >/dev/tty; done
	printf '  %d. другое значение\n' "$((${#opts[@]}+1))" >/dev/tty
	printf '  выберите номер или впишите своё [по умолчанию: %s] (Enter — принять): ' "$def" >/dev/tty
	IFS= read -r ans <&3 || ans=""
	if [[ -z "$ans" ]]; then printf -v "$var" '%s' "$def"; return; fi
	if [[ "$ans" =~ ^[0-9]+$ ]]; then
		if (( ans >= 1 && ans <= ${#opts[@]} )); then printf -v "$var" '%s' "${opts[ans-1]}"; return; fi
		if (( ans == ${#opts[@]}+1 )); then
			printf '  введите своё значение: ' >/dev/tty
			IFS= read -r ans <&3 || ans=""
			[[ -z "$ans" ]] && ans=$def
			printf -v "$var" '%s' "$ans"; return
		fi
	fi
	printf -v "$var" '%s' "$ans" # ввели текст напрямую
}

# ask_multi VAR "пояснение" "по_умолчанию(csv)" вариант...  — несколько через запятую.
ask_multi() {
	local var=$1 desc=$2 def=$3; shift 3
	local opts=("$@") ans tok out="" i
	if envset "$var"; then return; fi
	if [[ $interactive -ne 1 ]]; then printf -v "$var" '%s' "$def"; return; fi
	printf '\n%s%s%s\n' "$BOLD" "$desc" "$N" >/dev/tty
	for i in "${!opts[@]}"; do printf '  %d. %s\n' "$((i+1))" "${opts[i]}" >/dev/tty; done
	printf '  выберите один или несколько через запятую (напр. 1,3), либо впишите своё,\n' >/dev/tty
	printf '  0 — ничего [по умолчанию: %s] (Enter — принять): ' "${def:-пусто}" >/dev/tty
	IFS= read -r ans <&3 || ans=""
	if [[ -z "$ans" ]]; then printf -v "$var" '%s' "$def"; return; fi
	if [[ "$ans" == "0" ]]; then printf -v "$var" '%s' ""; return; fi
	if [[ "$ans" =~ ^[0-9,[:space:]]+$ ]]; then
		IFS=',' read -ra toks <<<"$ans"
		for tok in "${toks[@]}"; do
			tok=$(echo "$tok" | tr -d '[:space:]'); [[ -z "$tok" ]] && continue
			if (( tok >= 1 && tok <= ${#opts[@]} )); then out+="${out:+,}${opts[tok-1]}"; fi
		done
		printf -v "$var" '%s' "$out"; return
	fi
	printf -v "$var" '%s' "$ans" # текст напрямую
}

yesno() { # yesno "вопрос" default(y/n) → 0=yes 1=no
	local q=$1 def=${2:-y} ans
	if [[ $interactive -ne 1 ]]; then [[ "$def" == y ]] && return 0 || return 1; fi
	printf '\n%s%s%s [%s]: ' "$BOLD" "$q" "$N" "$([[ $def == y ]] && echo Y/n || echo y/N)" >/dev/tty
	IFS= read -r ans <&3 || ans=""
	ans=${ans:-$def}
	[[ "$ans" =~ ^[YyДд] ]]
}

info "Установка Backuper ($ROLE, linux/$ARCH), версия $VERSION"
[[ $interactive -eq 1 ]] && info "Интерактивный режим: отвечайте на вопросы (Enter — значение по умолчанию)." \
	|| info "Неинтерактивный режим: значения из окружения и по умолчанию."

dl() {
	if command -v curl >/dev/null 2>&1; then curl -fSL --retry 3 -o "$2" "$1"
	elif command -v wget >/dev/null 2>&1; then wget -O "$2" "$1"
	else die "нужен curl или wget для скачивания."; fi
}

# 1. пользователь
if ! getent group "$USER" >/dev/null; then groupadd --system "$USER"; fi
if ! id -u "$USER" >/dev/null 2>&1; then
	useradd --system --gid "$USER" --no-create-home --home-dir "$LIB_DIR" --shell /usr/sbin/nologin --comment "Backuper" "$USER"
	ok "создан пользователь $USER"
fi
install -d -m 0750 -o "$USER" -g "$USER" "$CONF_DIR" "$CERTS_DIR" "$LOG_DIR"
[[ "$ROLE" == "server" ]] && install -d -m 0750 -o "$USER" -g "$USER" "$LIB_DIR"

# 2. бинарник
ASSET="backuper-${ROLE}-linux-${ARCH}"
if [[ "$VERSION" == "latest" ]]; then URL="https://github.com/${REPO}/releases/latest/download/${ASSET}"
else URL="https://github.com/${REPO}/releases/download/${VERSION}/${ASSET}"; fi
info "Скачивание $ASSET …"
TMP="$(mktemp)"; dl "$URL" "$TMP"
install -m 0755 -o root -g root "$TMP" "$BIN_DIR/backuper-${ROLE}"; rm -f "$TMP"
ok "бинарник установлен: $BIN_DIR/backuper-${ROLE}"

# 3. опрос параметров и запись .env
write_env=1
if [[ -f "$ENV_FILE" ]]; then
	if yesno "Файл $ENV_FILE уже существует. Перезаписать?" n; then write_env=1; else write_env=0; warn "оставляю существующий $ENV_FILE"; fi
fi

if [[ $write_env -eq 1 ]]; then
	echo
	info "Настройка параметров (${BOLD}${ROLE}${N})"
	if [[ "$ROLE" == "server" ]]; then
		ask_required CLIENT_HOST "CLIENT_HOST — адрес КЛИЕНТА в локальной сети (IP или hostname), к которому сервер подключается за файлами."
		ask CLIENT_PORT "CLIENT_PORT — TCP-порт клиента." "9000"
		ask_password OWN_PASSWORD "OWN_PASSWORD — пароль ЭТОГО сервера (на клиенте он должен быть указан как PEER_PASSWORD). ≥24 символов."
		ask_password PEER_PASSWORD "PEER_PASSWORD — пароль КЛИЕНТА (то, что у клиента в OWN_PASSWORD)."
		ask STORAGE_DIR "STORAGE_DIR — куда складывать копии файлов." "/srv/backuper/storage"
		ask TRASH_DIR "TRASH_DIR — корзина для удалённых на клиенте файлов." "/srv/backuper/trash"
		ask TEMP_DIR "TEMP_DIR — временные файлы докачки." "/srv/backuper/temp"
		ask_choice SYNC_INTERVAL "SYNC_INTERVAL — как часто запускать сверку/бэкап." "1h" "15m" "1h" "6h" "24h"
		ask TRASH_RETENTION_DAYS "TRASH_RETENTION_DAYS — сколько дней хранить файлы в корзине." "10"
		ask_choice PARALLEL_TRANSFERS "PARALLEL_TRANSFERS — сколько файлов качать параллельно." "4" "2" "4" "8"
		ask BANDWIDTH_LIMIT "BANDWIDTH_LIMIT — лимит скорости, байт/с (0 — без лимита; можно 10MB, 1MiB)." "0"
		ask_choice TLS_MIN_VERSION "TLS_MIN_VERSION — минимальная версия TLS." "1.2" "1.2" "1.3"
		ask_choice LOG_LEVEL "LOG_LEVEL — уровень логирования." "INFO" "INFO" "DEBUG" "WARN" "ERROR"
		echo; info "Почта для алертов (SMTP)."
		if envset SMTP_HOST || { [[ $interactive -eq 1 ]] && yesno "Настроить отправку e-mail алертов сейчас?" y; }; then
			ask_required SMTP_HOST "SMTP_HOST — адрес SMTP-сервера (напр. smtp.gmail.com)."
			ask SMTP_PORT "SMTP_PORT — порт SMTP." "587"
			ask_choice SMTP_SECURITY "SMTP_SECURITY — шифрование соединения с SMTP." "starttls" "starttls" "tls" "none"
			ask_required SMTP_FROM "SMTP_FROM — адрес отправителя."
			ask_required SMTP_TO "SMTP_TO — получатели алертов (через запятую)."
			ask_opt SMTP_USER "SMTP_USER — логин SMTP (если требуется авторизация)."
			ask_opt SMTP_PASSWORD "SMTP_PASSWORD — пароль SMTP."
		else
			SMTP_HOST="ЗАМЕНИТЕ-smtp.example.com"; SMTP_PORT="587"; SMTP_SECURITY="starttls"
			SMTP_FROM="ЗАМЕНИТЕ-backuper@example.com"; SMTP_TO="ЗАМЕНИТЕ-admin@example.com"; SMTP_USER=""; SMTP_PASSWORD=""
			warn "SMTP пропущен — заполните позже в $ENV_FILE, иначе алерты не будут отправляться."
		fi

		umask 077
		cat > "$ENV_FILE" <<EOF
CLIENT_HOST=$CLIENT_HOST
CLIENT_PORT=$CLIENT_PORT
TLS_CERT_FILE=$CERTS_DIR/server.crt
TLS_KEY_FILE=$CERTS_DIR/server.key
TLS_CA_FILE=$CERTS_DIR/ca.crt
TLS_MIN_VERSION=$TLS_MIN_VERSION
OWN_PASSWORD=$OWN_PASSWORD
PEER_PASSWORD=$PEER_PASSWORD
STORAGE_DIR=$STORAGE_DIR
TRASH_DIR=$TRASH_DIR
TEMP_DIR=$TEMP_DIR
SYNC_INTERVAL=$SYNC_INTERVAL
PARALLEL_TRANSFERS=$PARALLEL_TRANSFERS
BANDWIDTH_LIMIT=$BANDWIDTH_LIMIT
TRASH_RETENTION_DAYS=$TRASH_RETENTION_DAYS
SMTP_HOST=$SMTP_HOST
SMTP_PORT=$SMTP_PORT
SMTP_SECURITY=$SMTP_SECURITY
SMTP_FROM=$SMTP_FROM
SMTP_TO=$SMTP_TO
SMTP_USER=$SMTP_USER
SMTP_PASSWORD=$SMTP_PASSWORD
LOG_DIR=$LOG_DIR
AUDIT_LOG=$LOG_DIR/audit.jsonl
STATE_DB=$LIB_DIR/state.db
LOCK_FILE=$LIB_DIR/backuper.lock
LOG_LEVEL=$LOG_LEVEL
TIMEZONE=Europe/Moscow
EOF
		umask 022
	else
		ask LISTEN_HOST "LISTEN_HOST — на каком интерфейсе слушать (0.0.0.0 — все)." "0.0.0.0"
		ask LISTEN_PORT "LISTEN_PORT — TCP-порт прослушивания." "9000"
		ask_password OWN_PASSWORD "OWN_PASSWORD — пароль ЭТОГО клиента (на сервере он должен быть указан как PEER_PASSWORD)."
		ask_password PEER_PASSWORD "PEER_PASSWORD — пароль СЕРВЕРА (то, что у сервера в OWN_PASSWORD)."
		ask_required BACKUP_DIR "BACKUP_DIR — каталог-источник, который бэкапим (он же цель восстановления)."
		ask_multi EXCLUDE_PATTERNS "EXCLUDE_PATTERNS — какие файлы НЕ бэкапить (маски)." "*.tmp,*.lock" "*.tmp" "*.lock" "~\$*" "*.bak"
		ask_opt INCLUDE_PATTERNS "INCLUDE_PATTERNS — бэкапить ТОЛЬКО эти маски (пусто — все файлы)."
		ask BANDWIDTH_LIMIT "BANDWIDTH_LIMIT — лимит скорости, байт/с (0 — без лимита)." "0"
		ask_choice TLS_MIN_VERSION "TLS_MIN_VERSION — минимальная версия TLS." "1.2" "1.2" "1.3"
		ask_choice LOG_LEVEL "LOG_LEVEL — уровень логирования." "INFO" "INFO" "DEBUG" "WARN" "ERROR"

		umask 077
		cat > "$ENV_FILE" <<EOF
LISTEN_HOST=$LISTEN_HOST
LISTEN_PORT=$LISTEN_PORT
TLS_CERT_FILE=$CERTS_DIR/client.crt
TLS_KEY_FILE=$CERTS_DIR/client.key
TLS_CA_FILE=$CERTS_DIR/ca.crt
TLS_MIN_VERSION=$TLS_MIN_VERSION
OWN_PASSWORD=$OWN_PASSWORD
PEER_PASSWORD=$PEER_PASSWORD
BACKUP_DIR=$BACKUP_DIR
INCLUDE_PATTERNS=$INCLUDE_PATTERNS
EXCLUDE_PATTERNS=$EXCLUDE_PATTERNS
BANDWIDTH_LIMIT=$BANDWIDTH_LIMIT
LOG_DIR=$LOG_DIR
AUDIT_LOG=$LOG_DIR/audit.jsonl
LOG_LEVEL=$LOG_LEVEL
TIMEZONE=Europe/Moscow
EOF
		umask 022
	fi
	chown "$USER:$USER" "$ENV_FILE"; chmod 600 "$ENV_FILE"
	ok "конфигурация записана: $ENV_FILE"
fi

# 4. сертификаты (на сервере можем сгенерировать весь набор)
if [[ "$ROLE" == "server" && ! -f "$CERTS_DIR/ca.crt" ]]; then
	if yesno "Сгенерировать TLS-сертификаты сейчас?" y; then
		ch="${CLIENT_HOST:-}"
		[[ -z "$ch" ]] && ask_required CLIENT_HOST_FOR_CERT "Адрес клиента (SAN client.crt)" && ch="$CLIENT_HOST_FOR_CERT"
		defip="$(hostname -I 2>/dev/null | awk '{print $1}')"
		SERVER_HOST="${SERVER_HOST:-}"
		ask SERVER_HOST "SERVER_HOST — адрес ЭТОГО сервера для SAN сертификата." "${defip:-127.0.0.1}"
		"$BIN_DIR/backuper-server" gen-certs -out "$CERTS_DIR" -client-host "$ch" ${SERVER_HOST:+-server-host "$SERVER_HOST"} >/dev/null
		chown -R "$USER:$USER" "$CERTS_DIR"; chmod 600 "$CERTS_DIR"/*.key
		ok "сертификаты созданы в $CERTS_DIR"
		warn "СКОПИРУЙТЕ на клиента: $CERTS_DIR/{ca.crt,client.crt,client.key} → его $CERTS_DIR/"
	fi
fi

# 5. systemd-юнит (с усилением безопасности и ReadWritePaths по реальным путям из .env)
get_env_val() { grep -E "^$1=" "$ENV_FILE" 2>/dev/null | head -1 | cut -d= -f2-; }
declare -a RWP
if [[ "$ROLE" == server ]]; then
	RWP=("$(get_env_val STORAGE_DIR)" "$(get_env_val TRASH_DIR)" "$(get_env_val TEMP_DIR)" "$LIB_DIR" "$LOG_DIR")
else
	RWP=("$(get_env_val BACKUP_DIR)" "$LOG_DIR")
fi
rwp=""
for p in "${RWP[@]}"; do [[ -n "$p" ]] && rwp+="${rwp:+ }$p"; done

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
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
RestrictRealtime=true
LockPersonality=true
ReadWritePaths=$rwp

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
ok "systemd-юнит установлен: backuper-${ROLE}.service"

[[ $interactive -eq 1 ]] && exec 3<&- || true

# 6. проверка и запуск
echo
have_certs=0; [[ -f "$CERTS_DIR/ca.crt" && -f "$CERTS_DIR/${ROLE}.crt" && -f "$CERTS_DIR/${ROLE}.key" ]] && have_certs=1
if "$BIN_DIR/backuper-${ROLE}" check-config >/dev/null 2>&1 && [[ "$have_certs" -eq 1 ]]; then
	systemctl enable --now "backuper-${ROLE}.service"
	ok "служба backuper-${ROLE} запущена и в автозапуске."
	info "Статус: systemctl status backuper-${ROLE}   |   Логи: journalctl -u backuper-${ROLE} -f"
else
	warn "Служба пока НЕ запущена — нужно завершить настройку:"
	"$BIN_DIR/backuper-${ROLE}" check-config 2>&1 | sed 's/^/    /' || true
	if [[ "$ROLE" == "client" && "$have_certs" -ne 1 ]]; then
		warn "  • положите в $CERTS_DIR файлы ca.crt, client.crt, client.key (с сервера), затем: chmod 600 $CERTS_DIR/*.key"
	fi
	info "После настройки: backuper-${ROLE} check-config && systemctl enable --now backuper-${ROLE}"
fi
echo
ok "Готово (роль $ROLE)."
