package through

import (
	"context"
	"time"

	"github.com/libp2p/go-libp2p-discovery"
	core "github.com/libp2p/go-libp2p-core/discovery"
	ma "github.com/multiformats/go-multiaddr"
)

var (
	// this is purposefully long to require some node stability before advertising as a relay
	AdvertiseBootDelay = 15 * time.Minute
	AdvertiseTTL       = 30 * time.Minute
)

// A将这个节点作为libp2p穿透服务发布。
func Advertise(ctx context.Context, advertise core.Advertiser) {
	go func() {
		select {
		case <-time.After(AdvertiseBootDelay):
			discovery.Advertise(ctx, advertise, ThroughRendezvous, core.TTL(AdvertiseTTL))
		case <-ctx.Done():
		}
	}()
}


// 过滤器过滤掉所有穿透地址。
func Filter(addrs []ma.Multiaddr) []ma.Multiaddr {
	raddrs := make([]ma.Multiaddr, 0, len(addrs))
	for _, addr := range addrs {
		if isThroughAddr(addr) {
			continue
		}
		raddrs = append(raddrs, addr)
	}
	return raddrs
}
