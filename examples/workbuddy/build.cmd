@echo off
chcp 65001 >nul
setlocal
cd /d "%~dp0..\.."
if "%GOPROXY%"=="" set GOPROXY=https://goproxy.cn,direct
set CGO_ENABLED=0
echo building workbuddy plugin...
go build -trimpath -ldflags "-s -w" -o "%~dp0workbuddy.exe" ./examples/workbuddy
if errorlevel 1 (
  echo build failed
  exit /b 1
)
echo done: %~dp0workbuddy.exe
endlocal