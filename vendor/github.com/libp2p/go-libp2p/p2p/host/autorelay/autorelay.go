package autorelay

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	//"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/libp2p/go-libp2p-core/discovery"
	"github.com/libp2p/go-libp2p-core/event"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/routing"

	basic "github.com/libp2p/go-libp2p/p2p/host/basic"
	relayv1 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv1/relay"
	circuitv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	circuitv2_proto "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/proto"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

const (
	RelayRendezvous = "/libp2p/relay"

	rsvpRefreshInterval = time.Minute
	rsvpExpirationSlack = 2 * time.Minute

	autorelayTag = "autorelay"

	protoIDv1 = string(relayv1.ProtoID)
	protoIDv2 = string(circuitv2_proto.ProtoIDv2Hop)
)

var (
	DesiredRelays = 2

	BootDelay = 20 * time.Second
)

// DefaultRelays are the known PL-operated v1 relays; will be decommissioned in 2022.
var DefaultRelays = []string{
	//"/ip4/147.75.80.110/tcp/4001/p2p/QmbFgm5zan8P6eWWmeyfncR5feYEMPbht5b1FW1C37aQ7y",
	//"/ip4/147.75.80.110/udp/4001/quic/p2p/QmbFgm5zan8P6eWWmeyfncR5feYEMPbht5b1FW1C37aQ7y",
	//"/ip4/147.75.195.153/tcp/4001/p2p/QmW9m57aiBDHAkKj9nmFSEn7ZqrcF1fZS4bipsTCHburei",
	//"/ip4/147.75.195.153/udp/4001/quic/p2p/QmW9m57aiBDHAkKj9nmFSEn7ZqrcF1fZS4bipsTCHburei",
	//"/ip4/147.75.70.221/tcp/4001/p2p/Qme8g49gm3q4Acp7xWBKg3nAa9fxZ1YmyDJdyGgoG6LsXh",
	//"/ip4/147.75.70.221/udp/4001/quic/p2p/Qme8g49gm3q4Acp7xWBKg3nAa9fxZ1YmyDJdyGgoG6LsXh",
}

var defaultStaticRelays []peer.AddrInfo

func init() {
	for _, s := range DefaultRelays {
		pi, err := peer.AddrInfoFromString(s)
		if err != nil {
			panic(fmt.Sprintf("failed to initialize default static relays: %s", err))
		}
		defaultStaticRelays = append(defaultStaticRelays, *pi)
	}
}

type Option func(*AutoRelay) error
type StaticRelayOption Option

func WithStaticRelays(static []peer.AddrInfo) StaticRelayOption {
	return func(r *AutoRelay) error {
		if len(r.static) > 0 {
			return errors.New("can't set static relays, static relays already configured")
		}
		r.static = static
		return nil
	}
}

func WithDefaultStaticRelays() StaticRelayOption {
	return WithStaticRelays(defaultStaticRelays)
}

func EnabledRelayServer() Option {
	return  func(r *AutoRelay) error {
		r.enableServer = true
		return nil
	}
}

func WithDiscoverer(discover discovery.Discovery) Option {
	return func(r *AutoRelay) error {
		r.discover = discover
		return nil
	}
}

// AutoRelay is a Host that uses relays for connectivity when a NAT is detected.
type AutoRelay struct {
	host     *basic.BasicHost
	discover discovery.Discovery
	router   routing.PeerRouting
	addrsF   basic.AddrsFactory
	enableServer bool		//是否开启中继服务
	static []peer.AddrInfo	//静态中继列表

	refCount  sync.WaitGroup
	ctxCancel context.CancelFunc

	relayFound        chan struct{}
	findRelaysRunning int32 // to be used as an atomic

	mx     sync.Mutex
	relays map[peer.ID]*circuitv2.Reservation // rsvp will be nil if it is a v1 relay
	status network.Reachability

	cachedAddrs       []ma.Multiaddr
	cachedAddrsExpiry time.Time
	discoverRelaysCache []peer.AddrInfo  //通过dht发现的中继列表
	discoverRelaysExpireTime time.Time  //发现的中继列表的到期时间
}

