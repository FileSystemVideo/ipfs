package iface

import (
	"context"
	"errors"
	"time"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/protocol"

	ma "github.com/multiformats/go-multiaddr"
	ipath "github.com/ipfs/interface-go-ipfs-core/path"
)

var (
	ErrNotConnected = errors.New("not connected")
	ErrConnNotFound = errors.New("conn not found")
)

// ConnectionInfo contains information about a peer
type ConnectionInfo interface {
	// ID returns PeerID
	ID() peer.ID

	// Address returns the multiaddress via which we are connected with the peer
	Address() ma.Multiaddr

	// Direction returns which way the connection was established
	Direction() network.Direction

	// Latency returns last known round trip time to the peer
	Latency() (time.Duration, error)

	// Streams returns list of streams established with the peer
	Streams() ([]protocol.ID, error)
}

//分享带宽和硬盘相关api
type ContributionApi interface{

	//获取指定ipfs文件的贡献者信息
	NameList(context.Context,ipath.Path) (map[string]int, error)

	//设置分享带宽的收益钱包地址
	SetWallet(string) error

	//读取分享带宽的收益钱包地址
	GetWallet() (string,error)
}

// SwarmAPI specifies the interface to libp2p swarm
type SwarmAPI interface {
	// Connect to a given peer
	Connect(context.Context, peer.AddrInfo) error

	// Disconnect from a given address
	Disconnect(context.Context, ma.Multiaddr) error

	// Peers returns the list of peers we are connected to
	Peers(context.Context) ([]ConnectionInfo, error)

	// KnownAddrs returns the list of all addresses this node is aware of
	KnownAddrs(context.Context) (map[peer.ID][]ma.Multiaddr, error)

	// LocalAddrs returns the list of announced listening addresses
	LocalAddrs(context.Context) ([]ma.Multiaddr, error)

	// ListenAddrs returns the list of all listening addresses
	ListenAddrs(context.Context) ([]ma.Multiaddr, error)

	Nat(context.Context) (network.Reachability, error)
}
