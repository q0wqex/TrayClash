@echo off
echo Building TrayClash...

echo Generating resources...
%USERPROFILE%\go\bin\go-winres make

echo Cleaning up old builds...
if exist dist rmdir /s /q dist
mkdir dist\x64
mkdir dist\x86

echo Building x64 version...
set GOARCH=amd64
set CGO_ENABLED=0
go build -trimpath -ldflags="-s -w -H windowsgui" -o dist\x64\TrayClash.exe .

echo Building x86 version...
set GOARCH=386
set CGO_ENABLED=0
go build -trimpath -ldflags="-s -w -H windowsgui" -o dist\x86\TrayClash.exe .


echo Done!
pause