func NewAutoRelay(bhost *basic.BasicHost, router routing.PeerRouting, opts ...Option) (*AutoRelay, error) {
	ctx, cancel := context.WithCancel(context.Background())
	ar := &AutoRelay{
		ctxCancel:  cancel,
		host:       bhost,
		router:     router,
		addrsF:     bhost.AddrsFactory,
		relays:     make(map[peer.ID]*circuitv2.Reservation),
		relayFound: make(chan struct{}, 1),
		status:     network.ReachabilityUnknown,
	}
	for _, opt := range opts {
		if err := opt(ar); err != nil {
			return nil, err
		}
	}
	bhost.AddrsFactory = ar.hostAddrs
	ar.refCount.Add(1)

	if ar.enableServer{
		go ar.serverAdvertise(ctx)
	}
	go ar.background(ctx)
	return ar, nil
}

func (ar *AutoRelay) hostAddrs(addrs []ma.Multiaddr) []ma.Multiaddr {
	return ar.relayAddrs(ar.addrsF(addrs))
}

//发送中继服务的广播
func (ar *AutoRelay) serverAdvertise(ctx context.Context) {
	log.Info("relay server advertise start")
	for {
		//如果当前不是公网,不需要发送中继服务的消息
		if ar.status != network.ReachabilityPublic {
			log.Debug("Current nat not is public, Registration of relay Server will not be conducted temporarily")
			<-time.After(time.Minute) //1分钟之后再检查
			continue
		}
		ttl, err := ar.discover.Advertise(ctx, RelayRendezvous, discovery.TTL(AdvertiseTTL))
		if err != nil {
			log.Debugf("Error advertising %s: %s", RelayRendezvous, err.Error())
			if ctx.Err() != nil {
				return
			}

			select {
			case <-time.After(2 * time.Minute):
				continue
			case <-ctx.Done():
				return
			}
		}
		log.Debug("relay server advertise send done")

		wait := 7 * ttl / 8
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return
		}
	}
}

