package through

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"
	"github.com/libp2p/go-libp2p-core/event"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/routing"
	coredis "github.com/libp2p/go-libp2p-core/discovery"
	p2pthrough "github.com/libp2p/go-libp2p-through"
	"github.com/libp2p/go-libp2p-discovery"
	basic "github.com/libp2p/go-libp2p/p2p/host/basic"
	"strings"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/multiformats/go-multiaddr-net"
)

const (
	ThroughRendezvous = "/libp2p/through"
)

var (
	//最少所需穿透服务器的数量
	DesiredThroughs = 2
	//ping发送服务数量
	PingThroughs = 2

	RegistDelay = 2 * time.Minute

	BootDelay = 20 * time.Second
)

// 默认穿透服务器
var DefaultThroughs = []string{
	//"/ip4/117.82.138.15/udp/4001/quic/p2p/QmZCRQVT5VB6LfQ6thAsmzrpYXFzYbMsqpPSx9ee6KLNnK",
	//"/ip4/43.246.208.209/udp/4001/quic/p2p/QmRo3PiWPJro9k5jrghbgNsP63YjJVy7Z9fF57asCTyRA9",
}

// ThroughClient 是一个穿透客户端，当检测到NAT时，它使用穿透服务端进行注册连接。
type ThroughClient struct {
	host     *basic.BasicHost
	discover coredis.Discoverer
	router   routing.PeerRouting
	addrsF   basic.AddrsFactory
	ctx      context.Context
	static []peer.AddrInfo
	natStatus network.Reachability		//nat状态

	disconnect chan peer.ID  //穿透服务器断开时消息通道推送
	pingChan chan peer.ID	 //穿透服务器断开时心跳消息通道推送

	mx     sync.Mutex
	throughs map[peer.ID]struct{}				//当前已连接的穿透服务器的列表
	pingServer map[peer.ID]struct{}				//当前正在发送心跳包的服务器
	discoveredService map[peer.ID]struct{}		//新发现的穿透服务端Peer id

	cachedAddrs       []ma.Multiaddr
	cachedAddrsExpiry time.Time
}

func NewThroughClient(ctx context.Context, bhost *basic.BasicHost, discover coredis.Discoverer, router routing.PeerRouting, static []peer.AddrInfo) *ThroughClient {
	ar := &ThroughClient{
		host:       bhost,
		discover:   discover,
		router:     router,
		addrsF:     bhost.AddrsFactory,
		static:     static,
		throughs:     		make(map[peer.ID]struct{}),
		pingServer: 		make(map[peer.ID]struct{}),
		disconnect: 		make(chan peer.ID, 1),
		pingChan: 			make(chan peer.ID, 1),
		natStatus: network.ReachabilityUnknown,
		discoveredService:	make(map[peer.ID]struct{}),
		ctx: ctx,
	}
	bhost.AddrsFactory = ar.hostAddrs // 给主机增加地址过滤器
	bhost.Network().Notify(ar) // 注册网络状态消息订阅
	go ar.background(ctx)
	return ar
}

func (ar *ThroughClient) baseAddrs() []ma.Multiaddr {
	return ar.addrsF(ar.host.AllAddrs())
}

//返回当前的主机地址
func (ar *ThroughClient) hostAddrs(addrs []ma.Multiaddr) []ma.Multiaddr {
	return ar.throughAddrs(ar.addrsF(addrs))
}


func (ar *ThroughClient) background(ctx context.Context) {
	log.Info("ThroughClient->background()")
	subReachability, _ := ar.host.EventBus().Subscribe(new(event.EvtLocalReachabilityChanged))
	defer subReachability.Close()

	// when true, we need to identify push
	push := false

	for {
		select {
		case ev, ok := <-subReachability.Out():
			if !ok {
				return
			}

			evt, ok := ev.(event.EvtLocalReachabilityChanged)
			if !ok {
				return
			}
			log.Info("current nat status:",evt.Reachability,"host nat status:",ar.host.GetNatStatus())
			ar.host.SetNatStatus(evt.Reachability) //更新主机的nat状态
			//log.Info("update host nat status after:",ar.host.GetNatStatus())
			var update bool
			//如果是私有网络
			if evt.Reachability == network.ReachabilityPrivate {
				// 禁用穿透服务端
				p2pthrough.SetServiceStatus(false)
				// TODO: this is a long-lived (2.5min task) that should get spun up in a separate thread
				// and canceled if the relay learns the nat is now public.
				update = ar.findThroughServer(ctx)
			} else if evt.Reachability == network.ReachabilityPublic{
				// 启用穿透服务端
				p2pthrough.SetServiceStatus(true)

				// 将自己是 穿透服务端 的消息广播到dht网络中
				Advertise(ctx, ar.discover.(*discovery.RoutingDiscovery))
			}

			ar.mx.Lock()
			if update || (ar.natStatus != evt.Reachability && evt.Reachability != network.ReachabilityUnknown) {
				push = true
			}
			ar.natStatus = evt.Reachability
			ar.mx.Unlock()
		case <-ar.disconnect:
			push = true
		case <-ctx.Done():
			return
		}

		if push {
			ar.mx.Lock()
			ar.cachedAddrs = nil
			ar.mx.Unlock()
			push = false
			ar.host.SignalAddressChange()
		}
	}
}

