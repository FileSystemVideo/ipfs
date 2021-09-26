package through

import (
	inet "github.com/libp2p/go-libp2p-core/network"
	ma "github.com/multiformats/go-multiaddr"
)


func (self *Through) Listen(net inet.Network, a ma.Multiaddr)      {}
func (self *Through) ListenClose(net inet.Network, a ma.Multiaddr) {}
func (self *Through) OpenedStream(net inet.Network, s inet.Stream) {}
func (self *Through) ClosedStream(net inet.Network, s inet.Stream) {}
func (self *Through) Connected(s inet.Network, c inet.Conn) {}
func (self *Through) Disconnected(s inet.Network, c inet.Conn) {
	p := c.RemotePeer()


	if self.host.Network().Connectedness(p) == inet.Connected {
		// 还有其他连接可以用
		return
	}

	//没有链接可以用的时候删除该客户端的信息
	self.deleteClientInfo(p)

	//删除地址信息
	self.host.Peerstore().ClearAddrs(p)
}