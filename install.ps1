<#
.SYNOPSIS
    Backuper — установщик «всё в одном» для Windows 10+.
    Сам скачивает готовый .exe с GitHub Release и регистрирует службу Windows.

.DESCRIPTION
    Скачивает backuper-<role>-windows-amd64.exe из релиза GitHub в C:\Backuper,
    создаёт .env (если нет) и каталог сертификатов, регистрирует службу с
    автозапуском и автоперезапуском, выполняет check-config и (если всё готово)
    запускает службу.

.PARAMETER Role         server | client
.PARAMETER OwnPassword  пароль этой стороны (server.OWN == client.PEER, ≥24 симв.)
.PARAMETER PeerPassword пароль другой стороны
.PARAMETER ClientHost   (server) адрес клиента в LAN
.PARAMETER ServerHost   (server) адрес сервера (SAN сертификата)
.PARAMETER BackupDir    (client) каталог-источник (по умолчанию C:\Data)
.PARAMETER Version      тег релиза (по умолчанию latest)
.PARAMETER NoStart      не запускать службу автоматически

.EXAMPLE
    # из PowerShell от администратора:
    .\install.ps1 -Role server -ClientHost 192.168.1.50 -OwnPassword "...." -PeerPassword "...."
    .\install.ps1 -Role client -BackupDir C:\Data -OwnPassword "...." -PeerPassword "...."

.EXAMPLE
    # одной строкой (скачать и запустить):
    iwr https://raw.githubusercontent.com/Ivan2812446/backuper/main/install.ps1 -OutFile "$env:TEMP\binst.ps1"; & "$env:TEMP\binst.ps1" -Role server
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][ValidateSet('server', 'client')][string]$Role,
    [string]$OwnPassword,
    [string]$PeerPassword,
    [string]$ClientHost,
    [string]$ServerHost,
    [string]$BackupDir = 'C:\Data',
    [string]$Version = 'latest',
    [switch]$NoStart
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$repo = 'Ivan2812446/backuper'
$installDir = 'C:\Backuper'
$certsDir = Join-Path $installDir 'certs'
$exe = Join-Path $installDir "backuper-$Role.exe"
$envFile = Join-Path $installDir '.env'
$svc = "backuper-$Role"

function Info($m) { Write-Host "[i] $m" -ForegroundColor Cyan }
function Ok($m) { Write-Host "[+] $m" -ForegroundColor Green }
function Warn2($m) { Write-Host "[!] $m" -ForegroundColor Yellow }
function Fail($m) { Write-Host "[x] $m" -ForegroundColor Red; exit 1 }

# админ?
$id = [Security.Principal.WindowsIdentity]::GetCurrent()
if (-not (New-Object Security.Principal.WindowsPrincipal($id)).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Fail 'Запустите PowerShell от имени Администратора.'
}

Info "Установка Backuper ($Role, windows/amd64), версия $Version"

New-Item -ItemType Directory -Force -Path $installDir, $certsDir | Out-Null

# 1. скачивание .exe
$asset = "backuper-$Role-windows-amd64.exe"
if ($Version -eq 'latest') {
    $url = "https://github.com/$repo/releases/latest/download/$asset"
}
else {
    $url = "https://github.com/$repo/releases/download/$Version/$asset"
}
Info "Скачивание $asset …"
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
Invoke-WebRequest -Uri $url -OutFile $exe -UseBasicParsing
Ok "бинарник: $exe"

# 2. .env (не перезаписываем)
$havePw = ($OwnPassword -and $PeerPassword)
if (-not $OwnPassword) { $OwnPassword = 'ЗАМЕНИТЕ-собственный-пароль-не-короче-24' }
if (-not $PeerPassword) { $PeerPassword = 'ЗАМЕНИТЕ-пароль-другой-стороны-не-короче-24' }