func (ar *AutoRelay) background(ctx context.Context) {
	defer ar.refCount.Done()

	subReachability, err := ar.host.EventBus().Subscribe(new(event.EvtLocalReachabilityChanged))
	if err != nil {
		log.Error("failed to subscribe to the EvtLocalReachabilityChanged")
		return
	}
	defer subReachability.Close()
	subConnectedness, err := ar.host.EventBus().Subscribe(new(event.EvtPeerConnectednessChanged))
	if err != nil {
		log.Error("failed to subscribe to the EvtPeerConnectednessChanged")
		return
	}
	defer subConnectedness.Close()

	ticker := time.NewTicker(rsvpRefreshInterval)
	defer ticker.Stop()

	for {
		// 如果是真的，我们需要更新身份
		var push bool

		select {
		case ev, ok := <-subConnectedness.Out():
			if !ok {
				return
			}
			evt := ev.(event.EvtPeerConnectednessChanged)
			switch evt.Connectedness {
			case network.Connected: //已连接上Peer的事件
				//log.Debugf("peer %s Connected",evt.Peer)
				for _, pi := range ar.static {
					if pi.ID == evt.Peer { //如果链接上的这个peer是静态中继的话,直接尝试中继
						rsvp, ok := ar.tryRelay(ctx, pi)
						if ok {
							ar.mx.Lock()
							ar.relays[pi.ID] = rsvp
							ar.mx.Unlock()
						}
						push = true
						break
					}
				}
				if len(ar.relays) < DesiredRelays { //如果中继服务数量少于期望值,则启动自动连接
					for _, pi := range ar.discoverRelaysCache{
						if pi.ID == evt.Peer { //如果链接上的这个peer是发现的中继的话,直接继续尝试中继
							rsvp, ok := ar.tryRelay(ctx, pi)
							if ok {
								ar.mx.Lock()
								ar.relays[pi.ID] = rsvp
								ar.mx.Unlock()
							}
							push = true
							break
						}
					}
				}
			case network.NotConnected: //无法连接到peer的事件
				//log.Debugf("peer %s Not Connected",evt.Peer)
				ar.mx.Lock()
				if ar.usingRelay(evt.Peer) { // 我们和中继的链接已经断开
					log.Debug("relay NotConnected")
					delete(ar.relays, evt.Peer)
					push = true

					if len(ar.relays) < DesiredRelays {
						go func() {
							//如果没有足够的中继可以用,这里需要重新查询可用的中继,需要观察
							ar.findRelaysOnce(ctx)
						}()
					}
				}
				ar.mx.Unlock()
			}
		case ev, ok := <-subReachability.Out():
			if !ok {
				return
			}
			evt := ev.(event.EvtLocalReachabilityChanged)

			if evt.Reachability == network.ReachabilityPrivate {
				if len(ar.relays) < DesiredRelays {
					go func() {
						//如果没有足够的中继可以用,这里需要重新查询可用的中继,需要观察
						ar.findRelaysOnce(ctx)
					}()
				}
			//	log.Debug("network private 2")
			//	// findRelays is a long-lived task (runs up to 2.5 minutes)
			//	// Make sure we only start it once.
			//	if atomic.CompareAndSwapInt32(&ar.findRelaysRunning, 0, 1) {
			//		go func() {
			//			defer atomic.StoreInt32(&ar.findRelaysRunning, 0)
			//			ar.findRelays(ctx)
			//		}()
			//	}
			}

			ar.mx.Lock()
			// if our reachability changed
			if ar.status != evt.Reachability && evt.Reachability != network.ReachabilityUnknown {
				push = true
			}
			ar.status = evt.Reachability
			ar.mx.Unlock()
		case <-ar.relayFound:
			push = true
		case now := <-ticker.C:
			push = ar.refreshReservations(ctx, now)
			if !push{
				if len(ar.relays) < DesiredRelays {
					go func() {
						//如果没有足够的中继可以用,这里需要重新查询可用的中继,需要观察
						ar.findRelaysOnce(ctx)
					}()
				}
			}
		case <-ctx.Done():
			return
		}

		if push {
			ar.mx.Lock()
			ar.cachedAddrs = nil
			ar.mx.Unlock()
			ar.host.SignalAddressChange()
		}
	}
}

//重新保持所有中继
func (ar *AutoRelay) refreshReservations(ctx context.Context, now time.Time) bool {
	ar.mx.Lock()
	if ar.status == network.ReachabilityPublic {
		// 我们是公网,不再使用中继,对这些中继 peer 取消保护
		for p := range ar.relays {
			ar.host.ConnManager().Unprotect(p, autorelayTag)
			delete(ar.relays, p)
		}

		ar.mx.Unlock()
		return true
	}

	if len(ar.relays) == 0 {
		ar.mx.Unlock()
		return false
	}

	// find reservations about to expire and refresh them in parallel
	g := new(errgroup.Group)
	for p, rsvp := range ar.relays {
		if rsvp == nil {
			// this is a circuitv1 relay, there is no reservation
			continue
		}
		//如果还未过期
		if now.Add(rsvpExpirationSlack).Before(rsvp.Expiration) {
			continue
		}

		p := p
		g.Go(func() error { return ar.refreshRelayReservation(ctx, p) })
	}
	ar.mx.Unlock()

	err := g.Wait()
	return err != nil
}

//重新保持中继
func (ar *AutoRelay) refreshRelayReservation(ctx context.Context, p peer.ID) error {
	//尝试保持中继
	rsvp, err := circuitv2.Reserve(ctx, ar.host, peer.AddrInfo{ID: p})

	ar.mx.Lock()
	defer ar.mx.Unlock()

	if err != nil {
		//保持中继失败
		log.Debugf("failed to refresh relay slot reservation with %s: %s", p, err)

		delete(ar.relays, p)
		// unprotect the connection
		ar.host.ConnManager().Unprotect(p, autorelayTag)
	} else {
		//保持中继成功
		log.Debugf("refreshed relay slot reservation with %s", p)
		ar.relays[p] = rsvp
	}

	return err
}

