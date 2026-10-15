$ErrorActionPreference = 'Stop'
Push-Location $PSScriptRoot
try {
    $env:GOOS='windows'
    $env:GOARCH='amd64'
    $env:CGO_ENABLED='0'
    $env:GOTOOLCHAIN='go1.25.7'
    $env:GOMODCACHE=Join-Path (Split-Path $PSScriptRoot -Parent) '.gomodcache'
    $env:GOCACHE=Join-Path (Split-Path $PSScriptRoot -Parent) '.gocache'
    New-Item -ItemType Directory -Force 'dist' | Out-Null
    & go test ./internal/lan
    if ($LASTEXITCODE -ne 0) { throw 'Tests failed' }
    & go build -trimpath -ldflags='-s -w' -o dist/lan_client.exe ./cmd/client
    if ($LASTEXITCODE -ne 0) { throw 'Client build failed' }
    & go build -trimpath -ldflags='-s -w' -o dist/lan_controller.exe ./cmd/controller
    if ($LASTEXITCODE -ne 0) { throw 'Controller build failed' }
    if (Test-Path 'runtime/ffmpeg.exe') { Copy-Item 'runtime/ffmpeg.exe' 'dist/ffmpeg.exe' -Force }
    if (Test-Path 'runtime/ffmpeg-compat.exe') { Copy-Item 'runtime/ffmpeg-compat.exe' 'dist/ffmpeg-compat.exe' -Force }
    Copy-Item README.md,THIRD_PARTY.md,VERIFICATION.md,launch_client.cmd,launch_controller.cmd,configure_client_firewall.cmd,configure_controller_firewall.cmd dist/ -Force
    Write-Output 'Built dist/lan_client.exe and dist/lan_controller.exe (Windows x64)'
} finally { Pop-Location }
