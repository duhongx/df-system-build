@echo off
cd /d "%~dp0"
if not exist "tunnel.json" (
  echo Copy tunnel.example.json to tunnel.json and configure the relay and target first.
  pause
  exit /b 1
)
"%~dp0ssh-tunnel.exe" -config "%~dp0tunnel.json"
pause