func (ar *AutoRelay) findRelays(ctx context.Context) {
	log.Debug("findRelays")
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for retry := 0; retry < 5; retry++ {
		if retry > 0 {
			log.Debug("no relays connected; retrying in 30s")
			select {
			case <-timer.C:
			case <-ctx.Done():
				return
			}
		}

		if foundAtLeastOneRelay := ar.findRelaysOnce(ctx); foundAtLeastOneRelay {
			return
		}
	}
}

func (ar *AutoRelay) findRelaysOnce(ctx context.Context) bool {
	if ar.findRelaysRunning == 1{
		return false
	}

	ar.findRelaysRunning = 1
	defer func(){
		ar.findRelaysRunning = 0
	}()


	relays, err := ar.discoverRelays(ctx)
	if err != nil {
		log.Debugf("error discovering relays: %s", err)
		return false
	}
	log.Debugf("discovered %d relays", len(relays))
	relays = ar.selectRelays(ctx, relays)
	log.Debugf("selected %d relays", len(relays))

	for n,ppp := range relays{
		log.Debugf("%d relay server %s", n , ppp.ID)
	}

	var found bool
	for _, pi := range relays {
		ar.mx.Lock()
		relayInUse := ar.usingRelay(pi.ID)
		ar.mx.Unlock()
		if relayInUse {
			continue
		}
		rsvp, ok := ar.tryRelay(ctx, pi)
		if !ok {
			continue
		}
		// make sure we're still connected.
		if ar.host.Network().Connectedness(pi.ID) != network.Connected {
			continue
		}
		found = true
		log.Debugf("relay %s success", pi.ID)
		ar.mx.Lock()
		ar.relays[pi.ID] = rsvp  //记录中继
		// protect the connection
		ar.host.ConnManager().Protect(pi.ID, autorelayTag)
		numRelays := len(ar.relays)
		ar.mx.Unlock()

		if numRelays >= DesiredRelays {
			break
		}
	}
	if found {
		ar.relayFound <- struct{}{}
		return true
	}
	return false
}

// usingRelay returns if we're currently using the given relay.
func (ar *AutoRelay) usingRelay(p peer.ID) bool {
	_, ok := ar.relays[p]
	return ok
}

// addRelay adds the given relay to our set of relays.
// returns true when we add a new relay
func (ar *AutoRelay) tryRelay(ctx context.Context, pi peer.AddrInfo) (*circuitv2.Reservation, bool) {
	log.Debugf("tryRelay %s",pi.ID)
	if !ar.connect(ctx, pi) {
		return nil, false
	}
	log.Debugf("connect %s success ",pi.ID)
	protos, err := ar.host.Peerstore().SupportsProtocols(pi.ID, protoIDv1, protoIDv2)
	if err != nil {
		log.Debugf("error checking relay protocol support for peer %s: %s", pi.ID, err)
		return nil, false
	}

	var supportsv1, supportsv2 bool
protoLoop:
	for _, proto := range protos {
		switch proto {
		case protoIDv1:
			supportsv1 = true
		case protoIDv2:
			supportsv2 = true
			break protoLoop
		}
	}

	switch {
	case supportsv2:
		rsvp, err := circuitv2.Reserve(ctx, ar.host, pi)
		if err != nil {
			log.Debugf("error reserving slot with %s: %s", pi.ID, err)
			return nil, false
		}
		return rsvp, true
	case supportsv1:
		ok, err := relayv1.CanHop(ctx, ar.host, pi.ID)
		if err != nil {
			log.Debugf("error querying relay %s for v1 hop: %s", pi.ID, err)
			return nil, false
		}
		return nil, ok
	default: // supports neither, unusable relay.
		return nil, false
	}
}

