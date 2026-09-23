#Requires -Version 5.1
<#
One-click launcher for stock-x (Windows).
Installs web deps, builds the frontend, then serves API + UI on :8080.

Usage:
  .\start.cmd            # or double-click start.cmd -> build frontend + serve on :8080
  .\start.cmd dev        # dev mode: Vite :5173 + API :8080
  powershell -ExecutionPolicy Bypass -File .\start.ps1 dev

Note: messages are ASCII-only on purpose, so Windows PowerShell 5.1
(which reads .ps1 files using the ANSI code page) never garbles them.
#>
[CmdletBinding()]
param(
    [Parameter(Position = 0)]
    [string]$Mode = ''
)

# Native commands (npm/go) write warnings to stderr; on PowerShell 7.3+ that
# would become a terminating error when ErrorActionPreference is Stop.
if (Test-Path variable:PSNativeCommandUseErrorActionPreference) {
    $PSNativeCommandUseErrorActionPreference = $false
}
$ErrorActionPreference = 'Stop'

$Root = $PSScriptRoot
Set-Location -LiteralPath $Root

function Fail([string]$Message) {
    Write-Host $Message -ForegroundColor Red
    exit 1
}

function Need([string]$Name) {
    if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
        Fail "Missing command: $Name"
    }
}

function Get-Stamp([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path)) { return $null }
    return (Get-Item -LiteralPath $Path).LastWriteTime
}

# True when $A exists and is strictly newer than $B (missing $B counts as older).
function Test-Newer([string]$A, [string]$B) {
    $ta = Get-Stamp $A
    if ($null -eq $ta) { return $false }
    $tb = Get-Stamp $B
    if ($null -eq $tb) { return $true }
    return $ta -gt $tb
}

# Prefer the .cmd shim: it bypasses PowerShell script execution policy for npm.
$Npm = 'npm'
if (Get-Command 'npm.cmd' -ErrorAction SilentlyContinue) { $Npm = 'npm.cmd' }

Need 'go'
Need $Npm

$GoVersion = (& go env GOVERSION).Trim() -replace '^go', ''
$GoMajor = 0
$GoMinor = 0
if ($GoVersion -match '^(\d+)\.(\d+)') {
    $GoMajor = [int]$Matches[1]
    $GoMinor = [int]$Matches[2]
}
if ($GoMajor -lt 1 -or ($GoMajor -eq 1 -and $GoMinor -lt 26)) {
    Fail "Go >= 1.26 required, found: $GoVersion"
}

if (-not (Test-Path -LiteralPath '.env')) {
    Copy-Item -LiteralPath '.env.example' -Destination '.env'
    Write-Host 'Created .env (edit FEISHU_WEBHOOK_URL if needed)'
}

# 服务端口：优先环境变量，其次 .env 里的 HTTP_ADDR，最后 :8080
function Get-ServePort {
    $addr = $env:HTTP_ADDR
    if (-not $addr -and (Test-Path -LiteralPath '.env')) {
        $hit = Select-String -Path '.env' -Pattern '^\s*HTTP_ADDR\s*=' | Select-Object -Last 1
        if ($hit) { $addr = ($hit.Line -split '=', 2)[1].Trim() }
    }
    if (-not $addr) { $addr = ':8080' }
    return ($addr -split ':')[-1]
}

# 端口上残留着上一次 go run 的 stock-x 时，新进程会 bind 失败直接退出，
# 表现成「脚本说启动了，页面却没换」。这里先把它清掉再启动。
function Clear-StaleServer {
    $port = Get-ServePort
    $conn = Get-NetTCPConnection -State Listen -LocalPort ([int]$port) -ErrorAction SilentlyContinue |
        Select-Object -First 1
    if (-not $conn) { return }
    $proc = Get-Process -Id $conn.OwningProcess -ErrorAction SilentlyContinue
    $name = if ($proc) { $proc.ProcessName } else { 'unknown' }
    if ($name -ne 'stock-x') {
        Fail "Port $port is already in use by PID $($conn.OwningProcess) ($name). Stop it first, then re-run."
    }
    Write-Host "Port $port is held by a leftover stock-x instance (PID $($conn.OwningProcess)), stopping it..."
    taskkill /PID $conn.OwningProcess /T /F 2>$null | Out-Null
    for ($i = 0; $i -lt 40; $i++) {
        Start-Sleep -Milliseconds 250
        if (-not (Get-NetTCPConnection -State Listen -LocalPort ([int]$port) -ErrorAction SilentlyContinue)) {
            return
        }
    }
    Fail "Port $port is still in use after stopping the old instance."
}

