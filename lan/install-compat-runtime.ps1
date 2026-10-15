$ErrorActionPreference = 'Stop'
$runtimeDirectory=Join-Path $PSScriptRoot 'runtime'
$outputDirectory=Join-Path $PSScriptRoot 'dist'
New-Item -ItemType Directory -Force $runtimeDirectory,$outputDirectory | Out-Null
$archive=Join-Path $runtimeDirectory 'ffmpeg-7.0.2.7z'
if (-not (Test-Path -LiteralPath $archive)) {
    & curl.exe -sS -L --fail --retry 2 -o $archive 'https://github.com/GyanD/codexffmpeg/releases/download/7.0.2/ffmpeg-7.0.2-essentials_build.7z'
    if ($LASTEXITCODE -ne 0) { throw 'Compatible FFmpeg download failed' }
}
$expected='B9D55FC90D03FA921281C6A0FE398723667FA6A126D8544B29F20EA819D54AD9'
if ((Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash -ne $expected) { throw 'Compatible FFmpeg archive hash differs from the build verified for this project' }
& tar.exe -xf $archive -C $runtimeDirectory
if ($LASTEXITCODE -ne 0) { throw 'Could not unpack compatible runtime' }
Copy-Item -LiteralPath (Join-Path $runtimeDirectory 'ffmpeg-7.0.2-essentials_build\bin\ffmpeg.exe') -Destination (Join-Path $runtimeDirectory 'ffmpeg-compat.exe') -Force
Copy-Item -LiteralPath (Join-Path $runtimeDirectory 'ffmpeg-compat.exe') -Destination (Join-Path $outputDirectory 'ffmpeg-compat.exe') -Force
Write-Output 'Installed FFmpeg 7.0.2 driver-compatible encoder runtime'
