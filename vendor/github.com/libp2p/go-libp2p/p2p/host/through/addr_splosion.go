package through

import (
	"encoding/binary"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr-net"
)


// 此函数用于清除穿透服务的地址集，以删除专用地址并缩减addrsplosion。
func cleanupAddressSet(addrs []ma.Multiaddr) []ma.Multiaddr {
	var public, private []ma.Multiaddr
	for _, a := range addrs {
		if isThroughAddr(a) {
			continue
		}
		if manet.IsPublicAddr(a) || isDNSAddr(a) {
			public = append(public, a)
			continue
		}
		// discard unroutable addrs
		if manet.IsPrivateAddr(a) {
			private = append(private, a)
		}
	}

	if !hasAddrsplosion(public) {
		return public
	}

	return sanitizeAddrsplodedSet(public, private)
}

//检查指定的地址是否使用了穿透服务
func isThroughAddr(a ma.Multiaddr) bool {
	isThrough := false
	ma.ForEach(a, func(c ma.Component) bool {
		switch c.Protocol().Code {
		case ma.P_THROUGH:
			isThrough = true
			return false
		default:
			return true
		}
	})

	return isThrough
}

func isDNSAddr(a ma.Multiaddr) bool {
	if first, _ := ma.SplitFirst(a); first != nil {
		switch first.Protocol().Code {
		case ma.P_DNS4, ma.P_DNS6, ma.P_DNSADDR:
			return true
		}
	}
	return false
}

// 检查同一个协议是否开启了多个端口号
func hasAddrsplosion(addrs []ma.Multiaddr) bool {
	aset := make(map[string]int)

	for _, a := range addrs {
		key, port := addrKeyAndPort(a)
		xport, ok := aset[key]
		if ok && port != xport {
			return true
		}
		aset[key] = port
	}

	return false
}

//返回地址的协议名称和端口号
func addrKeyAndPort(a ma.Multiaddr) (string, int) {
	var (
		key  string
		port int
	)

	ma.ForEach(a, func(c ma.Component) bool {
		switch c.Protocol().Code {
		case ma.P_TCP, ma.P_UDP:
			port = int(binary.BigEndian.Uint16(c.RawValue()))
			key += "/" + c.Protocol().Name
		default:
			val := c.Value()
			if val == "" {
				val = c.Protocol().Name
			}
			key += "/" + val
		}
		return true
	})

	return key, port
}

/*
清除地址
//使用以下启发式：
//-对于每个基地址/协议组合，如果有多个端口公布，则
//仅接受默认端口（如果存在）。
//-如果默认端口不存在，我们将通过跟踪检查非标准端口
//专用端口绑定（如果存在）。
//-如果没有默认或专用端口绑定，则无法推断正确的
//端口并放弃并返回所有地址（对于该基址）
 */
func sanitizeAddrsplodedSet(public, private []ma.Multiaddr) []ma.Multiaddr {
	type portAndAddr struct {
		addr ma.Multiaddr
		port int
	}

	privports := make(map[int]struct{})
	pubaddrs := make(map[string][]portAndAddr)

	for _, a := range private {
		_, port := addrKeyAndPort(a)
		privports[port] = struct{}{}
	}

	for _, a := range public {
		key, port := addrKeyAndPort(a)
		pubaddrs[key] = append(pubaddrs[key], portAndAddr{addr: a, port: port})
	}

	var result []ma.Multiaddr
	for _, pas := range pubaddrs {
		if len(pas) == 1 {
			// it's not addrsploded
			result = append(result, pas[0].addr)
			continue
		}

		haveAddr := false
		for _, pa := range pas {
			if _, ok := privports[pa.port]; ok {
				// it matches a privately bound port, use it
				result = append(result, pa.addr)
				haveAddr = true
				continue
			}

			if pa.port == 4001 || pa.port == 4002 {
				// it's a default port, use it
				result = append(result, pa.addr)
				haveAddr = true
			}
		}

		if !haveAddr {
			// we weren't able to select a port; bite the bullet and use them all
			for _, pa := range pas {
				result = append(result, pa.addr)
			}
		}
	}

	return result
}