Clear-StaleServer

function Test-WebNeedInstall {
    if (-not (Test-Path -LiteralPath 'web\node_modules')) { return $true }
    if (Test-Newer 'web\package.json' 'web\node_modules') { return $true }
    if (Test-Newer 'web\package-lock.json' 'web\node_modules') { return $true }
    return $false
}

function Invoke-WebInstall {
    if (-not (Test-WebNeedInstall)) {
        Write-Host 'Web dependencies are up to date, skipping npm install'
        return
    }
    Write-Host 'Installing web dependencies...'
    Push-Location 'web'
    try {
        & $Npm install --no-audit --no-fund --prefer-offline
        $code = $LASTEXITCODE
    } finally {
        Pop-Location
    }
    if ($code -ne 0) { Fail 'npm install failed' }
}

# Rebuild only when sources/config are newer than web/dist/index.html.
function Test-WebNeedBuild {
    $stamp = 'web\dist\index.html'
    if (-not (Test-Path -LiteralPath $stamp)) { return $true }
    foreach ($file in @('web\package.json', 'web\package-lock.json', 'web\index.html', 'web\vite.config.ts')) {
        if (Test-Newer $file $stamp) { return $true }
    }
    $stampTime = (Get-Item -LiteralPath $stamp).LastWriteTime
    foreach ($dir in @('web\src', 'web\public')) {
        if (-not (Test-Path -LiteralPath $dir)) { continue }
        $newer = Get-ChildItem -LiteralPath $dir -Recurse -File -ErrorAction SilentlyContinue |
            Where-Object { $_.LastWriteTime -gt $stampTime } |
            Select-Object -First 1
        if ($newer) { return $true }
    }
    return $false
}

# Dev mode: START_DEV=1 or `start.cmd dev` runs Vite and the API together.
if ($env:START_DEV -eq '1' -or $Mode -eq 'dev') {
    Write-Host 'Dev mode: Vite :5173 + API :8080'
    Invoke-WebInstall

    $vite = Start-Process -FilePath $Npm -ArgumentList 'run', 'dev' -PassThru -NoNewWindow `
        -WorkingDirectory (Join-Path $Root 'web')
    try {
        $embedDir = 'internal\webembed\dist'
        if (-not (Test-Path -LiteralPath $embedDir)) {
            New-Item -ItemType Directory -Path $embedDir -Force | Out-Null
        }
        $embedIndex = Join-Path $embedDir 'index.html'
        if (-not (Test-Path -LiteralPath $embedIndex)) {
            $placeholder = '<!doctype html><meta charset="utf-8"><title>stock-x</title>' +
                '<p>Use the Vite dev server on :5173</p>'
            Set-Content -LiteralPath $embedIndex -Value $placeholder -Encoding UTF8
        }
        & go run ./cmd/stock-x serve
        $code = $LASTEXITCODE
    } finally {
        if ($vite -and -not $vite.HasExited) {
            # taskkill /T also takes down the node process npm spawned.
            taskkill /PID $vite.Id /T /F 2>$null | Out-Null
        }
    }
    exit $code
}

Invoke-WebInstall

if (Test-WebNeedBuild) {
    Write-Host 'Building frontend...'
    Push-Location 'web'
    try {
        & $Npm run build
        $code = $LASTEXITCODE
    } finally {
        Pop-Location
    }
    if ($code -ne 0) { Fail 'Frontend build failed' }
} else {
    Write-Host 'Frontend build is up to date, skipping build'
}

Write-Host 'Syncing static assets into embed directory...'
$destDir = 'internal\webembed\dist'
# Wipe and recreate: Remove-Item does not accept piped DirectoryInfo objects.
Remove-Item -LiteralPath $destDir -Recurse -Force -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Path $destDir -Force | Out-Null
Copy-Item -Path 'web\dist\*' -Destination $destDir -Recurse -Force

$addr = if ($env:HTTP_ADDR) { $env:HTTP_ADDR } else { ':8080' }
# serve incrementally stores futures bars in SQLite and keeps them in memory for sweeps.
# History is then served locally. Disable with FUTURES_SYNC=0. Interval: FUTURES_SYNC_EVERY=30m
Write-Host "Serving http://127.0.0.1$addr ..."
& go run ./cmd/stock-x serve
exit $LASTEXITCODE
