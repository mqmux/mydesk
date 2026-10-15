param([int]$Parallel = 16)
$ErrorActionPreference = 'Stop'
$runtimeDirectory = Join-Path $PSScriptRoot 'runtime'
$outputDirectory = Join-Path $PSScriptRoot 'dist'
New-Item -ItemType Directory -Force $runtimeDirectory,$outputDirectory | Out-Null
$archivePath = Join-Path $runtimeDirectory 'ffmpeg-9.0.2-essentials_build.7z'
$url = 'https://www.gyan.dev/ffmpeg/builds/packages/ffmpeg-9.0.2-essentials_build.7z'
$totalBytes = 35430500
$chunkBytes = [int][Math]::Ceiling($totalBytes / $Parallel)
$workers = @()
for ($index=0; $index -lt $Parallel; $index++) {
    $start = $index * $chunkBytes
    $end = [Math]::Min($totalBytes-1, $start+$chunkBytes-1)
    $partPath = Join-Path $runtimeDirectory ('download-{0:D2}.part' -f $index)
	if ((Test-Path -LiteralPath $partPath) -and (Get-Item -LiteralPath $partPath).Length -eq ($end-$start+1)) {
		$workers += [pscustomobject]@{ Process=$null; Path=$partPath; Size=($end-$start+1) }
		continue
	}
    $arguments = @('-sS','-L','--fail','--retry','2','--max-time','240','--range',"$start-$end",'-o',"`"$partPath`"",$url)
    $process = Start-Process -FilePath 'curl.exe' -ArgumentList $arguments -WindowStyle Hidden -PassThru
    $workers += [pscustomobject]@{ Process=$process; Path=$partPath; Size=($end-$start+1) }
}
foreach ($worker in $workers) {
    if ($null -ne $worker.Process) { $worker.Process.WaitForExit() }
    if (($null -ne $worker.Process -and $worker.Process.ExitCode -ne 0) -or (Get-Item -LiteralPath $worker.Path).Length -ne $worker.Size) {
        throw "FFmpeg download failed: $($worker.Path)"
    }
}
$stream = [IO.File]::Create($archivePath)
try { foreach ($worker in $workers) { $part = [IO.File]::OpenRead($worker.Path); try { $part.CopyTo($stream) } finally { $part.Dispose() } } } finally { $stream.Dispose() }
$expected = '4705843CCAAF54257C16AD90F3E952ECE33C17DF964ECF7BFDBB0F49C7171077'
$actual = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash
if ($actual -ne $expected) { throw "FFmpeg SHA256 mismatch: $actual != $expected" }
& tar.exe -xf $archivePath -C $runtimeDirectory
if ($LASTEXITCODE -ne 0) { throw 'Could not unpack FFmpeg (Windows tar.exe with 7z support is required)' }
$package = Join-Path $runtimeDirectory 'ffmpeg-9.0.2-essentials_build'
Copy-Item -LiteralPath (Join-Path $package 'bin\ffmpeg.exe') -Destination (Join-Path $runtimeDirectory 'ffmpeg.exe') -Force
Copy-Item -LiteralPath (Join-Path $package 'bin\ffmpeg.exe') -Destination (Join-Path $outputDirectory 'ffmpeg.exe') -Force
Copy-Item -LiteralPath (Join-Path $package 'LICENSE') -Destination (Join-Path $outputDirectory 'FFmpeg-LICENSE.txt') -Force
Write-Output "FFmpeg installed and SHA256 verified: $actual"
