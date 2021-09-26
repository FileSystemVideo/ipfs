/*
The relay package contains the components necessary to implement the "autorelay"
feature.
中继程序包包含实现“自动继电器”功能所需的组件。

Warning: the internal interfaces are unstable.
警告：内部接口不稳定。

System Components:
- A discovery service to discover public relays.
- 发现服务以发现公共中继。
- An AutoNAT client used to determine if the node is behind a NAT/firewall.
- 用于确定节点是否在NAT /防火墙后面的AutoNAT客户端。
- One or more autonat services, instances of `AutoNATServices`. These are used
  by the autonat client.
- 一个或多个autonat服务，即AutoNATServices的实例。这些由autonat客户端使用。
- One or more relays, instances of `RelayHost`.
- 一个或多个中继，即RelayHost的实例。
- The AutoRelay service. This is the service that actually:
- 自动中继服务。该服务实际上是：

AutoNATService: https://github.com/libp2p/go-libp2p-autonat-svc
AutoNAT: https://github.com/libp2p/go-libp2p-autonat

How it works:
- `AutoNATService` instances are instantiated in the bootstrappers (or other
  well known publicly reachable hosts)
- 在引导程序（或其他众所周知的公共可访问主机）中实例化“ AutoNATService”实例。
- `AutoRelay`s are constructed with `libp2p.New(libp2p.Routing(makeDHT))`
  They passively discover autonat service instances and test dialability of
  their listen address set through them.  When the presence of NAT is detected,
  they discover relays through the DHT, connect to some of them and begin
  advertising relay addresses.  The new set of addresses is propagated to
  connected peers through the `identify/push` protocol.
- `AutoRelay`s是由`libp2p.New（libp2p.Routing（makeDHT））`构建的
  他们被动地发现自动服务实例，并通过它们测试其侦听地址集的可拨号性。
  当检测到NAT的存在时，他们通过DHT发现中继，连接到其中的一些中继并开始发布中继地址。
  新的地址集通过“ identify / push”协议传播到连接的对等端。
*/
package through