func (ar *AutoRelay) connect(ctx context.Context, pi peer.AddrInfo) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if len(pi.Addrs) == 0 {
		var err error
		log.Debugf("FindPeer %s",pi.ID)
		pi, err = ar.router.FindPeer(ctx, pi.ID)
		if err != nil {
			log.Debugf("error finding relay peer %s: %s", pi.ID, err.Error())
			return false
		}
	}
	log.Debugf("Connect Peer %s",pi.ID)
	err := ar.host.Connect(ctx, pi)
	if err != nil {
		log.Debugf("error connecting to relay %s: %s", pi.ID, err.Error())
		return false
	}

	log.Debugf("Conns To Peer %s",pi.ID)
	// wait for identify to complete in at least one conn so that we can check the supported protocols
	conns := ar.host.Network().ConnsToPeer(pi.ID)
	if len(conns) == 0 {
		return false
	}

	ready := make(chan struct{}, len(conns))
	for _, conn := range conns {
		go func(conn network.Conn) {
			select {
			case <-ar.host.IDService().IdentifyWait(conn):
				ready <- struct{}{}
			case <-ctx.Done():
			}
		}(conn)
	}

	select {
	case <-ready:
	case <-ctx.Done():
		return false
	}

	return true
}

func (ar *AutoRelay) discoverRelays(ctx context.Context) ([]peer.AddrInfo, error) {
	if len(ar.static) > 0 {
		return ar.static, nil
	}

	//如果缓存还未到期,直接返回
	if ar.discoverRelaysExpireTime.After(time.Now()) && len(ar.discoverRelaysCache) > 5 {
		return ar.discoverRelaysCache,nil
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var ret []peer.AddrInfo
	if ar.discover==nil{
		log.Debug("discover is null")
		return ret,nil
	}
	log.Debug("running discover FindPeers()")
	ch, err := ar.discover.FindPeers(ctx, RelayRendezvous, discovery.Limit(1000))
	if err != nil {
		return nil, err
	}
	for p := range ch {
		ret = append(ret, p)
	}
	ar.discoverRelaysCache = ret
	ar.discoverRelaysExpireTime = time.Now().Add(time.Minute*10) //缓存10分钟
	return ret, nil
}

func (ar *AutoRelay) selectRelays(ctx context.Context, pis []peer.AddrInfo) []peer.AddrInfo {
	// TODO: better relay selection strategy; this just selects random relays,
	// but we should probably use ping latency as the selection metric
	rand.Shuffle(len(pis), func(i, j int) {
		pis[i], pis[j] = pis[j], pis[i]
	})
	return pis
}

// This function is computes the NATed relay addrs when our status is private:
// - The public addrs are removed from the address set.
// - The non-public addrs are included verbatim so that peers behind the same NAT/firewall
//   can still dial us directly.
// - On top of those, we add the relay-specific addrs for the relays to which we are
//   connected. For each non-private relay addr, we encapsulate the p2p-circuit addr
//   through which we can be dialed.
func (ar *AutoRelay) relayAddrs(addrs []ma.Multiaddr) []ma.Multiaddr {
	ar.mx.Lock()
	defer ar.mx.Unlock()

	if ar.status != network.ReachabilityPrivate {
		return addrs
	}

	if ar.cachedAddrs != nil && time.Now().Before(ar.cachedAddrsExpiry) {
		return ar.cachedAddrs
	}

	raddrs := make([]ma.Multiaddr, 0, 4*len(ar.relays)+4)

	// only keep private addrs from the original addr set
	for _, addr := range addrs {
		if manet.IsPrivateAddr(addr) {
			raddrs = append(raddrs, addr)
		}
	}

	// add relay specific addrs to the list
	for p := range ar.relays {
		addrs := cleanupAddressSet(ar.host.Peerstore().Addrs(p))

		circuit, err := ma.NewMultiaddr(fmt.Sprintf("/p2p/%s/p2p-circuit", p.Pretty()))
		if err != nil {
			panic(err)
		}

		for _, addr := range addrs {
			pub := addr.Encapsulate(circuit)
			raddrs = append(raddrs, pub)
		}
	}

	ar.cachedAddrs = raddrs
	ar.cachedAddrsExpiry = time.Now().Add(30 * time.Second)

	return raddrs
}

func (ar *AutoRelay) Close() error {
	ar.ctxCancel()
	ar.refCount.Wait()
	return nil
}
