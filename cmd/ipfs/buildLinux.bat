@ECHO OFF
set GOOS=linux
go build -buildmode exe -o ipfs.linux.exe -ldflags="-s -w"