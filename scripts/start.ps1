$ErrorActionPreference = 'Stop'
Set-Location (Split-Path -Parent $PSScriptRoot)
$running = docker compose ps --status running --services app
if ($LASTEXITCODE -ne 0) { throw 'Could not check project service status.' }
if ($running -contains 'app') {
    $health = Invoke-RestMethod 'http://127.0.0.1:38120/healthz' -TimeoutSec 5
    if ($health.status -ne 'ok') { throw 'Project service is running but unhealthy.' }
    Write-Output 'Already running: http://127.0.0.1:38120 (web and API)'
    return
}
$listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 38120)
try { $listener.Start() } catch { throw '127.0.0.1:38120 is unavailable; no service was stopped.' } finally { $listener.Stop() }
docker compose --profile tools run --rm --no-deps tools go build -trimpath -o bin/netdisk ./cmd/netdisk
if ($LASTEXITCODE -ne 0) { throw 'Build failed.' }
docker compose up -d app
if ($LASTEXITCODE -ne 0) { throw 'Startup failed.' }
Write-Output 'Local URL: http://127.0.0.1:38120 (web and API)'
