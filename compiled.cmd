@echo off
if not exist ".gomodcache" mkdir ".gomodcache"
if not exist ".gocache" mkdir ".gocache"
if not exist ".gotmp" mkdir ".gotmp"
set "GOMODCACHE=%CD%\.gomodcache"
set "GOCACHE=%CD%\.gocache"
set "GOTMPDIR=%CD%\.gotmp"
set dates=%date:~0,10%
set times=%time:~0,-3%
set "BUILD_TIME=%dates:/=%%times::=%"
echo;BUILD_TIME:[%BUILD_TIME%]

if "%~1"=="" goto :linux

set GOOS=windows
set CGO_ENABLED=0
if /i "%~nx1"=="controller.go" (
    go build -trimpath -ldflags="-s -w -H windowsgui" %1
) else (
    go build -trimpath -ldflags="-s -w -X main.buildTime=%BUILD_TIME%" %1
)
goto :EOFF

:linux
echo;linux server.go
pause
set GOOS=linux
set CGO_ENABLED=0
go build -trimpath -ldflags="-s -w -X main.buildTime=%BUILD_TIME%" server.go || exit /b 1

:EOFF
pause