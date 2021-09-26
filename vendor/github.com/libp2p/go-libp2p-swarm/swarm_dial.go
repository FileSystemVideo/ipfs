package swarm

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/transport"
	lgbl "github.com/libp2p/go-libp2p-loggables"

	logging "github.com/ipfs/go-log"
	addrutil "github.com/libp2p/go-addr-util"
	ma "github.com/multiformats/go-multiaddr"
)

// Diagram of dial sync:
//
//   many callers of Dial()   synched w.  dials many addrs       results to callers
//  ----------------------\    dialsync    use earliest            /--------------
//  -----------------------\              |----------\           /----------------
//  ------------------------>------------<-------     >---------<-----------------
//  -----------------------|              \----x                 \----------------
//  ----------------------|                \-----x                \---------------
//                                         any may fail          if no addr at end
//                                                             retry dialAttempt x

var (
	// ErrDialBackoff is returned by the backoff code when a given peer has
	// been dialed too frequently
	ErrDialBackoff = errors.New("dial backoff")

	// ErrDialToSelf is returned if we attempt to dial our own peer
	ErrDialToSelf = errors.New("dial to self attempted")

	// ErrNoTransport is returned when we don't know a transport for the
	// given multiaddr.
	ErrNoTransport = errors.New("no transport for protocol")

	// ErrAllDialsFailed is returned when connecting to a peer has ultimately failed
	ErrAllDialsFailed = errors.New("all dials failed")

	// ErrNoAddresses is returned when we fail to find any addresses for a
	// peer we're trying to dial.
	ErrNoAddresses = errors.New("no addresses")

	// ErrNoGoodAddresses is returned when we find addresses for a peer but
	// can't use any of them.
	ErrNoGoodAddresses = errors.New("no good addresses")
)

// DialAttempts governs how many times a goroutine will try to dial a given peer.
// Note: this is down to one, as we have _too many dials_ atm. To add back in,
// add loop back in Dial(.)
const DialAttempts = 1

// ConcurrentFdDials is the number of concurrent outbound dials over transports
// that consume file descriptors
const ConcurrentFdDials = 160

// DefaultPerPeerRateLimit is the number of concurrent outbound dials to make
// per peer
const DefaultPerPeerRateLimit = 8

// dialbackoff is a struct used to avoid over-dialing the same, dead peers.
// Whenever we totally time out on a peer (all three attempts), we add them
// to dialbackoff. Then, whenevers goroutines would _wait_ (dialsync), they
// check dialbackoff. If it's there, they don't wait and exit promptly with
// an error. (the single goroutine that is actually dialing continues to
// dial). If a dial is successful, the peer is removed from backoff.
// Example:
//
//  for {
//  	if ok, wait := dialsync.Lock(p); !ok {
//  		if backoff.Backoff(p) {
//  			return errDialFailed
//  		}
//  		<-wait
//  		continue
//  	}
//  	defer dialsync.Unlock(p)
//  	c, err := actuallyDial(p)
//  	if err != nil {
//  		dialbackoff.AddBackoff(p)
//  		continue
//  	}
//  	dialbackoff.Clear(p)
//  }
//

// DialBackoff is a type for tracking peer dial backoffs.
//
// * It's safe to use its zero value.
// * It's thread-safe.
// * It's *not* safe to move this type after using.
type DialBackoff struct {
	entries map[peer.ID]map[string]*backoffAddr
	lock    sync.RWMutex
}

type backoffAddr struct {
	tries int
	until time.Time
}

func (db *DialBackoff) init(ctx context.Context) {
	if db.entries == nil {
		db.entries = make(map[peer.ID]map[string]*backoffAddr)
	}
	go db.background(ctx)
}

func (db *DialBackoff) background(ctx context.Context) {
	ticker := time.NewTicker(BackoffMax)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			db.cleanup()
		}
	}
}

// Backoff returns whether the client should backoff from dialing
// peer p at address addr
func (db *DialBackoff) Backoff(p peer.ID, addr ma.Multiaddr) (backoff bool) {
	db.lock.Lock()
	defer db.lock.Unlock()

	ap, found := db.entries[p][string(addr.Bytes())]
	return found && time.Now().Before(ap.until)
}

// BackoffBase is the base amount of time to backoff (default: 5s).
var BackoffBase = time.Second * 5

// BackoffCoef is the backoff coefficient (default: 1s).
var BackoffCoef = time.Second

