@echo off
chcp 65001 >nul
setlocal
cd /d "%~dp0..\.."
if "%GOPROXY%"=="" set GOPROXY=https://goproxy.cn,direct
set CGO_ENABLED=0
if not exist "%~dp0..\..\dist\plugins\trae" mkdir "%~dp0..\..\dist\plugins\trae"
echo building trae plugin...
go build -trimpath -ldflags "-s -w" -o "%~dp0..\..\dist\plugins\trae\trae.exe" ./examples/trae
if errorlevel 1 (
  echo build failed
  exit /b 1
)
copy /y "%~dp0plugin.json" "%~dp0..\..\dist\plugins\trae\plugin.json" >nul
echo done: %~dp0..\..\dist\plugins\trae\trae.exe
endlocal