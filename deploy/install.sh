#!/usr/bin/env bash
#
# install.sh — установка Backuper на Linux (Debian-семейство), раздел 24 ТЗ.
#
# Использование:
#   sudo deploy/install.sh server
#   sudo deploy/install.sh client
#
# Скрипт:
#   - создаёт системного пользователя backuper (без shell), если его нет;
#   - создаёт рабочие каталоги с владельцем backuper;
#   - копирует собранный бинарник из ./bin в /usr/local/bin;
#   - ставит .env из примера в /etc/backuper/.env (права 600), НЕ перезаписывая существующий;
#   - устанавливает systemd-юнит из ./deploy/systemd;
#   - выполняет daemon-reload и backuper-<role> check-config.
#
# Скрипт НЕ запускает службу (enable --now) — нужная команда выводится в конце.

set -euo pipefail

# ----------------------------------------------------------------------------
# Константы
# ----------------------------------------------------------------------------
readonly SERVICE_USER="backuper"
readonly SERVICE_GROUP="backuper"
readonly BIN_DIR="/usr/local/bin"
readonly CONF_DIR="/etc/backuper"
readonly CERTS_DIR="/etc/backuper/certs"
readonly ENV_FILE="/etc/backuper/.env"
readonly LOG_DIR="/var/log/backuper"
readonly LIB_DIR="/var/lib/backuper"
readonly SYSTEMD_DIR="/etc/systemd/system"

# Каталоги по умолчанию для сервера (см. .env.server.example).
readonly STORAGE_DIR="/srv/backuper/storage"
readonly TRASH_DIR="/srv/backuper/trash"
readonly TEMP_DIR="/srv/backuper/temp"

# Каталог запуска скрипта (корень репозитория = на уровень выше deploy/).
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
readonly REPO_ROOT

# ----------------------------------------------------------------------------
# Вывод
# ----------------------------------------------------------------------------
if [[ -t 1 ]]; then
    C_RED=$'\033[31m'; C_GRN=$'\033[32m'; C_YLW=$'\033[33m'; C_BLU=$'\033[36m'; C_RST=$'\033[0m'
else
    C_RED=''; C_GRN=''; C_YLW=''; C_BLU=''; C_RST=''
fi

info()  { printf '%s[ИНФО]%s  %s\n'    "${C_BLU}" "${C_RST}" "$*"; }
ok()    { printf '%s[ОК]%s    %s\n'    "${C_GRN}" "${C_RST}" "$*"; }
warn()  { printf '%s[ВНИМАНИЕ]%s %s\n' "${C_YLW}" "${C_RST}" "$*" >&2; }
err()   { printf '%s[ОШИБКА]%s %s\n'  "${C_RED}" "${C_RST}" "$*" >&2; }

die() { err "$*"; exit 1; }

# ----------------------------------------------------------------------------
# Проверки окружения
# ----------------------------------------------------------------------------
require_root() {
    if [[ "${EUID}" -ne 0 ]]; then
        die "Скрипт нужно запускать от root. Пример: sudo deploy/install.sh ${ROLE:-server|client}"
    fi
}

usage() {
    cat <<EOF
Использование:
  sudo deploy/install.sh server   — установка серверной части
  sudo deploy/install.sh client   — установка клиентской части
EOF
}

# ----------------------------------------------------------------------------
# Шаги установки
# ----------------------------------------------------------------------------

# Создать системного пользователя backuper (без shell, без домашнего каталога),
# если его ещё нет.
create_user() {
    if getent group "${SERVICE_GROUP}" >/dev/null 2>&1; then
        info "Группа '${SERVICE_GROUP}' уже существует."
    else
        groupadd --system "${SERVICE_GROUP}"
        ok "Создана системная группа '${SERVICE_GROUP}'."
    fi

    if id -u "${SERVICE_USER}" >/dev/null 2>&1; then
        info "Пользователь '${SERVICE_USER}' уже существует."
    else
        useradd --system \
                --gid "${SERVICE_GROUP}" \
                --no-create-home \
                --home-dir "${LIB_DIR}" \
                --shell /usr/sbin/nologin \
                --comment "Backuper service account" \
                "${SERVICE_USER}"
        ok "Создан системный пользователь '${SERVICE_USER}' (без shell)."
    fi
}

# Создать каталог с владельцем backuper и заданными правами.
make_dir() {
    local dir="$1" mode="$2"
    install -d -o "${SERVICE_USER}" -g "${SERVICE_GROUP}" -m "${mode}" "${dir}"
    ok "Каталог ${dir} (владелец ${SERVICE_USER}, права ${mode})."
}

create_dirs_common() {
    # /etc/backuper и /etc/backuper/certs — конфигурация и сертификаты.
    make_dir "${CONF_DIR}"  "0750"
    make_dir "${CERTS_DIR}" "0750"
    make_dir "${LOG_DIR}"   "0750"
}

create_dirs_server() {
    create_dirs_common
    make_dir "${LIB_DIR}"     "0750"
    make_dir "${STORAGE_DIR}" "0750"
    make_dir "${TRASH_DIR}"   "0750"
    make_dir "${TEMP_DIR}"    "0750"
}

create_dirs_client() {
    create_dirs_common
}

# Скопировать собранный бинарник из ./bin в /usr/local/bin.
install_binary() {
    local name="$1"
    local src="${REPO_ROOT}/bin/${name}"
    local dst="${BIN_DIR}/${name}"

    if [[ ! -f "${src}" ]]; then
        die "Не найден бинарник ${src}. Сначала соберите проект:
       go build -o bin/${name} ./cmd/${ROLE}"
    fi

    install -o root -g root -m 0755 "${src}" "${dst}"
    ok "Бинарник установлен: ${dst}"
}