// BackoffMax is the maximum backoff time (default: 5m).
var BackoffMax = time.Minute * 5

// AddBackoff lets other nodes know that we've entered backoff with
// peer p, so dialers should not wait unnecessarily. We still will
// attempt to dial with one goroutine, in case we get through.
//
// Backoff is not exponential, it's quadratic and computed according to the
// following formula:
//
//     BackoffBase + BakoffCoef * PriorBackoffs^2
//
// Where PriorBackoffs is the number of previous backoffs.
func (db *DialBackoff) AddBackoff(p peer.ID, addr ma.Multiaddr) {
	saddr := string(addr.Bytes())
	db.lock.Lock()
	defer db.lock.Unlock()
	bp, ok := db.entries[p]
	if !ok {
		bp = make(map[string]*backoffAddr, 1)
		db.entries[p] = bp
	}
	ba, ok := bp[saddr]
	if !ok {
		bp[saddr] = &backoffAddr{
			tries: 1,
			until: time.Now().Add(BackoffBase),
		}
		return
	}

	backoffTime := BackoffBase + BackoffCoef*time.Duration(ba.tries*ba.tries)
	if backoffTime > BackoffMax {
		backoffTime = BackoffMax
	}
	ba.until = time.Now().Add(backoffTime)
	ba.tries++
}

// Clear removes a backoff record. Clients should call this after a
// successful Dial.
func (db *DialBackoff) Clear(p peer.ID) {
	db.lock.Lock()
	defer db.lock.Unlock()
	delete(db.entries, p)
}

func (db *DialBackoff) cleanup() {
	db.lock.Lock()
	defer db.lock.Unlock()
	now := time.Now()
	for p, e := range db.entries {
		good := false
		for _, backoff := range e {
			backoffTime := BackoffBase + BackoffCoef*time.Duration(backoff.tries*backoff.tries)
			if backoffTime > BackoffMax {
				backoffTime = BackoffMax
			}
			if now.Before(backoff.until.Add(backoffTime)) {
				good = true
				break
			}
		}
		if !good {
			delete(db.entries, p)
		}
	}
}

// DialPeer connects to a peer.
//
// The idea is that the client of Swarm does not need to know what network
// the connection will happen over. Swarm can use whichever it choses.
// This allows us to use various transport protocols, do NAT traversal/relay,
// etc. to achieve connection.
func (s *Swarm) DialPeer(ctx context.Context, p peer.ID) (network.Conn, error) {
	return s.dialPeer(ctx, p)
}

// internal dial method that returns an unwrapped conn
//
// It is gated by the swarm's dial synchronization systems: dialsync and
// dialbackoff.
func (s *Swarm) dialPeer(ctx context.Context, p peer.ID) (*Conn, error) {
	log.Debugf("[%s] swarm dialing peer [%s]", s.local, p)
	var logdial = lgbl.Dial("swarm", s.LocalPeer(), p, nil, nil)
	err := p.Validate()
	if err != nil {
		return nil, err
	}

	if p == s.local {
		log.Event(ctx, "swarmDialSelf", logdial)
		return nil, ErrDialToSelf
	}

	defer log.EventBegin(ctx, "swarmDialAttemptSync", p).Done()

	// check if we already have an open connection first
	conn := s.bestConnToPeer(p)
	if conn != nil {
		return conn, nil
	}

	// apply the DialPeer timeout
	ctx, cancel := context.WithTimeout(ctx, network.GetDialPeerTimeout(ctx))
	defer cancel()

	conn, err = s.dsync.DialLock(ctx, p)
	if err == nil {
		return conn, nil
	}

	log.Debugf("network for %s finished dialing %s", s.local, p)

	if ctx.Err() != nil {
		// Context error trumps any dial errors as it was likely the ultimate cause.
		return nil, ctx.Err()
	}

	if s.ctx.Err() != nil {
		// Ok, so the swarm is shutting down.
		return nil, ErrSwarmClosed
	}

	return nil, err
}


