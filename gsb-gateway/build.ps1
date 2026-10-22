# Builds the Go control plane and the Rust data plane and assembles a
# portable folder in ..\dist (GSB_Gateway.exe, gsb-core.exe, wintun.dll,
# ui.html, list.txt, BeniOku.txt). Run from any directory:
#   powershell -ExecutionPolicy Bypass -File gsb-gateway\build.ps1
# 'Continue' because cargo/go log progress on stderr, which Windows
# PowerShell 5.1 would otherwise turn into terminating errors.
$ErrorActionPreference = 'Continue'
$root = Split-Path -Parent $PSScriptRoot
$dist = Join-Path $root 'dist'

foreach ($f in 'ui.html', 'mini.html') {
  Copy-Item (Join-Path $root $f) (Join-Path $PSScriptRoot "control\web\$f") -Force
}

Push-Location (Join-Path $PSScriptRoot 'core')
cargo build --release
if ($LASTEXITCODE -ne 0) { throw 'cargo build failed' }
Pop-Location

# Official WireGuard-signed wintun.dll shipped inside the wintun-bindings crate.
$wintun = Get-ChildItem "$env:USERPROFILE\.cargo\registry\src\*\wintun-bindings-*\wintun\bin\amd64\wintun.dll" |
  Sort-Object FullName -Descending | Select-Object -First 1
if (-not $wintun) { throw 'wintun.dll not found in cargo registry' }
if ((Get-AuthenticodeSignature $wintun.FullName).Status -ne 'Valid') { throw 'wintun.dll signature invalid' }

# Stage files to embed into the single GSB_Gateway.exe: the Rust core, the
# signed wintun.dll and the proxy list. The icon resource (.syso) is picked up
# automatically by `go build`.
$emb = Join-Path $PSScriptRoot 'control\embedded'
New-Item -ItemType Directory -Force $emb | Out-Null
Copy-Item (Join-Path $PSScriptRoot 'core\target\release\gsb-core.exe') (Join-Path $emb 'gsb-core.exe') -Force
Copy-Item $wintun.FullName (Join-Path $emb 'wintun.dll') -Force
Copy-Item (Join-Path $root 'list.txt') (Join-Path $emb 'list.txt') -Force

Push-Location (Join-Path $PSScriptRoot 'control')
# App/window icon resource.
go run github.com/akavel/rsrc@v0.10.2 -ico tray.ico -o rsrc_windows.syso
if ($LASTEXITCODE -ne 0) { throw 'rsrc (icon) failed' }
go test ./...
if ($LASTEXITCODE -ne 0) { throw 'go test failed' }
# -H windowsgui: no console window — runs as a tray application.
go build -trimpath -ldflags '-s -w -H windowsgui' -o bin\GSB_Gateway.exe .
if ($LASTEXITCODE -ne 0) { throw 'go build failed' }
Pop-Location

# gsb-relay: the UDP relay server, cross-built for a Linux VPS.
Push-Location (Join-Path $PSScriptRoot 'relay')
go test ./...
if ($LASTEXITCODE -ne 0) { throw 'relay go test failed' }
$env:GOOS = 'linux'; $env:GOARCH = 'amd64'; $env:CGO_ENABLED = '0'
go build -trimpath -ldflags '-s -w' -o bin\gsb-relay-linux-amd64 .
$code = $LASTEXITCODE
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED
if ($code -ne 0) { throw 'relay build failed' }
Pop-Location

New-Item -ItemType Directory -Force $dist | Out-Null
Copy-Item (Join-Path $PSScriptRoot 'control\bin\GSB_Gateway.exe') $dist -Force
Copy-Item (Join-Path $PSScriptRoot 'core\target\release\gsb-core.exe') $dist -Force
Copy-Item $wintun.FullName $dist -Force
New-Item -ItemType Directory -Force (Join-Path $dist 'server') | Out-Null
Copy-Item (Join-Path $PSScriptRoot 'relay\bin\gsb-relay-linux-amd64') (Join-Path $dist 'server\gsb-relay') -Force
Copy-Item (Join-Path $PSScriptRoot 'relay\gsb-relay.service') (Join-Path $dist 'server') -Force
foreach ($f in 'ui.html', 'mini.html', 'list.txt', 'BeniOku.txt') {
  Copy-Item (Join-Path $root $f) $dist -Force
}
Write-Host "dist hazir: $dist"

# Portable zip: a single self-contained GSB_Gateway.exe (gsb-core, wintun.dll
# and the proxy list are embedded) plus the readme and the Linux relay. Runtime
# state (config.json, usage.json, logs) lives in %APPDATA%\GSB Gateway.
$stage = Join-Path $env:TEMP 'gsb-gateway-zip\GSB_Gateway'
if (Test-Path (Split-Path $stage)) { Remove-Item (Split-Path $stage) -Recurse -Force }
New-Item -ItemType Directory -Force (Join-Path $stage 'server') | Out-Null
foreach ($f in 'GSB_Gateway.exe', 'BeniOku.txt') {
  Copy-Item (Join-Path $dist $f) $stage -Force
}
Copy-Item (Join-Path $dist 'server\gsb-relay'), (Join-Path $dist 'server\gsb-relay.service') (Join-Path $stage 'server') -Force
$zip = Join-Path $root 'GSB_Gateway.zip'
if (Test-Path $zip) { Remove-Item $zip -Force }
Compress-Archive -Path $stage -DestinationPath $zip -CompressionLevel Optimal
Remove-Item (Split-Path $stage) -Recurse -Force
Write-Host "zip hazir: $zip"
