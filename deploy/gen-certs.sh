#!/usr/bin/env bash
#
# gen-certs.sh — генерация mTLS-сертификатов для Backuper.
#
# Создаёт набор файлов в каталоге вывода:
#   ca.crt, ca.key        — корневой УЦ (подписывает оба сертификата)
#   server.crt, server.key — сертификат СЕРВЕРА (инициатор/dialer)
#   client.crt, client.key — сертификат КЛИЕНТА (TLS-слушатель)
#
# Роли TLS (раздел 6 ТЗ):
#   backuper-client — TLS-СЛУШАТЕЛЬ. Его сертификат — client.crt.
#     SAN client.crt ОБЯЗАН содержать адрес клиента в LAN (= CLIENT_HOST у сервера),
#     потому что сервер при подключении сверяет SAN сертификата клиента с CLIENT_HOST.
#   backuper-server — TLS-ИНИЦИАТОР/dialer. Его сертификат — server.crt.
#   ca.crt раскладывается ОБЕИМ сторонам; приватные ключи (*.key) — только своей стороне.
#
# Предпочтительно вызывается собранный бинарник `backuper-server gen-certs`
# (из ./bin рядом со скриптом/репозиторием или из PATH). Если бинарник не найден —
# используется openssl-фоллбэк, создающий те же файлы с теми же SAN.
#
# Использование:
#   gen-certs.sh -c CLIENT_HOST [-o OUTDIR] [-s SERVER_HOST]
#
#   -o OUTDIR        каталог вывода (по умолчанию ./certs)
#   -c CLIENT_HOST   IP/hostname клиента-слушателя для SAN (через запятую) — ОБЯЗАТЕЛЬНО
#   -s SERVER_HOST   IP/hostname сервера для SAN (через запятую) — необязательно
#
# Пример:
#   ./gen-certs.sh -o certs -c 192.168.1.50 -s 192.168.1.10
#
set -euo pipefail

# --- Константы ---
readonly DAYS=3650
readonly RSA_BITS=2048

# --- Каталог, где лежит сам скрипт (для поиска ./bin) ---
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"
readonly SCRIPT_DIR

# --- Значения по умолчанию ---
OUTDIR="./certs"
CLIENT_HOST=""
SERVER_HOST=""

# --- Вспомогательные функции вывода ---
info()  { printf '\033[1;34m[i]\033[0m %s\n' "$*"; }
ok()    { printf '\033[1;32m[+]\033[0m %s\n' "$*"; }
warn()  { printf '\033[1;33m[!]\033[0m %s\n' "$*" >&2; }
die()   { printf '\033[1;31m[x] ОШИБКА:\033[0m %s\n' "$*" >&2; exit 1; }

usage() {
	cat >&2 <<'EOF'
Использование: gen-certs.sh -c CLIENT_HOST [-o OUTDIR] [-s SERVER_HOST]

  -o OUTDIR        каталог вывода (по умолчанию ./certs)
  -c CLIENT_HOST   IP/hostname клиента-слушателя для SAN (через запятую) — ОБЯЗАТЕЛЬНО
  -s SERVER_HOST   IP/hostname сервера для SAN (через запятую) — необязательно
  -h               показать эту справку

Пример:
  ./gen-certs.sh -o certs -c 192.168.1.50 -s 192.168.1.10
EOF
}

# --- Разбор аргументов ---
while getopts ":o:c:s:h" opt; do
	case "$opt" in
		o) OUTDIR="$OPTARG" ;;
		c) CLIENT_HOST="$OPTARG" ;;
		s) SERVER_HOST="$OPTARG" ;;
		h) usage; exit 0 ;;
		:) die "опция -$OPTARG требует аргумент. См. -h" ;;
		\?) die "неизвестная опция -$OPTARG. См. -h" ;;
	esac
done

# --- Проверка обязательных аргументов ---
if [[ -z "$CLIENT_HOST" ]]; then
	usage
	die "не задан адрес клиента (-c CLIENT_HOST). Это адрес клиента в LAN, он же CLIENT_HOST у сервера."
