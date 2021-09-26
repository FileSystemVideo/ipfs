package identify

import (
	"context"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peerstore"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr-net"
)

// ActivationThresh sets how many times an address must be seen as "activated"
// and therefore advertised to other peers as an address that the local peer
// can be contacted on. The "seen" events expire by default after 40 minutes
// (OwnObservedAddressTTL * ActivationThreshold). The are cleaned up during
// the GC rounds set by GCInterval.
var ActivationThresh = 4

// GCInterval specicies how often to make a round cleaning seen events and
// observed addresses. An address will be cleaned if it has not been seen in
// OwnObservedAddressTTL (10 minutes). A "seen" event will be cleaned up if
// it is older than OwnObservedAddressTTL * ActivationThresh (40 minutes).
var GCInterval = 10 * time.Minute

// observedAddrManagerWorkerChannelSize defines how many addresses can be enqueued
// for adding to an ObservedAddrManager.
var observedAddrManagerWorkerChannelSize = 16

type observation struct {
	seenTime      time.Time
	connDirection network.Direction
}

// ObservedAddr is an entry for an address reported by our peers.
// We only use addresses that:
// - have been observed at least 4 times in last 40 minutes. (counter symmetric nats)
// - have been observed at least once recently (10 minutes), because our position in the
//   network, or network port mapppings, may have changed.
type ObservedAddr struct {
	Addr     ma.Multiaddr
	SeenBy   map[string]observation // peer(observer) address -> observation info
	LastSeen time.Time
}

func (oa *ObservedAddr) activated(ttl time.Duration) bool {
	// We only activate if other peers observed the same address
	// of ours at least 4 times. SeenBy peers are removed by GC if
	// they say the address more than ttl*ActivationThresh
	return len(oa.SeenBy) >= ActivationThresh
}

type newObservation struct {
	conn     network.Conn
	observed ma.Multiaddr
}

// ObservedAddrManager keeps track of a ObservedAddrs.
type ObservedAddrManager struct {
	host host.Host

	// latest observation from active connections
	// we'll "re-observe" these when we gc
	activeConnsMu sync.Mutex
	// active connection -> most recent observation
	activeConns map[network.Conn]ma.Multiaddr

	mu sync.RWMutex
	// local(internal) address -> list of observed(external) addresses
	addrs        map[string][]*ObservedAddr
	ttl          time.Duration
	refreshTimer *time.Timer

	// this is the worker channel
	wch chan newObservation
}

// NewObservedAddrManager returns a new address manager using
// peerstore.OwnObservedAddressTTL as the TTL.
func NewObservedAddrManager(ctx context.Context, host host.Host) *ObservedAddrManager {
	oas := &ObservedAddrManager{
		addrs:       make(map[string][]*ObservedAddr),
		ttl:         peerstore.OwnObservedAddrTTL,
		wch:         make(chan newObservation, observedAddrManagerWorkerChannelSize),
		host:        host,
		activeConns: make(map[network.Conn]ma.Multiaddr),
		// refresh every ttl/2 so we don't forget observations from connected peers
		refreshTimer: time.NewTimer(peerstore.OwnObservedAddrTTL / 2),
	}
	oas.host.Network().Notify((*obsAddrNotifiee)(oas))
	go oas.worker(ctx)
	return oas
}

// AddrsFor return all activated observed addresses associated with the given
// (resolved) listen address.
func (oas *ObservedAddrManager) AddrsFor(addr ma.Multiaddr) (addrs []ma.Multiaddr) {
	oas.mu.RLock()
	defer oas.mu.RUnlock()

	if len(oas.addrs) == 0 {
		return nil
	}

	key := string(addr.Bytes())
	observedAddrs, ok := oas.addrs[key]
	if !ok {
		return
	}

	now := time.Now()
	for _, a := range observedAddrs {
		if now.Sub(a.LastSeen) <= oas.ttl && a.activated(oas.ttl) {
			addrs = append(addrs, a.Addr)
		}
	}

	return addrs
}

