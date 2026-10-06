@echo off
setlocal

rem Edit these paths. DATA_DIR must match server.dataDir in CONFIG.
set "EXE=C:\minihub\minihub.exe"
set "CONFIG=C:\minihub\minihub.json"
set "DATA_DIR=C:\minihub\data"
set "BACKUP_DIR=\\backup-server\backups\minihub"

if not exist "%EXE%" (echo Executable not found: %EXE% 1>&2 & exit /b 1)
if not exist "%CONFIG%" (echo Config not found: %CONFIG% 1>&2 & exit /b 1)
if not exist "%DATA_DIR%\storage.json" (echo Storage marker not found: %DATA_DIR% 1>&2 & exit /b 1)
if not exist "%BACKUP_DIR%\." (
    mkdir "%BACKUP_DIR%"
    if errorlevel 1 (echo Cannot create backup directory: %BACKUP_DIR% 1>&2 & exit /b 1)
)

for %%D in ("%DATA_DIR%") do (
    set "DATA_PARENT=%%~dpD"
    set "DATA_NAME=%%~nxD"
)
set "NAME=minihub-%RANDOM%-%RANDOM%-%RANDOM%"
set "PARTIAL=%BACKUP_DIR%\%NAME%.partial.zip"
set "ARCHIVE=%BACKUP_DIR%\%NAME%.zip"
if exist "%ARCHIVE%" (echo Backup name already exists: %ARCHIVE% 1>&2 & exit /b 1)

call "%EXE%" stop -config "%CONFIG%" -wait 60s
if errorlevel 1 goto failed
tar.exe -a -c -f "%PARTIAL%" -C "%DATA_PARENT%." "%DATA_NAME%"
if errorlevel 1 goto failed
tar.exe -tf "%PARTIAL%" >nul
if errorlevel 1 goto failed
move /Y "%PARTIAL%" "%ARCHIVE%" >nul
if errorlevel 1 goto failed
echo Backup created: %ARCHIVE%
echo Start minihub manually after this backup.
exit /b 0

:failed
echo Backup failed. 1>&2
if exist "%PARTIAL%" del /f /q "%PARTIAL%"
echo Check whether minihub is running; start it manually if needed. 1>&2
exit /b 1
