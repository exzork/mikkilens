@echo off
REM Build MikkiLens from source.
REM
REM Needs Go 1.25 or newer and Node 20 or newer. Everything else comes down
REM with them.

setlocal
cd /d "%~dp0"

echo Fetching dependencies...
call go mod download || goto :failed
call npm install || goto :failed

echo Building the voice engine...
call go build -o dist\mikkilensd.exe .\apps\daemon || goto :failed

echo Building the settings app...
call npm run build:desktop || goto :failed

echo.
echo Done.
echo   run.bat       starts MikkiLens
echo   settings.bat  opens the settings window
echo   setup.bat     walks through the spoken first-run setup
echo   build-app.bat builds the one-click MikkiLens.exe in dist\app
exit /b 0

:failed
echo.
echo Build failed. See the messages above.
exit /b 1
