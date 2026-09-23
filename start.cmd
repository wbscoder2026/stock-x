@echo off
rem One-click launcher for stock-x on Windows.
rem Double-click this file, or run:  start.cmd [dev]
rem ASCII-only on purpose: cmd.exe uses the OEM code page and would garble Chinese.
setlocal
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0start.ps1" %*
if errorlevel 1 (
  echo.
  echo [start] failed with exit code %errorlevel%
  pause
)
endlocal
