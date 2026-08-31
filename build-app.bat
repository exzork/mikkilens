@echo off
REM Build the one-click MikkiLens.exe.
REM
REM This is the build that produces something to hand to someone else: one
REM installer and one portable executable, each carrying the engine inside it.
REM Neither needs Go or Node on the machine it runs on.

setlocal
cd /d "%~dp0"

echo Building the voice engine...
call npm run build:daemon || goto :failed

echo Packaging the app...
call npm run package || goto :failed

echo.
echo Done. In dist\app:
echo   MikkiLens.exe              double-click to run, nothing to install
echo   MikkiLens-Setup-*.exe      installs it, with a desktop shortcut
exit /b 0

:failed
echo.
echo Build failed. See the messages above.
exit /b 1
