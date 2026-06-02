@echo off
echo Building TrayClash...

set GO_BIN=go
for /d %%d in ("%USERPROFILE%\sdk\go1.20*") do (
    if exist "%%d\bin\go.exe" (
        set GO_BIN="%%d\bin\go.exe"
    )
)

if not %GO_BIN%==go (
    echo Found Go 1.20 SDK, using %GO_BIN% for Windows 7 compatibility...
) else (
    echo WARNING: Go 1.20 SDK not found in %%USERPROFILE%%\sdk. Using system default Go...
)

echo Generating resources...
%USERPROFILE%\go\bin\go-winres make

echo Cleaning up old builds...
if exist dist rmdir /s /q dist
mkdir dist\x64
mkdir dist\x86

echo Building x64 version...
set GOARCH=amd64
set CGO_ENABLED=0
%GO_BIN% build -trimpath -ldflags="-s -w -H windowsgui" -o dist\x64\TrayClash.exe .

echo Building x86 version...
set GOARCH=386
set CGO_ENABLED=0
%GO_BIN% build -trimpath -ldflags="-s -w -H windowsgui" -o dist\x86\TrayClash.exe .


echo Done!
pause