# Поставить .env из примера, НЕ перезаписывая существующий. Права 600, владелец backuper.
install_env() {
    local example="${REPO_ROOT}/.env.${ROLE}.example"

    if [[ ! -f "${example}" ]]; then
        die "Не найден пример конфигурации ${example}."
    fi

    if [[ -f "${ENV_FILE}" ]]; then
        warn "Файл ${ENV_FILE} уже существует — оставлен без изменений (не перезаписан)."
        warn "Сверьте его с примером ${example}, если в новой версии появились параметры."
        return 0
    fi

    install -o "${SERVICE_USER}" -g "${SERVICE_GROUP}" -m 0600 "${example}" "${ENV_FILE}"
    ok "Конфигурация установлена: ${ENV_FILE} (права 600, владелец ${SERVICE_USER})."
    ENV_WAS_CREATED=1
}

# Установить systemd-юнит из ./deploy/systemd.
install_unit() {
    local unit="backuper-${ROLE}.service"
    local src="${SCRIPT_DIR}/systemd/${unit}"
    local dst="${SYSTEMD_DIR}/${unit}"

    if [[ ! -f "${src}" ]]; then
        die "Не найден systemd-юнит ${src}.
       Ожидается файл ${unit} в каталоге deploy/systemd."
    fi

    install -o root -g root -m 0644 "${src}" "${dst}"
    ok "systemd-юнит установлен: ${dst}"

    systemctl daemon-reload
    ok "Выполнен systemctl daemon-reload."
}

# Проверка конфигурации соответствующей ролью.
run_check_config() {
    local bin="${BIN_DIR}/backuper-${ROLE}"
    info "Проверка конфигурации: ${bin} check-config"
    if "${bin}" check-config; then
        ok "check-config пройдена."
    else
        warn "check-config завершилась с ошибкой."
        warn "Скорее всего, требуется заполнить ${ENV_FILE} (пароли, адреса) и положить сертификаты в ${CERTS_DIR}."
        CHECK_FAILED=1
    fi
}

# Итоговые подсказки администратору.
print_reminders() {
    local unit="backuper-${ROLE}.service"

    echo
    printf '%s================ ЧТО СДЕЛАТЬ ДАЛЬШЕ ================%s\n' "${C_YLW}" "${C_RST}"
    echo

    if [[ "${ENV_WAS_CREATED:-0}" -eq 1 ]]; then
        warn "ОБЯЗАТЕЛЬНО отредактируйте ${ENV_FILE} перед запуском:"
        warn "  - ЗАМЕНИТЕ пароли OWN_PASSWORD и PEER_PASSWORD (не короче 24 символов)."
        warn "    Правило: server.OWN_PASSWORD == client.PEER_PASSWORD и наоборот."
        if [[ "${ROLE}" == "server" ]]; then
            warn "  - Укажите реальный CLIENT_HOST (адрес клиента в LAN) и параметры SMTP."
        else
            warn "  - Проверьте LISTEN_HOST/LISTEN_PORT и BACKUP_DIR."
        fi
    else
        warn "ЗАМЕНИТЕ пароли в ${ENV_FILE}, если они ещё стоят по умолчанию"
        warn "(OWN_PASSWORD/PEER_PASSWORD, не короче 24 символов)."
    fi

    echo
    warn "Положите сертификаты mTLS в ${CERTS_DIR} (НЕ используйте примеры/чужие ключи!):"
    if [[ "${ROLE}" == "server" ]]; then
        warn "  - server.crt, server.key (сертификат сервера-инициатора),"
        warn "  - ca.crt (общий для обеих сторон)."
        warn "  Генерация: backuper-server gen-certs -out certs \\"
        warn "             -client-host <IP_клиента> -server-host <IP_сервера>"
        warn "  SAN в client.crt должен содержать адрес клиента (CLIENT_HOST)."
    else
        warn "  - client.crt, client.key (сертификат клиента-слушателя; SAN = адрес клиента в LAN),"
        warn "  - ca.crt (общий для обеих сторон)."
        warn "  Приватный ключ server.key на клиент НЕ копируется."
    fi
    warn "Установите владельца и права на сертификаты:"
    warn "  chown -R ${SERVICE_USER}:${SERVICE_GROUP} ${CERTS_DIR} && chmod 600 ${CERTS_DIR}/*.key"

    echo
    info "После настройки повторите проверку:"
    info "  backuper-${ROLE} check-config"
    echo
    info "Запуск службы с автозапуском (выполните вручную):"
    printf '  %ssudo systemctl enable --now %s%s\n' "${C_GRN}" "${unit}" "${C_RST}"
    echo
    info "Состояние и логи:"
    info "  systemctl status ${unit}"
    info "  journalctl -u ${unit} -f"

    if [[ "${CHECK_FAILED:-0}" -eq 1 ]]; then
        echo
        warn "check-config пока не проходит — служба не запустится, пока конфигурация неполна."
    fi
}

# ----------------------------------------------------------------------------
# main
# ----------------------------------------------------------------------------
main() {
    if [[ $# -ne 1 ]]; then
        usage
        exit 1
    fi

    ROLE="$1"
    case "${ROLE}" in
        server|client) ;;
        -h|--help|help) usage; exit 0 ;;
        *) err "Неизвестная роль: '${ROLE}'."; usage; exit 1 ;;
    esac
    readonly ROLE

    require_root

    info "Установка Backuper, роль: ${ROLE}."
    info "Корень репозитория: ${REPO_ROOT}"
    echo

    create_user

    if [[ "${ROLE}" == "server" ]]; then
        create_dirs_server
    else
        create_dirs_client
    fi

    install_binary "backuper-${ROLE}"
    install_env
    install_unit
    run_check_config

    print_reminders

    echo
    ok "Установка роли '${ROLE}' завершена."
}

main "$@"
