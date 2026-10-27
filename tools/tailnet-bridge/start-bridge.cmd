@echo off
cd /d "%~dp0"
if not exist "bridge.json" (
  copy "bridge.example.json" "bridge.json" >nul
  echo Created bridge.json. Edit target_address to the Linux server reachable from this Windows PC.
  echo Then run start-bridge.cmd again.
  pause
  exit /b 1
)
"%~dp0tailnet-bridge.exe" -config "%~dp0bridge.json"
pause
