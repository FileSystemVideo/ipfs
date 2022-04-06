@ECHO OFF
set GOOS=linux
go build -o ipfs.linux -ldflags="-s -w"