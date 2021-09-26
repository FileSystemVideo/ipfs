package through

import (
	"context"
	"fmt"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/transport"
	ma "github.com/multiformats/go-multiaddr"
)

//普通用户 要连接 指定peer时 进行调用拨号
func (d *ThroughTransport) Dial(ctx context.Context, a ma.Multiaddr, p peer.ID) (transport.CapableConn, error) {
	c, err := d.Through().Dial(ctx, a, p)
	if err != nil {
		return nil, err
	}
	return d.upgrader.UpgradeOutbound(ctx, d, c, p)
}

//对目标peer p 的穿透服务器进行连接
func (r *Through) Dial(ctx context.Context, a ma.Multiaddr, p peer.ID) (*Conn, error) {
	// split /a/p2p-circuit/b into (/a, /p2p-circuit/b)

	//解析地址返回 穿透服务器的地址 和 目标peer的地址
	serverAddr, destAddr := ma.SplitFunc(a, func(c ma.Component) bool {
		return c.Protocol().Code == ma.P_THROUGH
	})

	// 如果地址里面不包含穿透服务器的pid，则第二部分为nil。
	if destAddr == nil {
		//这时候需要使用普通的连接
		return nil, fmt.Errorf("%s is not a through address", a)
	}

	// 从目标地址中去掉 /p2p-circuit 前缀。
	_, destAddr = ma.SplitFirst(destAddr)

	dinfo := &peer.AddrInfo{ID: p, Addrs: []ma.Multiaddr{}}
	if destAddr != nil {
		dinfo.Addrs = append(dinfo.Addrs, destAddr)
	}

	if serverAddr == nil {
		// 如果对方未使用穿透服务器,返回错误
		return nil, fmt.Errorf("the service address of the target is invalid")
	}

	var rinfo *peer.AddrInfo
	rinfo, err := peer.AddrInfoFromP2pAddr(serverAddr) //获取穿透服务器的信息
	if err != nil {
		return nil, fmt.Errorf("error parsing multiaddr '%s': %s", serverAddr.String(), err)
	}

	return r.DialPeer(ctx, *rinfo, *dinfo) //调用穿透服务器 rinfo 来连接目标 dinfo
}