fi

# --- Поиск бинарника backuper-server ---
# Приоритет: ./bin рядом со скриптом, ./bin рядом с репозиторием (родитель deploy/), затем PATH.
find_binary() {
	local candidate
	for candidate in \
		"$SCRIPT_DIR/bin/backuper-server" \
		"$SCRIPT_DIR/../bin/backuper-server"; do
		if [[ -x "$candidate" ]]; then
			# Нормализуем путь
			( cd "$(dirname "$candidate")" >/dev/null 2>&1 && printf '%s/%s\n' "$(pwd)" "$(basename "$candidate")" )
			return 0
		fi
	done
	if command -v backuper-server >/dev/null 2>&1; then
		command -v backuper-server
		return 0
	fi
	return 1
}

# --- Генерация через бинарник ---
gen_with_binary() {
	local bin="$1"
	info "Найден бинарник: $bin"
	info "Генерация сертификатов через backuper-server gen-certs ..."
	local args=( gen-certs -out "$OUTDIR" -client-host "$CLIENT_HOST" -days "$DAYS" )
	if [[ -n "$SERVER_HOST" ]]; then
		args+=( -server-host "$SERVER_HOST" )
	fi
	"$bin" "${args[@]}"
}

# --- openssl-фоллбэк ---
# Формирует строку SAN из списка хостов "h1,h2,..." с автоопределением IP vs DNS.
build_san() {
	local hosts="$1" ip_re='^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$'
	local out="" h dns_n=0 ip_n=0
	local IFS=','
	for h in $hosts; do
		# обрезаем пробелы
		h="${h#"${h%%[![:space:]]*}"}"
		h="${h%"${h##*[![:space:]]}"}"
		[[ -z "$h" ]] && continue
		if [[ "$h" =~ $ip_re ]]; then
			ip_n=$((ip_n + 1))
			out+="${out:+,}IP:${h}"
		else
			dns_n=$((dns_n + 1))
			out+="${out:+,}DNS:${h}"
		fi
	done
	[[ -z "$out" ]] && return 1
	printf '%s\n' "$out"
}

gen_with_openssl() {
	warn "Бинарник backuper-server не найден — используется openssl-фоллбэк."
	command -v openssl >/dev/null 2>&1 || die "openssl не найден в PATH и бинарник backuper-server отсутствует."
	info "Генерация сертификатов через openssl (RSA-${RSA_BITS}, срок ${DAYS} дн.) ..."

	local client_san server_san
	client_san="$(build_san "$CLIENT_HOST")" || die "не удалось построить SAN для клиента из '-c $CLIENT_HOST'"

	# --- Корневой УЦ ---
	info "1/3 Корневой УЦ (ca.crt / ca.key) ..."
	openssl genrsa -out "$OUTDIR/ca.key" "$RSA_BITS" 2>/dev/null
	openssl req -x509 -new -nodes -key "$OUTDIR/ca.key" \
		-sha256 -days "$DAYS" \
		-subj "/CN=Backuper Root CA/O=Backuper" \
		-out "$OUTDIR/ca.crt" 2>/dev/null

	# --- Сертификат КЛИЕНТА (TLS-слушатель), SAN = адрес клиента ---
	info "2/3 Сертификат клиента (client.crt / client.key), SAN: ${client_san} ..."
	openssl genrsa -out "$OUTDIR/client.key" "$RSA_BITS" 2>/dev/null
	openssl req -new -key "$OUTDIR/client.key" \
		-subj "/CN=backuper-client/O=Backuper" \
		-out "$OUTDIR/client.csr" 2>/dev/null
	{
		printf 'basicConstraints=CA:FALSE\n'
		printf 'keyUsage=critical,digitalSignature,keyEncipherment\n'
		printf 'extendedKeyUsage=serverAuth,clientAuth\n'
		printf 'subjectAltName=%s\n' "$client_san"
	} > "$OUTDIR/client.ext"
	openssl x509 -req -in "$OUTDIR/client.csr" \
		-CA "$OUTDIR/ca.crt" -CAkey "$OUTDIR/ca.key" -CAcreateserial \
		-sha256 -days "$DAYS" \
		-extfile "$OUTDIR/client.ext" \
		-out "$OUTDIR/client.crt" 2>/dev/null

	# --- Сертификат СЕРВЕРА (инициатор/dialer) ---
	info "3/3 Сертификат сервера (server.crt / server.key) ..."
	openssl genrsa -out "$OUTDIR/server.key" "$RSA_BITS" 2>/dev/null
	openssl req -new -key "$OUTDIR/server.key" \
		-subj "/CN=backuper-server/O=Backuper" \
		-out "$OUTDIR/server.csr" 2>/dev/null
	{
		printf 'basicConstraints=CA:FALSE\n'
		printf 'keyUsage=critical,digitalSignature,keyEncipherment\n'
		printf 'extendedKeyUsage=serverAuth,clientAuth\n'
		if [[ -n "$SERVER_HOST" ]] && server_san="$(build_san "$SERVER_HOST")"; then
			printf 'subjectAltName=%s\n' "$server_san"
		fi
	} > "$OUTDIR/server.ext"
	openssl x509 -req -in "$OUTDIR/server.csr" \
		-CA "$OUTDIR/ca.crt" -CAkey "$OUTDIR/ca.key" -CAcreateserial \
		-sha256 -days "$DAYS" \
		-extfile "$OUTDIR/server.ext" \
		-out "$OUTDIR/server.crt" 2>/dev/null

	# --- Уборка временных файлов ---
	rm -f "$OUTDIR"/client.csr "$OUTDIR"/server.csr \
	      "$OUTDIR"/client.ext "$OUTDIR"/server.ext \
	      "$OUTDIR"/ca.srl
}

# --- Подготовка каталога вывода ---
mkdir -p "$OUTDIR"
OUTDIR="$(cd "$OUTDIR" >/dev/null 2>&1 && pwd)" || die "не удалось перейти в каталог вывода: $OUTDIR"

info "Каталог вывода: $OUTDIR"
info "Адрес клиента (SAN client.crt): $CLIENT_HOST"
[[ -n "$SERVER_HOST" ]] && info "Адрес сервера (SAN server.crt): $SERVER_HOST"

# --- Выбор способа генерации ---
if BIN="$(find_binary)"; then
	gen_with_binary "$BIN"
else
	gen_with_openssl
fi

# --- Проверка результата ---
missing=()
for f in ca.crt ca.key server.crt server.key client.crt client.key; do
	[[ -s "$OUTDIR/$f" ]] || missing+=("$f")
done
if (( ${#missing[@]} > 0 )); then
	die "не созданы файлы: ${missing[*]}"
fi

# --- Безопасные права: ключи только владельцу, сертификаты читаемы ---
chmod 600 "$OUTDIR"/ca.key "$OUTDIR"/server.key "$OUTDIR"/client.key
chmod 644 "$OUTDIR"/ca.crt "$OUTDIR"/server.crt "$OUTDIR"/client.crt

ok "Сертификаты успешно созданы в: $OUTDIR"
echo
echo "  ┌─ СЕРВЕРУ положить: ca.crt, server.crt, server.key"
echo "  └─ КЛИЕНТУ положить: ca.crt, client.crt, client.key"
echo
warn "Приватные ключи (*.key) НЕ передавайте чужой стороне. ca.crt — общий для обоих."
echo
echo "В .env сервера:  TLS_CERT_FILE=.../server.crt  TLS_KEY_FILE=.../server.key  TLS_CA_FILE=.../ca.crt"
echo "В .env клиента:  TLS_CERT_FILE=.../client.crt  TLS_KEY_FILE=.../client.key  TLS_CA_FILE=.../ca.crt"
