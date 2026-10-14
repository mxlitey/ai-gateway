@echo off
chcp 65001 >nul
setlocal
cd /d "%~dp0"

rem 绿色版入口：桌面窗口（无黑窗口，可设端口、启停网关、增删插件）
if exist "ai-gateway-gui.exe" (
  start "" "ai-gateway-gui.exe"
  exit /b 0
)

echo [错误] 未找到 ai-gateway-gui.exe，请先运行 build\build.ps1 构建。
pause
exit /b 1