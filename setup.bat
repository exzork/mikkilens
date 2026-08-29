@echo off
REM Spoken first-run setup: it reads every choice aloud as you make it.
setlocal
cd /d "%~dp0"
if not exist dist\mikkilensd.exe (
    echo The engine is not built yet. Run install.bat first.
    exit /b 1
)
dist\mikkilensd.exe setup
