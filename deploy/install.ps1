<#
.SYNOPSIS
    Установка Backuper как службы Windows (Windows 10+).
    Реализует разделы 19.2 и 24.5 ТЗ.

.DESCRIPTION
    Скрипт регистрирует backuper-server.exe или backuper-client.exe как
    Windows Service через sc.exe create, настраивает автозапуск (start= auto)
    и автоперезапуск при сбое (recovery actions, sc.exe failure).
    Перед регистрацией раскладывает файлы в C:\Backuper\ (бинарник, .env,
    сертификаты) и выполняет проверку конфигурации (check-config).

    Сервер  = TLS-инициатор (dialer), его сертификат — server.crt/server.key.
    Клиент  = TLS-слушатель (listener), его сертификат — client.crt/client.key.
    Файл ca.crt кладётся обеим сторонам.

.PARAMETER Role
    Роль устанавливаемого узла: server или client.

.PARAMETER InstallDir
    Каталог установки. По умолчанию C:\Backuper.

.PARAMETER SourceDir
    Каталог-источник, где лежат заранее подготовленные файлы для копирования
    (backuper-<role>.exe, .env, certs\). По умолчанию — каталог самого скрипта.

.PARAMETER ServiceName
    Имя службы Windows. По умолчанию backuper-server / backuper-client.

.PARAMETER SkipCheckConfig
    Пропустить выполнение check-config (например, если .env ещё не готов).

.EXAMPLE
    # Установка сервера из текущего каталога
    .\install.ps1 -Role server

.EXAMPLE
    # Установка клиента с явным указанием источника
    .\install.ps1 -Role client -SourceDir D:\dist

.NOTES
    Запускать в консоли PowerShell от имени Администратора.
    После установки положите боевой .env (см. .env.server.example /
    .env.client.example) и сертификаты gen-certs в C:\Backuper\.
#>

[CmdletBinding()]
param(
    # Роль узла: только server или client.
    [Parameter(Mandatory = $true)]
    [ValidateSet('server', 'client')]
    [string]$Role,

    # Каталог установки (бинарник, .env, сертификаты).
    [string]$InstallDir = 'C:\Backuper',

    # Каталог-источник с подготовленными файлами.
    [string]$SourceDir = $PSScriptRoot,

    # Имя службы Windows (по умолчанию backuper-<role>).
    [string]$ServiceName,

    # Не запускать check-config после установки.
    [switch]$SkipCheckConfig
)

# Строгий режим: останавливаемся на первой же необработанной ошибке.
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# --- Вспомогательные функции вывода ---------------------------------------

function Write-Info  { param([string]$Message) Write-Host "[INFO]  $Message"  -ForegroundColor Cyan }
function Write-Ok    { param([string]$Message) Write-Host "[OK]    $Message"  -ForegroundColor Green }
function Write-Warn2 { param([string]$Message) Write-Host "[WARN]  $Message"  -ForegroundColor Yellow }
function Fail        { param([string]$Message) Write-Host "[ОШИБКА] $Message" -ForegroundColor Red; exit 1 }

# --- Проверка прав администратора -----------------------------------------
# Регистрация службы и настройка recovery требуют прав администратора.

$identity  = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-Object Security.Principal.WindowsPrincipal($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Fail 'Скрипт нужно запускать от имени Администратора (повышенные права обязательны).'
}

# --- Производные значения по роли -----------------------------------------

# Имя исполняемого файла зависит от роли.
$exeName = "backuper-$Role.exe"

# Имя службы по умолчанию — backuper-<role>.
if ([string]::IsNullOrWhiteSpace($ServiceName)) {
    $ServiceName = "backuper-$Role"
}

# Человекочитаемое отображаемое имя и описание службы.
if ($Role -eq 'server') {
    $displayName = 'Backuper Server'
    $description = 'Backuper: сервер резервного копирования (TLS-инициатор, периодическая сверка и загрузка).'
} else {
    $displayName = 'Backuper Client'
    $description = 'Backuper: клиент резервного копирования (TLS-слушатель, отдаёт файлы по запросу сервера).'
}

# --- Пути ------------------------------------------------------------------

# Целевые пути внутри каталога установки.
$destExe   = Join-Path $InstallDir $exeName
$destEnv   = Join-Path $InstallDir '.env'
$certsDir  = Join-Path $InstallDir 'certs'

# Файл .env лежит рядом с бинарником (раздел 19.2: «.env рядом с бинарником»),
# поэтому именно его путь передаём в -config всем подкомандам.
$configArg = $destEnv

Write-Info "Роль:            $Role"
Write-Info "Служба:          $ServiceName ($displayName)"
Write-Info "Каталог:         $InstallDir"
Write-Info "Источник:        $SourceDir"
Write-Info "Бинарник:        $exeName"

# --- Создание каталога установки C:\Backuper\ ------------------------------
# Раздел 24.5, п.1: каталог для бинарника, .env и сертификатов.

