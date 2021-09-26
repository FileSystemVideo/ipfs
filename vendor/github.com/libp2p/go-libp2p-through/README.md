go-libp2p-through
==================

> libp2p穿透功能 允许对等方通过第三方协商连接其他对等方。


## Table of Contents

- [Install](#install)
- [Contribute](#contribute)
- [License](#license)

## Install

```sh
go get -u github.com/libp2p/go-libp2p-circuit
```
### 工作原理
穿透传输器through作为一个专用协议存在，和tcp、ws、quic属于同一级别


## 当前的问题

- 处于外网的主机,有时会识别NAT失败，把自己识别为内网
- 1111


依赖的模块
go-libp2p/p2p/host/through