// Addrs return all activated observed addresses
func (oas *ObservedAddrManager) Addrs() (addrs []ma.Multiaddr) {
	oas.mu.RLock()
	defer oas.mu.RUnlock()

	if len(oas.addrs) == 0 {
		return nil
	}

	now := time.Now()
	for _, observedAddrs := range oas.addrs {
		for _, a := range observedAddrs {
			if now.Sub(a.LastSeen) <= oas.ttl && a.activated(oas.ttl) {
				addrs = append(addrs, a.Addr)
			}
		}
	}
	return addrs
}

// Record records an address observation, if valid.
func (oas *ObservedAddrManager) Record(conn network.Conn, observed ma.Multiaddr) {
	select {
	case oas.wch <- newObservation{
		conn:     conn,
		observed: observed,
	}:
	default:
		log.Debugw("dropping address observation due to full buffer",
			"from", conn.RemoteMultiaddr(),
			"observed", observed,
		)
	}
}

func (oas *ObservedAddrManager) teardown() {
	oas.host.Network().StopNotify((*obsAddrNotifiee)(oas))

	oas.mu.Lock()
	oas.refreshTimer.Stop()
	oas.mu.Unlock()
}

func (oas *ObservedAddrManager) worker(ctx context.Context) {
	defer oas.teardown()

	ticker := time.NewTicker(GCInterval)
	defer ticker.Stop()

	hostClosing := oas.host.Network().Process().Closing()
	for {
		select {
		case obs := <-oas.wch:
			oas.maybeRecordObservation(obs.conn, obs.observed)
		case <-ticker.C:
			oas.gc()
		case <-oas.refreshTimer.C:
			oas.refresh()
		case <-hostClosing:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (oas *ObservedAddrManager) refresh() {
	oas.activeConnsMu.Lock()
	recycledObservations := make([]newObservation, 0, len(oas.activeConns))
	for conn, observed := range oas.activeConns {
		recycledObservations = append(recycledObservations, newObservation{
			conn:     conn,
			observed: observed,
		})
	}
	oas.activeConnsMu.Unlock()

	oas.mu.Lock()
	defer oas.mu.Unlock()
	for _, obs := range recycledObservations {
		oas.recordObservationUnlocked(obs.conn, obs.observed)
	}
	// refresh every ttl/2 so we don't forget observations from connected peers
	oas.refreshTimer.Reset(oas.ttl / 2)
}

func (oas *ObservedAddrManager) gc() {
	oas.mu.Lock()
	defer oas.mu.Unlock()

	now := time.Now()
	for local, observedAddrs := range oas.addrs {
		filteredAddrs := observedAddrs[:0]
		for _, a := range observedAddrs {
			// clean up SeenBy set
			for k, ob := range a.SeenBy {
				if now.Sub(ob.seenTime) > oas.ttl*time.Duration(ActivationThresh) {
					delete(a.SeenBy, k)
				}
			}

			// leave only alive observed addresses
			if now.Sub(a.LastSeen) <= oas.ttl {
				filteredAddrs = append(filteredAddrs, a)
			}
		}
		if len(filteredAddrs) > 0 {
			oas.addrs[local] = filteredAddrs
		} else {
			delete(oas.addrs, local)
		}
	}
}

func (oas *ObservedAddrManager) addConn(conn network.Conn, observed ma.Multiaddr) {
	oas.activeConnsMu.Lock()
	defer oas.activeConnsMu.Unlock()

	// We need to make sure we haven't received a disconnect event for this
	// connection yet. The only way to do that right now is to make sure the
	// swarm still has the connection.
	//
	// Doing this under a lock that we _also_ take in a disconnect event
	// handler ensures everything happens in the right order.
	for _, c := range oas.host.Network().ConnsToPeer(conn.RemotePeer()) {
		if c == conn {
			oas.activeConns[conn] = observed
			return
		}
	}
}

func (oas *ObservedAddrManager) removeConn(conn network.Conn) {
	// DO NOT remove this lock.
	// This ensures we don't call addConn at the same time:
	// 1. see that we have a connection and pause inside addConn right before recording it.
	// 2. process a disconnect event.
	// 3. record the connection (leaking it).

	oas.activeConnsMu.Lock()
	delete(oas.activeConns, conn)
	oas.activeConnsMu.Unlock()
}

func (oas *ObservedAddrManager) maybeRecordObservation(conn network.Conn, observed ma.Multiaddr) {

	// First, determine if this observation is even worth keeping...

	// Ignore observations from loopback nodes. We already know our loopback
	// addresses.
	if manet.IsIPLoopback(observed) {
		return
	}

	// we should only use ObservedAddr when our connection's LocalAddr is one
	// of our ListenAddrs. If we Dial out using an ephemeral addr, knowing that
	// address's external mapping is not very useful because the port will not be
	// the same as the listen addr.
	ifaceaddrs, err := oas.host.Network().InterfaceListenAddresses()
	if err != nil {
		log.Infof("failed to get interface listen addrs", err)
		return
	}

	local := conn.LocalMultiaddr()
	if !addrInAddrs(local, ifaceaddrs) && !addrInAddrs(local, oas.host.Network().ListenAddresses()) {
		// not in our list
		return
	}

	// We should reject the connection if the observation doesn't match the
	// transports of one of our advertised addresses.
	if !HasConsistentTransport(observed, oas.host.Addrs()) {
		log.Debugw(
			"observed multiaddr doesn't match the transports of any announced addresses",
			"from", conn.RemoteMultiaddr(),
			"observed", observed,
		)
		return
	}

	// Ok, the observation is good, record it.
	log.Debugw("added own observed listen addr", "observed", observed)

	defer oas.addConn(conn, observed)

	oas.mu.Lock()
	defer oas.mu.Unlock()
	oas.recordObservationUnlocked(conn, observed)
}

func (oas *ObservedAddrManager) recordObservationUnlocked(conn network.Conn, observed ma.Multiaddr) {
	now := time.Now()
	observerString := observerGroup(conn.RemoteMultiaddr())
	localString := string(conn.LocalMultiaddr().Bytes())
	ob := observation{
		seenTime:      now,
		connDirection: conn.Stat().Direction,
	}

	observedAddrs := oas.addrs[localString]
	// check if observed address seen yet, if so, update it
	for i, previousObserved := range observedAddrs {
		if previousObserved.Addr.Equal(observed) {
			observedAddrs[i].SeenBy[observerString] = ob
			observedAddrs[i].LastSeen = now
			return
		}
	}
	// observed address not seen yet, append it
	oas.addrs[localString] = append(oas.addrs[localString], &ObservedAddr{
		Addr: observed,
		SeenBy: map[string]observation{
			observerString: ob,
		},
		LastSeen: now,
	})
}

// observerGroup is a function that determines what part of
// a multiaddr counts as a different observer. for example,
// two ipfs nodes at the same IP/TCP transport would get
// the exact same NAT mapping; they would count as the
// same observer. This may protect against NATs who assign
// different ports to addresses at different IP hosts, but
// not TCP ports.
//
// Here, we use the root multiaddr address. This is mostly
// IP addresses. In practice, this is what we want.
func observerGroup(m ma.Multiaddr) string {
	//TODO: If IPv6 rolls out we should mark /64 routing zones as one group
	first, _ := ma.SplitFirst(m)
	return string(first.Bytes())
}

// SetTTL sets the TTL of an observed address manager.
func (oas *ObservedAddrManager) SetTTL(ttl time.Duration) {
	oas.mu.Lock()
	defer oas.mu.Unlock()
	oas.ttl = ttl
	// refresh every ttl/2 so we don't forget observations from connected peers
	oas.refreshTimer.Reset(ttl / 2)
}

// TTL gets the TTL of an observed address manager.
func (oas *ObservedAddrManager) TTL() time.Duration {
	oas.mu.RLock()
	defer oas.mu.RUnlock()
	return oas.ttl
}

type obsAddrNotifiee ObservedAddrManager

func (on *obsAddrNotifiee) Listen(n network.Network, a ma.Multiaddr)      {}
func (on *obsAddrNotifiee) ListenClose(n network.Network, a ma.Multiaddr) {}
func (on *obsAddrNotifiee) Connected(n network.Network, v network.Conn)   {}
func (on *obsAddrNotifiee) Disconnected(n network.Network, v network.Conn) {
	(*ObservedAddrManager)(on).removeConn(v)
}
func (on *obsAddrNotifiee) OpenedStream(n network.Network, s network.Stream) {}
func (on *obsAddrNotifiee) ClosedStream(n network.Network, s network.Stream) {}