if (-not (Test-Path -LiteralPath $InstallDir)) {
    Write-Info "Создаю каталог установки: $InstallDir"
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
}
if (-not (Test-Path -LiteralPath $certsDir)) {
    Write-Info "Создаю каталог сертификатов: $certsDir"
    New-Item -ItemType Directory -Path $certsDir -Force | Out-Null
}
Write-Ok 'Каталоги готовы.'

# --- Копирование бинарника -------------------------------------------------
# Бинарник статический (pure-Go, CGO не требуется), без рантайм-зависимостей.

$srcExe = Join-Path $SourceDir $exeName
if (Test-Path -LiteralPath $srcExe) {
    # Источник и приёмник могут совпасть, если скрипт запущен прямо из C:\Backuper.
    if ((Resolve-Path -LiteralPath $srcExe).Path -ne $destExe) {
        Write-Info "Копирую бинарник: $srcExe -> $destExe"
        Copy-Item -LiteralPath $srcExe -Destination $destExe -Force
    } else {
        Write-Info 'Бинарник уже находится в каталоге установки — копирование не требуется.'
    }
} elseif (Test-Path -LiteralPath $destExe) {
    Write-Warn2 "Бинарник в источнике не найден ($srcExe), но уже есть в каталоге установки — использую существующий."
} else {
    Fail "Не найден бинарник $exeName ни в источнике ($srcExe), ни в каталоге установки ($destExe).
Соберите его командой:  GOOS=windows GOARCH=amd64 go build -o bin/$exeName ./cmd/$Role"
}
Write-Ok 'Бинарник на месте.'

# --- Копирование .env ------------------------------------------------------
# Боевой .env должен лежать рядом с бинарником. Если в источнике его нет —
# копируем пример как заготовку и предупреждаем администратора.

$srcEnv        = Join-Path $SourceDir '.env'
$srcEnvExample = Join-Path $SourceDir ".env.$Role.example"

if (Test-Path -LiteralPath $srcEnv) {
    if ((Resolve-Path -LiteralPath $srcEnv).Path -ne $destEnv) {
        Write-Info "Копирую .env: $srcEnv -> $destEnv"
        Copy-Item -LiteralPath $srcEnv -Destination $destEnv -Force
    }
} elseif (Test-Path -LiteralPath $destEnv) {
    Write-Info '.env уже присутствует в каталоге установки — оставляю как есть.'
} elseif (Test-Path -LiteralPath $srcEnvExample) {
    Write-Warn2 "Боевой .env не найден. Копирую пример .env.$Role.example как заготовку — ОТРЕДАКТИРУЙТЕ его перед запуском службы."
    Copy-Item -LiteralPath $srcEnvExample -Destination $destEnv -Force
} else {
    Write-Warn2 "Файл .env не найден ни в источнике, ни в каталоге установки. Создайте $destEnv вручную (см. .env.$Role.example)."
}

# --- Копирование сертификатов ---------------------------------------------
# Раскладка по ролям TLS:
#   server: server.crt, server.key, ca.crt
#   client: client.crt, client.key, ca.crt
# Приватный ключ кладётся только своей стороне; ca.crt — обеим.

if ($Role -eq 'server') {
    $certFiles = @('server.crt', 'server.key', 'ca.crt')
} else {
    $certFiles = @('client.crt', 'client.key', 'ca.crt')
}

# Сертификаты могут лежать в SourceDir\certs или прямо в SourceDir.
$srcCertsDir = Join-Path $SourceDir 'certs'
foreach ($cf in $certFiles) {
    $candidate = $null
    if (Test-Path -LiteralPath (Join-Path $srcCertsDir $cf)) {
        $candidate = Join-Path $srcCertsDir $cf
    } elseif (Test-Path -LiteralPath (Join-Path $SourceDir $cf)) {
        $candidate = Join-Path $SourceDir $cf
    }

    $destCert = Join-Path $certsDir $cf
    if ($candidate) {
        if ((Resolve-Path -LiteralPath $candidate).Path -ne $destCert) {
            Write-Info "Копирую сертификат: $cf"
            Copy-Item -LiteralPath $candidate -Destination $destCert -Force
        }
    } elseif (Test-Path -LiteralPath $destCert) {
        Write-Info "Сертификат $cf уже в каталоге установки."
    } else {
        Write-Warn2 "Сертификат $cf не найден. Сгенерируйте набор: backuper-server gen-certs -out certs -client-host <CLIENT_HOST> -server-host <SERVER_HOST>"
    }
}

# --- Защита приватных ключей и .env ---------------------------------------
# Снимаем наследование ACL и оставляем доступ только SYSTEM и администраторам,
# чтобы приватные ключи и пароли в .env не читались обычными пользователями.

