$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$env:CONFIG_FILE = Join-Path $root "config.local.yaml"
Set-Location (Join-Path $root "backend")
Write-Host "CONFIG_FILE=$env:CONFIG_FILE"
Write-Host "API http://localhost:8080  admin token: dev-admin"
go run ./cmd/server
