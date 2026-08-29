@echo off
REM Check every part and read the result aloud.
setlocal
cd /d "%~dp0"
if not exist dist\mikkilensd.exe (
    echo The engine is not built yet. Run install.bat first.
    exit /b 1
)
dist\mikkilensd.exe selftest
