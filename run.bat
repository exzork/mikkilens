@echo off
REM Start the voice engine in this window. Ctrl+C stops it.
setlocal
cd /d "%~dp0"
if not exist dist\mikkilensd.exe (
    echo The engine is not built yet. Run install.bat first.
    exit /b 1
)
dist\mikkilensd.exe run