// 查找穿透服务端
func (ar *ThroughClient) findThroughServer(ctx context.Context) bool {
	log.Infof("findThroughServer()-------------------")
	//如果注册穿透服务器大于2个则退出注册
	if ar.numThroughs() >= DesiredThroughs {
		return false
	}
	update := false
	retry := 0
	for { //如果找不到穿透服务器,则一直阻塞在这里
		if retry > 0 {
			log.Infof("through service not available; retrying %d in 30s",retry)
			select {
			case <-time.After(30 * time.Second): //每次执行前等待30秒
			case <-ctx.Done():
				return update
			}
		}
		log.Infof("findThroughServerOnce()-------------------")
		update = ar.findThroughServerOnce(ctx) || update
		if ar.numThroughs() > 0 {
			return update
		}
		retry++
	}
	return update
}


// dht查找穿透服务端
func (ar *ThroughClient) findThroughServerOnce(ctx context.Context) bool {
	//发现穿透服务
	pis, err := ar.discoverThroughServer(ctx)
	if err != nil {
		log.Infof("error search through server: %s", err)
		return false
	}
	log.Infof("search through server from %d peer", len(pis))

	for k, pi := range pis {
		log.Infof("peer %d :%s",k, pi.ID)
	}

	//选择要使用的穿透服务器
	pis = ar.selectThroughServer(ctx, pis)
	//log.Infof("selected %d throughs", len(pis))

	update := false
	for _, pi := range pis {
		if ar.registerRetry(pi) {
			update = ar.tryThroughServer(ctx, pi) || update
			//如果注册穿透服务器大于2个则退出注册
			if ar.numThroughs() >= DesiredThroughs {
				break
			}
		}
	}
	return update
}

var RegisterLimit sync.Map
func (ar *ThroughClient) registerRetry(pi peer.AddrInfo) bool {
	if timeUnix,ok := RegisterLimit.Load(pi.ID);ok {
		timeInt64 := timeUnix.(int64)
		//如果当前时间大于延迟时间则可以重新注册
		if time.Now().Unix()> timeInt64 {
			RegisterLimit.Delete(pi.ID)
			return true
		}
		log.Infof("peer %s regist retring after %d s",pi.ID.String(),timeInt64 - time.Now().Unix())
		return false
	}
	return true
}

func (ar *ThroughClient) numThroughs() int {
	ar.mx.Lock()
	defer ar.mx.Unlock()
	return len(ar.throughs)
}

// 如果我们当前正在使用指定的穿透服务器，则返回 true
func (ar *ThroughClient) usingThrough(p peer.ID) bool {
	ar.mx.Lock()
	defer ar.mx.Unlock()
	_, ok := ar.throughs[p]
	return ok
}