// doDial是一种丑陋的的垫片方法，它保留了旧dialsync代码的所有日志记录和回退逻辑
// 拨号的实际执行方法
func (s *Swarm) doDial(ctx context.Context, p peer.ID) (*Conn, error) {
	// 当我们使用拨号锁时，我们可能已经与对方建立了连接。.
	c := s.bestConnToPeer(p)
	if c != nil {
		return c, nil
	}

	logdial := lgbl.Dial("swarm", s.LocalPeer(), p, nil, nil)

	// 好的，我们被要求拨号！我们开始吧。如果成功，dial将把 conn 添加到 swarm 本身。
	defer log.EventBegin(ctx, "swarmDialAttemptStart", logdial).Done()

	// 拨号并建立连接
	conn, err := s.dial(ctx, p)
	if err != nil {
		conn = s.bestConnToPeer(p)
		if conn != nil {
			// 嗯? 什么错误?
			// 我们可以取消拨号，因为我们收到了一个连接或其他一些随机的原因。
			// 只需忽略错误并返回连接。
			log.Debugf("ignoring dial error because we have a connection: %s", err)
			return conn, nil
		}

		// 好的,我们失败了。
		return nil, err
	}
	return conn, nil
}

func (s *Swarm) canDial(addr ma.Multiaddr) bool {
	t := s.TransportForDialing(addr)
	return t != nil && t.CanDial(addr)
}

// dial 是 swarm 实际的拨号逻辑
func (s *Swarm) dial(ctx context.Context, p peer.ID) (*Conn, error) {
	// 准备拨号元数据
	var logdial = lgbl.Dial("swarm", s.LocalPeer(), p, nil, nil)
	if p == s.local {
		log.Event(ctx, "swarmDialDoDialSelf", logdial)
		return nil, ErrDialToSelf
	}
	defer log.EventBegin(ctx, "swarmDialDo", logdial).Done()
	logdial["dial"] = "failure"  // 从 failure 开始。最后设为 success

	sk := s.peers.PrivKey(s.local)
	logdial["encrypted"] = sk != nil // log whether this will be an encrypted dial or not.
	if sk == nil {
		// 如果sk为 nil 就好了，只要记录下。
		log.Debug("Dial not given PrivateKey, so WILL NOT SECURE conn.")
	}

	//////
	/*
		This slice-to-chan code is temporary, the peerstore can currently provide
		a channel as an interface for receiving addresses, but more thought
		needs to be put into the execution. For now, this allows us to use
		the improved rate limiter, while maintaining the outward behaviour
		that we previously had (halting a dial when we run out of addrs)
	*/
	peerAddrs := s.peers.Addrs(p)
	if len(peerAddrs) == 0 {
		return nil, &DialError{Peer: p, Cause: ErrNoAddresses}
	}
	goodAddrs := s.filterKnownUndialables(peerAddrs)
	if len(goodAddrs) == 0 {
		return nil, &DialError{Peer: p, Cause: ErrNoGoodAddresses}
	}
	goodAddrsChan := make(chan ma.Multiaddr, len(goodAddrs))
	nonBackoff := false
	for _, a := range goodAddrs {
		// skip addresses in back-off
		if !s.backf.Backoff(p, a) {
			nonBackoff = true
			goodAddrsChan <- a
		}
	}
	close(goodAddrsChan)
	if !nonBackoff {
		return nil, ErrDialBackoff
	}
	/////////

	// 尝试连接到任何地址
	connC, dialErr := s.dialAddrs(ctx, p, goodAddrsChan)
	if dialErr != nil {
		logdial["error"] = dialErr.Cause.Error()
		switch dialErr.Cause {
		case context.Canceled, context.DeadlineExceeded:
			// Always prefer the context errors as we rely on being
			// able to check them.
			//
			// Removing this will BREAK backoff (causing us to
			// backoff when canceling dials).
			return nil, dialErr.Cause
		}
		return nil, dialErr
	}
	logdial["conn"] = logging.Metadata{
		"localAddr":  connC.LocalMultiaddr(),
		"remoteAddr": connC.RemoteMultiaddr(),
	}
	swarmC, err := s.addConn(connC, network.DirOutbound)
	if err != nil {
		logdial["error"] = err.Error()
		connC.Close() // close the connection. didn't work out :(
		return nil, &DialError{Peer: p, Cause: err}
	}

	logdial["dial"] = "success"
	return swarmC, nil
}

