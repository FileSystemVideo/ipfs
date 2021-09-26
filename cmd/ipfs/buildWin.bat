@ECHO OFF
set GOOS=windows
go build -buildmode exe -o ipfs.win.exe -ldflags="-s -w"