$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location (Join-Path $root "frontend")
if (-not (Test-Path "node_modules")) {
  npm install
}
Write-Host "管控台 http://localhost:5173  Token: dev-admin"
npm run dev
