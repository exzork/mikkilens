@echo off
REM Open the settings window. It starts the engine if it is not already up.
setlocal
cd /d "%~dp0"
call npm run start
