
require protoc-gen-gogo.exe


protoc -I=$GOPATH\src\. --proto_path=. --gogo_out=plugins=grpc:.  message.proto