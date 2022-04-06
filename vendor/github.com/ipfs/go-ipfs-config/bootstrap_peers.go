package config

import (
	"errors"
	"fmt"

	peer "github.com/libp2p/go-libp2p-core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

// DefaultBootstrapAddresses are the hardcoded bootstrap addresses
// for IPFS. they are nodes run by the IPFS team. docs on these later.
// As with all p2p networks, bootstrap is an important security concern.
//
// NOTE: This is here -- and not inside cmd/ipfs/init.go -- because of an
// import dependency issue. TODO: move this into a config/default/ package.
var DefaultBootstrapAddresses = []string{
	"/dns4/node2.fs.video/udp/4001/quic/p2p/QmPKrgbXygetwBJ3LGE1YJRy9PTvTiKvCVjE2DrmyLrj1s",
	"/dns4/node3.fs.video/udp/4001/quic/p2p/QmQnRsB8Bfapn8LVxeiVmx17ipDNWL9MM3Jm2t1PYsCjkq",
	"/dns4/node4.fs.video/udp/4001/quic/p2p/QmRrPe3r9uTQHFFaaQmdBEtYEUtGjju2SPGn23KEtqhDbf",
	"/dns4/node5.fs.video/udp/4001/quic/p2p/QmWSzS3KPavbkGj7HkKWUDWvQedtm45LWxzD8uUfrFp7iN",
	"/dns4/node6.fs.video/udp/4001/quic/p2p/QmeVwnjXNyQKZ7oyRxfk3EUxU1UTxEkyv35kzY8iwiAiUh",
	"/dns4/node7.fs.video/udp/4001/quic/p2p/QmVoAuag1bd6bVE5gHqCZFdqw3Ha3ESLE5q9S6cxnU9zUA",
	"/dns4/node8.fs.video/udp/4001/quic/p2p/QmaSmYPSPUS4BJL6YvFgPJEjk6zr1144xCAiuat1ZtR6Mf",
	"/dns4/node9.fs.video/udp/4001/quic/p2p/QmQL63zegiX3TXPrT6Trj45MRQctxafKsUyuyd5Fwnvvjm",
}

// ErrInvalidPeerAddr signals an address is not a valid peer address.
var ErrInvalidPeerAddr = errors.New("invalid peer address")

func (c *Config) BootstrapPeers() ([]peer.AddrInfo, error) {
	return ParseBootstrapPeers(c.Bootstrap)
}

// DefaultBootstrapPeers returns the (parsed) set of default bootstrap peers.
// if it fails, it returns a meaningful error for the user.
// This is here (and not inside cmd/ipfs/init) because of module dependency problems.
func DefaultBootstrapPeers() ([]peer.AddrInfo, error) {
	ps, err := ParseBootstrapPeers(DefaultBootstrapAddresses)
	if err != nil {
		return nil, fmt.Errorf(`failed to parse hardcoded bootstrap peers: %s
This is a problem with the ipfs codebase. Please report it to the dev team`, err)
	}
	return ps, nil
}

func (c *Config) SetBootstrapPeers(bps []peer.AddrInfo) {
	c.Bootstrap = BootstrapPeerStrings(bps)
}

// ParseBootstrapPeer parses a bootstrap list into a list of AddrInfos.
func ParseBootstrapPeers(addrs []string) ([]peer.AddrInfo, error) {
	maddrs := make([]ma.Multiaddr, len(addrs))
	for i, addr := range addrs {
		var err error
		maddrs[i], err = ma.NewMultiaddr(addr)
		if err != nil {
			return nil, err
		}
	}
	return peer.AddrInfosFromP2pAddrs(maddrs...)
}

// BootstrapPeerStrings formats a list of AddrInfos as a bootstrap peer list
// suitable for serialization.
func BootstrapPeerStrings(bps []peer.AddrInfo) []string {
	bpss := make([]string, 0, len(bps))
	for _, pi := range bps {
		addrs, err := peer.AddrInfoToP2pAddrs(&pi)
		if err != nil {
			// programmer error.
			panic(err)
		}
		for _, addr := range addrs {
			bpss = append(bpss, addr.String())
		}
	}
	return bpss
}
