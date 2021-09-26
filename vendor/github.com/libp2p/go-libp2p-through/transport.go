package through

import (
	"context"
	"fmt"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/transport"
	tptu "github.com/libp2p/go-libp2p-transport-upgrader"
	ma "github.com/multiformats/go-multiaddr"
	"time"
)


var throughAddr = ma.Cast(ma.ProtocolWithCode(ma.P_THROUGH).VCode)

var _ transport.Transport = (*ThroughTransport)(nil)

//穿透传输器类型
type ThroughTransport Through

func (t *ThroughTransport) Ping(addr ma.Multiaddr, timeout time.Duration) error {
	log.Debug("ThroughTransport->ping()")
	return nil
}

//返回穿透对象
func (t *ThroughTransport) Through() *Through {
	return (*Through)(t)
}

func (r *Through) Transport() *ThroughTransport {
	return (*ThroughTransport)(r)
}

func (t *ThroughTransport) Listen(laddr ma.Multiaddr) (transport.Listener, error) {
	// TODO: Ensure we have a connection to the relay, if specified. Also,
	// 确保multiaddr定义了穿透,才会启用穿透监听。
	if !t.Through().Matches(laddr) {
		return nil, fmt.Errorf("%s is not a through address", laddr)
	}
	return t.upgrader.UpgradeListener(t, t.Through().Listener()), nil
}

//返回是否可以对目标地址进行拨号
func (t *ThroughTransport) CanDial(raddr ma.Multiaddr) bool {
	return t.Through().Matches(raddr)
}

func (t *ThroughTransport) Proxy() bool {
	return true
}

func (t *ThroughTransport) Protocols() []int {
	return []int{ma.P_THROUGH}
}

// 构造穿透对象并将其作为传输添加到主机网络。
func AddThroughTransport(ctx context.Context,h host.Host, upgrader *tptu.Upgrader) error {
	log.Debug("AddThroughTransport()-------------")
	n, ok := h.Network().(transport.TransportNetwork)
	if !ok {
		return fmt.Errorf("%v is not a transport network", h.Network())
	}

	r, err := NewThrough(ctx, h, upgrader)
	if err != nil {
		return err
	}

	// 尝试添加传输器到host中
	if err := n.AddTransport(r.Transport()); err != nil {
		log.Error("failed to add through transport:", err)

		//如果传输器添加失败，尝试监听本地端口
	} else if err := n.Listen(r.Listener().Multiaddr()); err != nil {
		log.Error("failed to listen on through transport:", err)
	}
	return nil
}
