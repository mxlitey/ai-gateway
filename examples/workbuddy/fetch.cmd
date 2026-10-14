@echo off
setlocal
cd /d "%~dp0"

set ASSET=workbuddy-gateway-windows-amd64.exe
set URL=https://github.com/CangShui/workbuddy-gateway/releases/latest/download/%ASSET%

if exist "workbuddy-gateway.exe" goto :login

echo 正在下载 %ASSET% ...
echo   来源: %URL%
powershell -NoProfile -ExecutionPolicy Bypass -Command "try { Invoke-WebRequest -Uri '%URL%' -OutFile 'workbuddy-gateway.exe' -UseBasicParsing } catch { Write-Host ('下载失败: ' + $_.Exception.Message); exit 1 }"

if not exist "workbuddy-gateway.exe" (
  echo.
  echo 下载失败。请手动到 https://github.com/CangShui/workbuddy-gateway/releases
  echo 下载 %ASSET% ，重命名为 workbuddy-gateway.exe 后放到本目录。
  pause
  exit /b 1
)
echo 下载完成: workbuddy-gateway.exe

:login
echo.
echo ---------------------------------------------------------------
echo 第一步：登录（会打开浏览器完成授权，只需做一次）
echo.
echo    国内站 CodeBuddy  ^(codebuddy.cn^)
echo      "%~dp0workbuddy-gateway.exe" login -auth "%~dp0workbuddy.json"
echo.
echo    国际站 WorkBuddy  ^(workbuddy.ai^)，额外再加 -intl
echo      "%~dp0workbuddy-gateway.exe" login -intl -auth "%~dp0workbuddy-intl.json"
echo.
echo 第二步：把本目录整个复制到网关的 plugins\workbuddy
echo         例如  xcopy /E /I /Y "%~dp0." "D:\aigw\plugins\workbuddy\"
echo.
echo 第三步：打开管理面板的「插件」页，点「重新扫描」
echo         （凭据就在本目录里，插件会自动加载）
echo ---------------------------------------------------------------
pause