// filterKnownUndialables takes a list of multiaddrs, and removes those
// that we definitely don't want to dial: addresses configured to be blocked,
// IPv6 link-local addresses, addresses without a dial-capable transport,
// and addresses that we know to be our own.
// This is an optimization to avoid wasting time on dials that we know are going to fail.
func (s *Swarm) filterKnownUndialables(addrs []ma.Multiaddr) []ma.Multiaddr {
	lisAddrs, _ := s.InterfaceListenAddresses()
	var ourAddrs []ma.Multiaddr
	for _, addr := range lisAddrs {
		protos := addr.Protocols()
		// we're only sure about filtering out /ip4 and /ip6 addresses, so far
		if len(protos) == 2 && (protos[0].Code == ma.P_IP4 || protos[0].Code == ma.P_IP6) {
			ourAddrs = append(ourAddrs, addr)
		}
	}

	return addrutil.FilterAddrs(addrs,
		addrutil.SubtractFilter(ourAddrs...),
		s.canDial,
		// TODO: Consider allowing link-local addresses
		addrutil.AddrOverNonLocalIP,
		addrutil.FilterNeg(s.Filters.AddrBlocked),
	)
}

//拨号到目标地址返回可用链接
func (s *Swarm) dialAddrs(ctx context.Context, p peer.ID, remoteAddrs <-chan ma.Multiaddr) (transport.CapableConn, *DialError) {
	log.Debugf("%s swarm dialing %s", s.local, p)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel() // cancel work when we exit func

	// 使用单一的响应类型，而不是错误和连接，降低了复杂性*a ton*
	respch := make(chan dialResult)
	err := &DialError{Peer: p}

	defer s.limiter.clearAllPeerDials(p)

	var active int
dialLoop:
	for remoteAddrs != nil || active > 0 {
		// 首先检查上下文取消和/或响应。
		select {
		case <-ctx.Done():
			break dialLoop
		case resp := <-respch:
			active--
			if resp.Err != nil {
				// 错误是正常的，许多拨号会失败
				if resp.Err != context.Canceled {
					s.backf.AddBackoff(p, resp.Addr)
				}

				log.Infof("got error on dial: %s", resp.Err)
				err.recordErr(resp.Addr, resp.Err)
			} else if resp.Conn != nil {
				return resp.Conn, nil
			}

			// 我们得到了一个结果，再试一次。
			continue
		default:
		}

		// 现在，尝试拨号。
		select {
		case addr, ok := <-remoteAddrs:
			if !ok {
				remoteAddrs = nil
				continue
			}

			s.limitedDial(ctx, p, addr, respch)
			active++
		case <-ctx.Done():
			break dialLoop
		case resp := <-respch:
			active--
			if resp.Err != nil {
				// 错误是正常的，许多拨号会失败
				if resp.Err != context.Canceled {
					s.backf.AddBackoff(p, resp.Addr)
				}

				log.Infof("got error on dial: %s", resp.Err)
				err.recordErr(resp.Addr, resp.Err)
			} else if resp.Conn != nil {
				return resp.Conn, nil
			}
		}
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		err.Cause = ctxErr
	} else if len(err.DialErrors) == 0 {
		err.Cause = network.ErrNoRemoteAddrs
	} else {
		err.Cause = ErrAllDialsFailed
	}
	return nil, err
}

// limitedDial will start a dial to the given peer when
// it is able, respecting the various different types of rate
// limiting that occur without using extra goroutines per addr
func (s *Swarm) limitedDial(ctx context.Context, p peer.ID, a ma.Multiaddr, resp chan dialResult) {
	s.limiter.AddDialJob(&dialJob{
		addr: a,
		peer: p,
		resp: resp,
		ctx:  ctx,
	})
}

//拨号实际执行的底层方法
func (s *Swarm) dialAddr(ctx context.Context, p peer.ID, addr ma.Multiaddr) (transport.CapableConn, error) {
	// Just to double check. Costs nothing.
	if s.local == p {
		return nil, ErrDialToSelf
	}
	log.Debugf("%s swarm dialing %s %s", s.local, p, addr)

	tpt := s.TransportForDialing(addr)
	if tpt == nil {
		return nil, ErrNoTransport
	}

	//调用传输器的拨号
	connC, err := tpt.Dial(ctx, addr, p)
	if err != nil {
		return nil, err
	}

	// Trust the transport? Yeah... right.
	if connC.RemotePeer() != p {
		connC.Close()
		err = fmt.Errorf("BUG in transport %T: tried to dial %s, dialed %s", p, connC.RemotePeer(), tpt)
		log.Error(err)
		return nil, err
	}

	// success! we got one!
	return connC, nil
}