// 将指定的穿透服务器添加到我们的地址集。
// 添加成功时返回true
func (ar *ThroughClient) tryThroughServer(ctx context.Context, pi peer.AddrInfo) bool {
	// 判断穿透服务器是否已经使用
	if ar.usingThrough(pi.ID) {
		return false
	}

	// 连接到穿透服务器,判断是否成功
	if !ar.connect(ctx, pi) {
		return false
	}

	addrInfo := peer.AddrInfo{ID: ar.host.ID(), Addrs: ar.host.Addrs()} //自己的地址

	// 询问对方是否支持 穿透服务
	ok, err := p2pthrough.SendCanService(ctx, ar.host, pi.ID, addrInfo)
	if err != nil {
		log.Warnf("error can service querying: %s", err.Error())
		return false
	}

	//如果不支持,则返回false
	if !ok {
		return false
	}

	ar.mx.Lock()
	defer ar.mx.Unlock()

	if len(ar.throughs) >= DesiredThroughs {
		return false
	}
	//log.Infof("--------1---------")

	// 尝试 向 穿透服务 注册
	var remoteAddr ma.Multiaddr
	remoteAddr, ok, err = p2pthrough.SendRegister(ctx, ar.host, pi.ID, addrInfo)
	if err != nil {
		log.Infof("error register client: %s", err.Error())
		//穿透服务器注册达到上限
		if strings.Contains(err.Error(),"register limit"){
			//给当前节点设置延迟注册
			RegisterLimit.Store(pi.ID,time.Now().Add(RegistDelay).Unix())
		}
		return false
	}

	//log.Infof("---------2--------")

	//注册失败,返回
	if !ok {
		log.Warn("register client failed")
		return false
	}

	// 确保当前已经建立连接并且是有效的。
	if ar.host.Network().Connectedness(pi.ID) != network.Connected {
		log.Warn("connect status invalid")
		return false
	}

	//保存已连接的
	ar.throughs[pi.ID] = struct{}{}
	//刷新缓存地址
	ar.cachedAddrs = nil
	//如果当前有发送的心跳包，就不再发送
	if len(ar.pingServer)>= PingThroughs {
		return true
	}
	//开启协程发送心跳包
	go ar.pingFun(ctx,pi.ID,remoteAddr,addrInfo)
	return true
}
//发送心跳包
func (ar *ThroughClient) pingFun(ctx context.Context,pid peer.ID,remoteAddr ma.Multiaddr,addrInfo peer.AddrInfo)  {
	log.Infof("connect to through service %s[%s] success.",pid, remoteAddr)
	//检查连接是否已断开,断开则退出循环
	var serverPid = pid
	ar.pingServer[serverPid] = struct{}{}
	forSign:
	for{
		select {
		case pid := <-ar.pingChan:	//检测到穿透服务器断开,
			log.Infof("through service break off pid:",pid,",serverId:",serverPid)
			if serverPid == pid{ //如果是当前发送心跳包的服务器断开,则停止发送心跳包
				delete(ar.pingServer,serverPid)
				//判断是否还有可用的穿透服务器
				if len(ar.throughs)>0{
					for k:= range ar.throughs {
						log.Infof("find available through service pid:",k)
						go ar.pingFun(ctx,k,remoteAddr,addrInfo)
						break forSign
					}
				}
				break forSign
			}
		case <-time.After(time.Second*30): //此处默认阻塞30秒
			break
		}
		//每隔30秒一次心跳包
		status,err := p2pthrough.SendAlivePing(ctx, ar.host, pid, addrInfo)
		if !status{
			errInfo := ""
			if err != nil{
				errInfo = err.Error()
			}
			log.Infof("error client ping: %s", errInfo)
		}
	}
	log.Infof("error service disconnect: %s",remoteAddr)
}


// 连接到穿透服务器
func (ar *ThroughClient) connect(ctx context.Context, pi peer.AddrInfo) bool {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	pid := pi.ID
	if len(pi.Addrs) == 0 {
		var err error
		pi, err = ar.router.FindPeer(ctx, pi.ID)
		if err != nil {
			log.Infof("failed to get address %s: %s", pid, err.Error())
			return false
		}
	}

	err := ar.host.Connect(ctx, pi)
	if err != nil {
		log.Infof("error connecting to %s: %s", pid, err.Error())
		return false
	}

	// 将连接标记为非常重要
	//ar.host.ConnManager().TagPeer(pi.ID, "relay", 42)
	return true
}

//通过dht查找穿透服务端节点
func (ar *ThroughClient) discoverThroughServer(ctx context.Context) ([]peer.AddrInfo, error) {
	//// 如果已经定义静态穿透服务地址,则使用静态的
	//if len(ar.static) > 0 {
	//	return ar.static, nil
	//}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	peers,err := discovery.FindPeers(ctx, ar.discover, ThroughRendezvous, coredis.Limit(1000))
	if err == nil{
		//将探测到的穿透服务端加入到列表

	}
	return peers,err
}

func (ar *ThroughClient) selectThroughServer(ctx context.Context, pis []peer.AddrInfo) []peer.AddrInfo {
	//将发现的服务端加入到列表中
	var nonexistent bool
	addrInfos := []peer.AddrInfo{}
	for pid, _ := range ar.discoveredService {
		nonexistent = true
		for _, v := range pis {
			if v.ID == pid {
				nonexistent = false
				break
			}
		}
		//如果发现的services是新的
		if nonexistent{
			addr := peer.AddrInfo{pid,[]ma.Multiaddr{}}
			addrInfos = append(addrInfos,addr)
		}
	}
	// todo 这里只是选择随机选择穿透服务器，更好的穿透选择策略应该使用ping延迟作为选择参考
	shuffleThroughs(pis)

	//将发现的服务端插入到前面
	if len(addrInfos) > 0{
		pis = append(addrInfos,pis...)
	}
	return pis
}

