<#
.SYNOPSIS
    Backuper — интерактивный установщик для Windows 10+.
    Скачивает готовый .exe c GitHub Release, задаёт вопросы по каждому параметру
    .env (с пояснением, значением по умолчанию и вариантами), регистрирует службу
    Windows и (если всё готово) запускает её.

.DESCRIPTION
    Запуск (PowerShell от администратора):
      iwr https://raw.githubusercontent.com/Ivan2812446/backuper/main/install.ps1 -OutFile "$env:TEMP\binst.ps1"
      powershell -ExecutionPolicy Bypass -File "$env:TEMP\binst.ps1" -Role server

    Любой параметр можно передать аргументом (тогда его не спросят). Полностью без
    вопросов: добавьте -NonInteractive (и нужные аргументы).

.PARAMETER Role          server | client
.PARAMETER NonInteractive не задавать вопросы (значения из аргументов/по умолчанию)
.PARAMETER OwnPassword   пароль этой стороны
.PARAMETER PeerPassword  пароль другой стороны
.PARAMETER ClientHost    (server) адрес клиента в LAN
.PARAMETER ServerHost    (server) адрес сервера (SAN сертификата)
.PARAMETER BackupDir     (client) каталог-источник
.PARAMETER Version       тег релиза (по умолчанию latest)
.PARAMETER NoStart       не запускать службу автоматически
#>
[CmdletBinding()]
param(
    [ValidateSet('server', 'client')][string]$Role,
    [switch]$Uninstall,
    [switch]$Purge,
    [switch]$Update,
    [switch]$NonInteractive,
    [string]$OwnPassword,
    [string]$PeerPassword,
    [string]$ClientHost,
    [string]$ServerHost,
    [string]$BackupDir,
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

$script:Interactive = (-not $NonInteractive) -and [Environment]::UserInteractive

function Info($m) { Write-Host "[i] $m" -ForegroundColor Cyan }
function Ok($m) { Write-Host "[+] $m" -ForegroundColor Green }
function Warn2($m) { Write-Host "[!] $m" -ForegroundColor Yellow }
function Fail($m) { Write-Host "[x] $m" -ForegroundColor Red; exit 1 }

function GenPassword { -join ((48..57) + (65..90) + (97..122) | Get-Random -Count 32 | ForEach-Object { [char]$_ }) }

function Ask([string]$desc, [string]$def) {
    if (-not $script:Interactive) { return $def }
    Write-Host ""; Write-Host $desc -ForegroundColor White
    $a = Read-Host "  [по умолчанию: $def] (Enter — принять)"
    if ([string]::IsNullOrEmpty($a)) { return $def } else { return $a }
}
function AskOpt([string]$desc) {
    if (-not $script:Interactive) { return "" }
    Write-Host ""; Write-Host $desc -ForegroundColor White
    return (Read-Host "  (Enter — пропустить)")
}
function AskRequired([string]$desc) {
    if (-not $script:Interactive) { return "" }
    while ($true) {
        Write-Host ""; Write-Host $desc -ForegroundColor White
        $a = Read-Host "  значение (обязательно)"
        if ($a) { return $a }
        Warn2 "это поле обязательно"
    }
}
function AskPassword([string]$desc) {
    if (-not $script:Interactive) { return (GenPassword) }
    Write-Host ""; Write-Host $desc -ForegroundColor White
    $sec = Read-Host "  введите пароль (Enter — сгенерировать случайный)" -AsSecureString
    $p = [Runtime.InteropServices.Marshal]::PtrToStringAuto([Runtime.InteropServices.Marshal]::SecureStringToBSTR($sec))
    if ([string]::IsNullOrEmpty($p)) { $p = GenPassword; Write-Host "  сгенерирован: $p" -ForegroundColor Yellow }
    return $p
}
function AskChoice([string]$desc, [string]$def, [string[]]$opts) {
    if (-not $script:Interactive) { return $def }
    Write-Host ""; Write-Host $desc -ForegroundColor White
    for ($i = 0; $i -lt $opts.Count; $i++) { Write-Host ("  {0}. {1}" -f ($i + 1), $opts[$i]) }
    Write-Host ("  {0}. другое значение" -f ($opts.Count + 1))
    $a = Read-Host "  выберите номер или впишите своё [по умолчанию: $def] (Enter — принять)"
    if ([string]::IsNullOrEmpty($a)) { return $def }
    $n = 0
    if ([int]::TryParse($a, [ref]$n)) {
        if ($n -ge 1 -and $n -le $opts.Count) { return $opts[$n - 1] }
        if ($n -eq $opts.Count + 1) { $c = Read-Host "  введите своё значение"; if ($c) { return $c } else { return $def } }
    }
    return $a
}
function AskMulti([string]$desc, [string]$def, [string[]]$opts) {
    if (-not $script:Interactive) { return $def }
    Write-Host ""; Write-Host $desc -ForegroundColor White
    for ($i = 0; $i -lt $opts.Count; $i++) { Write-Host ("  {0}. {1}" -f ($i + 1), $opts[$i]) }
    $a = Read-Host "  один или несколько через запятую (напр. 1,3), 0 — ничего, или своё [по умолчанию: $def]"
    if ([string]::IsNullOrEmpty($a)) { return $def }
    if ($a -eq "0") { return "" }
    if ($a -match '^[0-9,\s]+$') {
        $out = @()
        foreach ($t in $a.Split(',')) {
            $t = $t.Trim(); if (-not $t) { continue }
            $n = 0; if ([int]::TryParse($t, [ref]$n) -and $n -ge 1 -and $n -le $opts.Count) { $out += $opts[$n - 1] }
        }
        return ($out -join ',')
    }
    return $a
}
function YesNo([string]$q, [string]$def = 'y') {
    if (-not $script:Interactive) { return ($def -eq 'y') }
    $hint = if ($def -eq 'y') { 'Y/n' } else { 'y/N' }
    $a = Read-Host "$q [$hint]"
    if ([string]::IsNullOrEmpty($a)) { $a = $def }
    return ($a -match '^[YyДд]')
}

# --- права администратора ---
$id = [Security.Principal.WindowsIdentity]::GetCurrent()
if (-not (New-Object Security.Principal.WindowsPrincipal($id)).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Fail 'Запустите PowerShell от имени Администратора.'
}

# Удаление: ... -Uninstall [-Purge]
if ($Uninstall) {
    foreach ($r in 'server', 'client') {
        if (Get-Service "backuper-$r" -ErrorAction SilentlyContinue) {
            & sc.exe stop "backuper-$r" | Out-Null
            & sc.exe delete "backuper-$r" | Out-Null
            Ok "удалена служба backuper-$r"
        }
    }
    if ($Purge) {
        Remove-Item -Recurse -Force $installDir -ErrorAction SilentlyContinue
        Ok "удалено полностью, включая данные: $installDir"
    }
    else {
        Ok "службы удалены; файлы в $installDir СОХРАНЕНЫ."
        Info "Удалить вместе с данными: добавьте -Purge"
    }
    exit 0
}
if ($Update) {
    foreach ($r in 'server', 'client') {
        $rexe = Join-Path $installDir "backuper-$r.exe"
        if (-not (Test-Path $rexe)) { continue }
        $u = "https://github.com/$repo/releases/latest/download/backuper-$r-windows-amd64.exe"
        Info "Обновление backuper-$r из релиза …"
        $svcR = "backuper-$r"; $wasRunning = $false
        $s = Get-Service $svcR -ErrorAction SilentlyContinue
        if ($s -and $s.Status -eq 'Running') { $wasRunning = $true; & sc.exe stop $svcR | Out-Null; Start-Sleep 2 }
        [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
        Invoke-WebRequest -Uri $u -OutFile $rexe -UseBasicParsing
        Ok "обновлён $rexe"
        if ($wasRunning) { & sc.exe start $svcR | Out-Null; Ok "служба $svcR перезапущена" }
    }
    Ok ".env и сертификаты сохранены без изменений."
    exit 0
}
if (-not $Role) { Fail "укажите -Role server|client (или -Uninstall / -Update)" }

Info "Установка Backuper ($Role, windows/amd64), версия $Version"
if ($script:Interactive) { Info "Интерактивный режим: отвечайте на вопросы (Enter — значение по умолчанию)." }

New-Item -ItemType Directory -Force -Path $installDir, $certsDir | Out-Null

# --- скачивание .exe ---
$asset = "backuper-$Role-windows-amd64.exe"
if ($Version -eq 'latest') { $url = "https://github.com/$repo/releases/latest/download/$asset" }
else { $url = "https://github.com/$repo/releases/download/$Version/$asset" }
Info "Скачивание $asset …"
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
Invoke-WebRequest -Uri $url -OutFile $exe -UseBasicParsing
Ok "бинарник: $exe"

# --- опрос параметров и запись .env ---
$writeEnv = $true
if (Test-Path $envFile) {
    if (-not (YesNo "Файл $envFile уже существует. Перезаписать?" 'n')) { $writeEnv = $false; Warn2 "оставляю существующий $envFile" }
}

$tlsMin = '1.2'; $logLevel = 'INFO'
if ($writeEnv) {
    Write-Host ""; Info "Настройка параметров ($Role)"
    if ($Role -eq 'server') {
        if (-not $ClientHost) { $ClientHost = AskRequired "CLIENT_HOST — адрес КЛИЕНТА в локальной сети (IP/hostname), к которому сервер подключается за файлами." }
        $clientPort = Ask "CLIENT_PORT — TCP-порт клиента." "9000"
        if (-not $OwnPassword) { $OwnPassword = AskPassword "OWN_PASSWORD — пароль ЭТОГО сервера (на клиенте — как PEER_PASSWORD)." }
        if (-not $PeerPassword) { $PeerPassword = AskPassword "PEER_PASSWORD — пароль КЛИЕНТА (то, что у клиента в OWN_PASSWORD)." }
        $storageDir = Ask "STORAGE_DIR — куда складывать копии файлов." "$installDir\storage"
        $trashDir = Ask "TRASH_DIR — корзина для удалённых на клиенте файлов." "$installDir\trash"
        $tempDir = Ask "TEMP_DIR — временные файлы докачки." "$installDir\temp"
        $syncInterval = AskChoice "SYNC_INTERVAL — как часто запускать сверку/бэкап." "1h" @("15m", "1h", "6h", "24h")
        $retention = Ask "TRASH_RETENTION_DAYS — сколько дней хранить файлы в корзине." "10"
        $parallel = AskChoice "PARALLEL_TRANSFERS — сколько файлов качать параллельно." "4" @("2", "4", "8")
        $bw = Ask "BANDWIDTH_LIMIT — лимит скорости, байт/с (0 — без лимита; можно 10MB)." "0"
        $tlsMin = AskChoice "TLS_MIN_VERSION — минимальная версия TLS." "1.2" @("1.2", "1.3")
        $logLevel = AskChoice "LOG_LEVEL — уровень логирования." "INFO" @("INFO", "DEBUG", "WARN", "ERROR")

        Write-Host ""; Info "Почта для алертов (SMTP)."
        if (YesNo "Настроить отправку e-mail алертов сейчас?" 'y') {
            $smtpHost = AskRequired "SMTP_HOST — адрес SMTP-сервера (напр. smtp.gmail.com)."
            $smtpPort = Ask "SMTP_PORT — порт SMTP." "587"
            $smtpSec = AskChoice "SMTP_SECURITY — шифрование соединения." "starttls" @("starttls", "tls", "none")
            $smtpFrom = AskRequired "SMTP_FROM — адрес отправителя."
            $smtpTo = AskRequired "SMTP_TO — получатели (через запятую)."
            $smtpUser = AskOpt "SMTP_USER — логин SMTP (если нужен)."
            $smtpPass = AskOpt "SMTP_PASSWORD — пароль SMTP."
        }
        else {
            $smtpHost = 'ЗАМЕНИТЕ-smtp.example.com'; $smtpPort = '587'; $smtpSec = 'starttls'
            $smtpFrom = 'ЗАМЕНИТЕ-backuper@example.com'; $smtpTo = 'ЗАМЕНИТЕ-admin@example.com'; $smtpUser = ''; $smtpPass = ''
            Warn2 "SMTP пропущен — заполните позже в $envFile."
        }

        New-Item -ItemType Directory -Force -Path $storageDir, $trashDir, $tempDir, "$installDir\logs" | Out-Null
        @"
CLIENT_HOST=$ClientHost
CLIENT_PORT=$clientPort
TLS_CERT_FILE=$certsDir\server.crt
TLS_KEY_FILE=$certsDir\server.key
TLS_CA_FILE=$certsDir\ca.crt
TLS_MIN_VERSION=$tlsMin
OWN_PASSWORD=$OwnPassword
PEER_PASSWORD=$PeerPassword
STORAGE_DIR=$storageDir
TRASH_DIR=$trashDir
TEMP_DIR=$tempDir
SYNC_INTERVAL=$syncInterval
PARALLEL_TRANSFERS=$parallel
BANDWIDTH_LIMIT=$bw
TRASH_RETENTION_DAYS=$retention
SMTP_HOST=$smtpHost
SMTP_PORT=$smtpPort
SMTP_SECURITY=$smtpSec
SMTP_FROM=$smtpFrom
SMTP_TO=$smtpTo
SMTP_USER=$smtpUser
SMTP_PASSWORD=$smtpPass
LOG_DIR=$installDir\logs
AUDIT_LOG=$installDir\logs\audit.jsonl
STATE_DB=$installDir\state.db
LOCK_FILE=$installDir\backuper.lock
LOG_LEVEL=$logLevel
TIMEZONE=Europe/Moscow
"@ | Set-Content -Path $envFile -Encoding UTF8
    }
    else {
        $listenHost = Ask "LISTEN_HOST — интерфейс прослушивания (0.0.0.0 — все)." "0.0.0.0"
        $listenPort = Ask "LISTEN_PORT — TCP-порт прослушивания." "9000"
        if (-not $OwnPassword) { $OwnPassword = AskPassword "OWN_PASSWORD — пароль ЭТОГО клиента (на сервере — как PEER_PASSWORD)." }
        if (-not $PeerPassword) { $PeerPassword = AskPassword "PEER_PASSWORD — пароль СЕРВЕРА (то, что у сервера в OWN_PASSWORD)." }
        if (-not $BackupDir) { $BackupDir = AskRequired "BACKUP_DIR — каталог-источник, который бэкапим (он же цель восстановления), напр. C:\Data." }
        $excl = AskMulti "EXCLUDE_PATTERNS — какие файлы НЕ бэкапить (маски)." "*.tmp,*.lock" @("*.tmp", "*.lock", "~`$*", "*.bak")
        $incl = AskOpt "INCLUDE_PATTERNS — бэкапить ТОЛЬКО эти маски (пусто — все файлы)."
        $bw = Ask "BANDWIDTH_LIMIT — лимит скорости, байт/с (0 — без лимита)." "0"
        $tlsMin = AskChoice "TLS_MIN_VERSION — минимальная версия TLS." "1.2" @("1.2", "1.3")
        $logLevel = AskChoice "LOG_LEVEL — уровень логирования." "INFO" @("INFO", "DEBUG", "WARN", "ERROR")

        New-Item -ItemType Directory -Force -Path "$installDir\logs" | Out-Null
        @"
LISTEN_HOST=$listenHost
LISTEN_PORT=$listenPort
TLS_CERT_FILE=$certsDir\client.crt
TLS_KEY_FILE=$certsDir\client.key
TLS_CA_FILE=$certsDir\ca.crt
TLS_MIN_VERSION=$tlsMin
OWN_PASSWORD=$OwnPassword
PEER_PASSWORD=$PeerPassword
BACKUP_DIR=$BackupDir
INCLUDE_PATTERNS=$incl
EXCLUDE_PATTERNS=$excl
BANDWIDTH_LIMIT=$bw
LOG_DIR=$installDir\logs
AUDIT_LOG=$installDir\logs\audit.jsonl
LOG_LEVEL=$logLevel
TIMEZONE=Europe/Moscow
"@ | Set-Content -Path $envFile -Encoding UTF8
    }
    Ok "конфигурация записана: $envFile"
}

# --- защита .env и ключей ACL ---
function Restrict-Acl([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path)) { return }
    try { & icacls.exe $Path /inheritance:r /grant:r 'SYSTEM:(F)' 'Administrators:(F)' | Out-Null } catch { }
}
Restrict-Acl $envFile

# --- сертификаты на сервере ---
$haveCerts = (Test-Path "$certsDir\ca.crt") -and (Test-Path "$certsDir\$Role.crt") -and (Test-Path "$certsDir\$Role.key")
if ($Role -eq 'server' -and -not (Test-Path "$certsDir\ca.crt")) {
    if (YesNo "Сгенерировать TLS-сертификаты сейчас?" 'y') {
        if (-not $ClientHost) { $ClientHost = AskRequired "Адрес клиента (SAN client.crt)." }
        if (-not $ServerHost) { $ServerHost = Ask "SERVER_HOST — адрес ЭТОГО сервера для SAN." "127.0.0.1" }
        $genArgs = @('gen-certs', '-out', $certsDir, '-client-host', $ClientHost, '-server-host', $ServerHost)
        & $exe @genArgs
        $haveCerts = (Test-Path "$certsDir\server.crt")
        Get-ChildItem "$certsDir\*.key" -ErrorAction SilentlyContinue | ForEach-Object { Restrict-Acl $_.FullName }
        Warn2 "СКОПИРУЙТЕ на клиента: $certsDir\ca.crt, client.crt, client.key"
    }
}

# --- служба ---
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

# --- проверка и запуск ---
Write-Host ""
& $exe check-config -config $envFile
$cfgOk = ($LASTEXITCODE -eq 0)
if ($cfgOk -and $haveCerts -and -not $NoStart) {
    & sc.exe start $svc | Out-Null
    Ok "служба '$svc' запущена."
    Info "Статус узла: & '$exe' status -config `"$envFile`""
}
else {
    Warn2 'Служба пока НЕ запущена — завершите настройку:'
    if ($Role -eq 'client' -and -not $haveCerts) { Warn2 "  - положите в $certsDir файлы ca.crt, client.crt, client.key (с сервера)" }
    Info "После настройки: & '$exe' check-config -config `"$envFile`"; sc.exe start $svc"
}
Write-Host ""
Ok "Готово (роль $Role)."