if (Test-Path $envFile) {
    Warn2 ".env уже существует — оставлен без изменений: $envFile"
}
else {
    if ($Role -eq 'server') {
        $ch = if ($ClientHost) { $ClientHost } else { 'ЗАМЕНИТЕ-адрес-клиента' }
        @"
CLIENT_HOST=$ch
CLIENT_PORT=9000
TLS_CERT_FILE=$certsDir\server.crt
TLS_KEY_FILE=$certsDir\server.key
TLS_CA_FILE=$certsDir\ca.crt
OWN_PASSWORD=$OwnPassword
PEER_PASSWORD=$PeerPassword
STORAGE_DIR=$installDir\storage
TRASH_DIR=$installDir\trash
TEMP_DIR=$installDir\temp
SYNC_INTERVAL=1h
PARALLEL_TRANSFERS=4
TRASH_RETENTION_DAYS=10
SMTP_HOST=smtp.example.com
SMTP_PORT=587
SMTP_FROM=backuper@example.com
SMTP_TO=admin@example.com
SMTP_SECURITY=starttls
LOG_DIR=$installDir\logs
AUDIT_LOG=$installDir\logs\audit.jsonl
STATE_DB=$installDir\state.db
LOCK_FILE=$installDir\backuper.lock
LOG_LEVEL=INFO
TIMEZONE=Europe/Moscow
"@ | Set-Content -Path $envFile -Encoding UTF8
        New-Item -ItemType Directory -Force -Path "$installDir\storage", "$installDir\trash", "$installDir\temp", "$installDir\logs" | Out-Null
    }
    else {
        @"
LISTEN_HOST=0.0.0.0
LISTEN_PORT=9000
TLS_CERT_FILE=$certsDir\client.crt
TLS_KEY_FILE=$certsDir\client.key
TLS_CA_FILE=$certsDir\ca.crt
OWN_PASSWORD=$OwnPassword
PEER_PASSWORD=$PeerPassword
BACKUP_DIR=$BackupDir
EXCLUDE_PATTERNS=*.tmp,*.lock,~`$*
LOG_DIR=$installDir\logs
AUDIT_LOG=$installDir\logs\audit.jsonl
LOG_LEVEL=INFO
TIMEZONE=Europe/Moscow
"@ | Set-Content -Path $envFile -Encoding UTF8
        New-Item -ItemType Directory -Force -Path "$installDir\logs" | Out-Null
    }
    Ok "создан $envFile"
}

# 3. сертификаты на сервере (если задан ClientHost)
$haveCerts = (Test-Path "$certsDir\ca.crt") -and (Test-Path "$certsDir\$Role.crt") -and (Test-Path "$certsDir\$Role.key")
if ($Role -eq 'server' -and $ClientHost -and -not (Test-Path "$certsDir\ca.crt")) {
    Info "Генерация сертификатов (client-host=$ClientHost) …"
    $genArgs = @('gen-certs', '-out', $certsDir, '-client-host', $ClientHost)
    if ($ServerHost) { $genArgs += @('-server-host', $ServerHost) }
    & $exe @genArgs
    $haveCerts = (Test-Path "$certsDir\server.crt")
    Warn2 "СКОПИРУЙТЕ на клиента: $certsDir\ca.crt, client.crt, client.key"
}

# защитим .env и ключи ACL (только SYSTEM и администраторы)
foreach ($p in @($envFile) + (Get-ChildItem "$certsDir\*.key" -ErrorAction SilentlyContinue | ForEach-Object FullName)) {
    if (Test-Path $p) { & icacls.exe $p /inheritance:r /grant:r 'SYSTEM:(F)' 'Administrators:(F)' | Out-Null }
}

# 4. служба
$existing = Get-Service -Name $svc -ErrorAction SilentlyContinue
if ($existing) {
    if ($existing.Status -ne 'Stopped') { & sc.exe stop $svc | Out-Null; Start-Sleep 2 }
    & sc.exe delete $svc | Out-Null; Start-Sleep 1
}
$binPath = "`"$exe`" run -config `"$envFile`""
& sc.exe create $svc binPath= $binPath start= auto DisplayName= "Backuper $Role" | Out-Null
if ($LASTEXITCODE -ne 0) { Fail "sc.exe create вернул $LASTEXITCODE" }
& sc.exe failure $svc reset= 86400 actions= restart/5000/restart/5000/restart/5000 | Out-Null
& sc.exe failureflag $svc 1 | Out-Null
Ok "служба '$svc' зарегистрирована (автозапуск + автоперезапуск)"

# 5. проверка и запуск
& $exe check-config -config $envFile
$cfgOk = ($LASTEXITCODE -eq 0)
Write-Host ''
if ($cfgOk -and $havePw -and $haveCerts -and -not $NoStart) {
    & sc.exe start $svc | Out-Null
    Ok "служба '$svc' запущена."
    Info "Статус узла: & '$exe' status -config `"$envFile`""
}
else {
    Warn2 'Служба НЕ запущена автоматически — завершите настройку:'
    if (-not $havePw) { Warn2 "  - задайте пароли OWN_PASSWORD/PEER_PASSWORD в $envFile" }
    if ($Role -eq 'server') {
        if (-not $ClientHost) { Warn2 "  - укажите CLIENT_HOST в $envFile" }
        if (-not $haveCerts) { Warn2 "  - создайте сертификаты: & '$exe' gen-certs -out $certsDir -client-host <IP_клиента> -server-host <IP_сервера>" }
    }
    else {
        if (-not $haveCerts) { Warn2 "  - положите в $certsDir файлы ca.crt, client.crt, client.key (с сервера)" }
        Warn2 "  - проверьте BACKUP_DIR в $envFile"
    }
    Info "После настройки: & '$exe' check-config -config `"$envFile`"; sc.exe start $svc"
}
Write-Host ''
Ok "Готово (роль $Role)."