function Restrict-Acl {
    param([string]$Path)
    if (-not (Test-Path -LiteralPath $Path)) { return }
    try {
        # /inheritance:r — убрать наследование; затем явные права SYSTEM и Администраторы.
        & icacls.exe $Path /inheritance:r /grant:r 'SYSTEM:(F)' 'Administrators:(F)' | Out-Null
    } catch {
        Write-Warn2 "Не удалось ужесточить ACL для $Path: $($_.Exception.Message)"
    }
}

Restrict-Acl -Path $destEnv
foreach ($cf in $certFiles) {
    Restrict-Acl -Path (Join-Path $certsDir $cf)
}
Write-Ok 'Файлы разложены, права ужесточены.'

# --- Проверка конфигурации (check-config) ---------------------------------
# Раздел 24.5, п.2: backuper-*.exe check-config перед регистрацией службы.

if ($SkipCheckConfig) {
    Write-Warn2 'check-config пропущен по флагу -SkipCheckConfig.'
} else {
    Write-Info "Выполняю проверку конфигурации: $exeName check-config -config $configArg"
    # Запускаем синхронно и забираем код возврата.
    & $destExe check-config -config $configArg
    $rc = $LASTEXITCODE
    if ($rc -ne 0) {
        Fail "check-config завершился с кодом $rc. Исправьте $destEnv и сертификаты, затем повторите установку."
    }
    Write-Ok 'check-config пройдена успешно.'
}

# --- Удаление старой версии службы ----------------------------------------
# Если служба уже есть — останавливаем и удаляем, чтобы пересоздать чисто.

$existing = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if ($existing) {
    Write-Warn2 "Служба '$ServiceName' уже существует — останавливаю и удаляю для переустановки."
    if ($existing.Status -ne 'Stopped') {
        & sc.exe stop $ServiceName | Out-Null
        # Ждём фактической остановки, прежде чем удалять.
        for ($i = 0; $i -lt 15; $i++) {
            Start-Sleep -Milliseconds 1000
            $svc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
            if (-not $svc -or $svc.Status -eq 'Stopped') { break }
        }
    }
    & sc.exe delete $ServiceName | Out-Null
    # Менеджеру служб нужно немного времени, чтобы освободить имя.
    Start-Sleep -Milliseconds 1500
}

# --- Регистрация службы (sc.exe create) -----------------------------------
# Раздел 24.5, п.3: регистрация службы с автозапуском.
# binPath включает подкоманду run и явный -config на .env рядом с бинарником.
# Кавычки внутри binPath обязательны из-за пробелов в путях.

$binPath = "`"$destExe`" run -config `"$configArg`""

Write-Info "Регистрирую службу: $ServiceName"
Write-Info "binPath= $binPath"

# Примечание про синтаксис sc.exe: после 'имя_параметра=' ОБЯЗАТЕЛЕН пробел
# (например, 'start= auto'), иначе sc.exe не распознает аргументы.
& sc.exe create $ServiceName binPath= $binPath start= auto DisplayName= $displayName | Out-Null
if ($LASTEXITCODE -ne 0) {
    Fail "sc.exe create вернул код $LASTEXITCODE — служба не создана."
}

# Описание службы (отдельная команда sc.exe description).
& sc.exe description $ServiceName $description | Out-Null

Write-Ok "Служба '$ServiceName' создана с автозапуском (start= auto)."

# --- Recovery actions (sc.exe failure) ------------------------------------
# Раздел 19.2/24.5: автоперезапуск при сбое.
# reset= 86400  — окно сброса счётчика сбоев = 1 сутки (в секундах).
# actions= restart/5000 — перезапуск службы через 5000 мс после каждого сбоя
#   (три слота: 1-й, 2-й и последующие сбои — все restart).

Write-Info 'Настраиваю recovery actions (автоперезапуск при сбое).'
& sc.exe failure $ServiceName reset= 86400 actions= restart/5000/restart/5000/restart/5000 | Out-Null
if ($LASTEXITCODE -ne 0) {
    Write-Warn2 "sc.exe failure вернул код $LASTEXITCODE — recovery actions могли не примениться."
} else {
    Write-Ok 'Recovery actions заданы: reset= 86400, restart через 5000 мс.'
}

# Перезапускать службу также после некорректного (не нулевого) завершения,
# а не только после жёсткого падения процесса.
& sc.exe failureflag $ServiceName 1 | Out-Null

# --- Итог ------------------------------------------------------------------

Write-Host ''
Write-Ok "Установка завершена: служба '$ServiceName' зарегистрирована."
Write-Host ''
Write-Info 'Дальнейшие шаги:'
Write-Host "  1. Проверьте/заполните боевой $destEnv и сертификаты в $certsDir"
Write-Host "  2. Запустите службу:        sc.exe start $ServiceName   (или: Start-Service $ServiceName)"
Write-Host "  3. Проверьте статус узла:   & '$destExe' status -config `"$configArg`""
Write-Host "  4. Состояние службы:        Get-Service $ServiceName"
Write-Host ''
Write-Info "Удаление службы: sc.exe stop $ServiceName ; sc.exe delete $ServiceName"