// 当我们的状态为private时，这个函数计算 NaT 的 穿透地址
// - 公共地址将从地址集中删除
// - 非公共地址不会删除，这样同一NAT/防火墙后面的对等方仍然可以直接拨打我们的电话。
// - 除此之外，我们还为所连接的穿透服务器添加了特定于穿透的地址。
// 对于每个非私有中继地址，我们封装了p2p穿透地址，通过它我们让别人找到我们
func (ar *ThroughClient) throughAddrs(addrs []ma.Multiaddr) []ma.Multiaddr {
	ar.mx.Lock()
	defer ar.mx.Unlock()

	//如果当前不是nat 私有环境则返回
	if ar.natStatus != network.ReachabilityPrivate {
		return addrs
	}

	//如果缓存时间未过,返回
	if ar.cachedAddrs != nil && time.Now().Before(ar.cachedAddrsExpiry) {
		return ar.cachedAddrs
	}

	raddrs := make([]ma.Multiaddr, 0, 4*len(ar.throughs)+4)

	// 只保留原始地址集中的私有地址
	for _, addr := range addrs {
		if manet.IsPrivateAddr(addr) {
			raddrs = append(raddrs, addr)
		}
	}

	// 将穿透服务器的地址添加到列表中
	for p := range ar.throughs {
		addrs := cleanupAddressSet(ar.host.Peerstore().Addrs(p))

		circuit, err := ma.NewMultiaddr(fmt.Sprintf("/p2p/%s/p2p-through", p.Pretty()))
		if err != nil {
			panic(err)
		}

		for _, addr := range addrs {
			pub := addr.Encapsulate(circuit)
			raddrs = append(raddrs, pub)
		}
	}

	ar.cachedAddrs = raddrs
	ar.cachedAddrsExpiry = time.Now().Add(30 * time.Minute)  //地址更新间隔30秒

	return raddrs
}

// 随机打乱地址
func shuffleThroughs(pis []peer.AddrInfo) {
	for i := range pis {
		j := rand.Intn(i + 1)
		pis[i], pis[j] = pis[j], pis[i]
	}
}

// Notifee
func (ar *ThroughClient) Listen(network.Network, ma.Multiaddr)      {}
func (ar *ThroughClient) ListenClose(network.Network, ma.Multiaddr) {}
func (ar *ThroughClient) Connected(n network.Network,c  network.Conn)   {

	_, err := c.RemoteMultiaddr().ValueForProtocol(ma.P_THROUGH)
	if err == nil{
		return
	}
	go func(pid peer.ID) {
		ctx, cancel := context.WithTimeout(ar.ctx, time.Second*3)
		defer cancel()

		addr := peer.AddrInfo{ID: ar.host.ID(), Addrs: ar.host.Addrs()}
		canService, err := p2pthrough.SendCanService(ctx, ar.host, pid,addr)
		if err != nil {
			log.Infof("Error testing through can service: %s", err.Error())
			return
		}

		if canService {
			ar.host.Peerstore().SetProtocols(pid,"/libp2p/autonat/1.0.0") //标记该节点支持autonat协议,加快nat识别的速度
			log.Infof("Discovered through service %s", pid.Pretty())
			ar.mx.Lock()
			if _,ok := ar.discoveredService[pid]; !ok{
				ar.discoveredService[pid] = struct{}{}
			}
			ar.mx.Unlock()
			if ar.natStatus == network.ReachabilityPrivate {
				ar.findThroughServer(ctx)
			}
		}
	}(c.RemotePeer())
}

func (ar *ThroughClient) Disconnected(net network.Network, c network.Conn) {
	p := c.RemotePeer()

	ar.mx.Lock()
	defer ar.mx.Unlock()

	if ar.host.Network().Connectedness(p) == network.Connected {
		// 还有其他连接可以用
		return
	}

	//如果和穿透服务器断开了
	if _, ok := ar.throughs[p]; ok {
		delete(ar.throughs, p)
		ar.disconnect <- p
		ar.pingChan <- p
		//如果2S后没有消费,将自动消费
		time.After(time.Second*2)
		select {
		case <-ar.pingChan:
		default:
		}


		/*select {
		case ar.disconnect <- p:
		default:
		}*/
	}
}

func (ar *ThroughClient) OpenedStream(network.Network, network.Stream) {}
func (ar *ThroughClient) ClosedStream(network.Network, network.Stream) {}
