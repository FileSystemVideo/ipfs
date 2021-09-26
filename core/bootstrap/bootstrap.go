package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"sync"
	"time"

	logging "github.com/ipfs/go-log"
	"github.com/jbenet/goprocess"
	"github.com/jbenet/goprocess/context"
	"github.com/jbenet/goprocess/periodic"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/peerstore"
	"github.com/libp2p/go-libp2p-core/routing"
)

var log = logging.Logger("bootstrap")

// ErrNotEnoughBootstrapPeers signals that we do not have enough bootstrap
// peers to bootstrap correctly.
var ErrNotEnoughBootstrapPeers = errors.New("not enough bootstrap peers to bootstrap")

// BootstrapConfig specifies parameters used in an IpfsNode's network
// bootstrapping process.
type BootstrapConfig struct {
	// MinPeerThreshold governs whether to bootstrap more connections. If the
	// node has less open connections than this number, it will open connections
	// to the bootstrap nodes. From there, the routing system should be able
	// to use the connections to the bootstrap nodes to connect to even more
	// peers. Routing systems like the IpfsDHT do so in their own Bootstrap
	// process, which issues random queries to find more peers.
	MinPeerThreshold int

	// Period governs the periodic interval at which the node will
	// attempt to bootstrap. The bootstrap process is not very expensive, so
	// this threshold can afford to be small (<=30s).
	Period time.Duration

	// ConnectionTimeout determines how long to wait for a bootstrap
	// connection attempt before cancelling it.
	ConnectionTimeout time.Duration

	// BootstrapPeers is a function that returns a set of bootstrap peers
	// for the bootstrap process to use. This makes it possible for clients
	// to control the peers the process uses at any moment.
	BootstrapPeers func() []peer.AddrInfo
}

// DefaultBootstrapConfig specifies default sane parameters for bootstrapping.
var DefaultBootstrapConfig = BootstrapConfig{
	MinPeerThreshold:  4,
	Period:            30 * time.Second,
	ConnectionTimeout: (30 * time.Second) / 3, // Perod / 3
}

func BootstrapConfigWithPeers(pis []peer.AddrInfo) BootstrapConfig {
	cfg := DefaultBootstrapConfig
	cfg.BootstrapPeers = func() []peer.AddrInfo {
		return pis
	}
	return cfg
}

// 启动IpfsNode引导。
// 此函数将定期检查打开的连接数，如果连接数太少，则会启动与已知引导对等机的连接。
// 它还启动子系统引导（即路由）。
func Bootstrap(id peer.ID, host host.Host, rt routing.Routing, cfg BootstrapConfig) (io.Closer, error) {

	// make a signal to wait for one bootstrap round to complete.
	doneWithRound := make(chan struct{})

	if len(cfg.BootstrapPeers()) == 0 {
		// We *need* to bootstrap but we have no bootstrap peers
		// configured *at all*, inform the user.
		log.Warn("no bootstrap nodes configured: go-ipfs may have difficulty connecting to the network")
	}

	// the periodic bootstrap function -- the connection supervisor
	periodic := func(worker goprocess.Process) {
		ctx := goprocessctx.OnClosingContext(worker)

		if err := bootstrapRound(ctx, host, cfg); err != nil {
			log.Debugf("%s bootstrap error: %s", id, err)
		}

		<-doneWithRound
	}

	// 启动节点的定期引导
	proc := periodicproc.Tick(cfg.Period, periodic)
	proc.Go(periodic) // run one right now.

	// 开始路由引导
	if rt != nil {
		ctx := goprocessctx.OnClosingContext(proc)
		if err := rt.Bootstrap(ctx); err != nil {
			proc.Close()
			return nil, err
		}
	}

	doneWithRound <- struct{}{}
	close(doneWithRound) // it no longer blocks periodic
	return proc, nil
}

func bootstrapRound(ctx context.Context, host host.Host, cfg BootstrapConfig) error {

	ctx, cancel := context.WithTimeout(ctx, cfg.ConnectionTimeout)
	defer cancel()
	id := host.ID()

	// 从配置中获取引导对等点。在这里检索它们可以确保我们能够观察到客户机配置的变化。
	peers := cfg.BootstrapPeers()
	// 确定要打开的引导连接数
	connected := host.Network().Peers()
	if len(connected) >= cfg.MinPeerThreshold {
		log.Debugf("%s core bootstrap skipped -- connected to %d (> %d) nodes",
			id, len(connected), cfg.MinPeerThreshold)
		return nil
	}
	numToDial := cfg.MinPeerThreshold - len(connected)

	// 筛选出我们已经连接到的引导节点
	var notConnected []peer.AddrInfo
	for _, p := range peers {
		if host.Network().Connectedness(p.ID) != network.Connected {
			notConnected = append(notConnected, p)
		}
	}

	// 如果已连接到所有的引导对等节点，返回
	if len(notConnected) < 1 {
		log.Debugf("%s no more bootstrap peers to create %d connections", id, numToDial)
		return ErrNotEnoughBootstrapPeers
	}

	// 连接到随机的引导候选集
	randSubset := randomSubsetOfPeers(notConnected, numToDial)

	log.Debugf("%s bootstrapping to %d nodes: %s", id, numToDial, randSubset)
	return bootstrapConnect(ctx, host, randSubset)
}

func bootstrapConnect(ctx context.Context, ph host.Host, peers []peer.AddrInfo) error {
	if len(peers) < 1 {
		return ErrNotEnoughBootstrapPeers
	}

	errs := make(chan error, len(peers))
	var wg sync.WaitGroup
	for _, p := range peers {

		// 异步执行，因为当同步执行时，如果一个“Connect”调用挂起，后续调用更有可能由于过期上下文而失败/中止。
		// 另外，异步执行拨号速度

		wg.Add(1)
		go func(p peer.AddrInfo) {
			defer wg.Done()
			log.Debugf("%s bootstrapping to %s", ph.ID(), p.ID)

			ph.Peerstore().AddAddrs(p.ID, p.Addrs, peerstore.PermanentAddrTTL)
			if err := ph.Connect(ctx, p); err != nil {
				log.Debugf("failed to bootstrap with %v: %s", p.ID, err)
				errs <- err
				return
			}
			log.Infof("bootstrapped with %v", p.ID)
		}(p)
	}
	wg.Wait()

	// our failure condition is when no connection attempt succeeded.
	// So drain the errs channel, counting the results.
	close(errs)
	count := 0
	var err error
	for err = range errs {
		if err != nil {
			count++
		}
	}
	if count == len(peers) {
		return fmt.Errorf("failed to bootstrap. %s", err)
	}
	return nil
}

func randomSubsetOfPeers(in []peer.AddrInfo, max int) []peer.AddrInfo {
	if max > len(in) {
		max = len(in)
	}

	out := make([]peer.AddrInfo, max)
	for i, val := range rand.Perm(len(in))[:max] {
		out[i] = in[val]
	}
	return out
}
