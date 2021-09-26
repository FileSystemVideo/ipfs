package swarm

import (
	"errors"
	manet "github.com/multiformats/go-multiaddr-net"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/libp2p/go-libp2p-core/peer"
	"time"
)

var(
	ErrPingToSelf = errors.New("ping to self attempted")
	ErrPingInvalidAddress = errors.New("ping target all address invalid")
)


func checkThroughAddr(multiaddr ma.Multiaddr) bool {
	for j:=0;j<len(multiaddr.Protocols());j++{
		if multiaddr.Protocols()[j].Code == ma.P_THROUGH{
			return true
		}
	}
	return false
}

//向对方发送ping
func (s *Swarm) Ping(p peer.ID,timeout time.Duration) error {
	if s.local == p {
		return ErrPingToSelf
	}
	addrs := s.Peerstore().Addrs(p)
	validAddress := 0 //有效的地址
	for i:=0;i<len(addrs);i++{
		addr := addrs[i]
		//是 穿透注册地址 跳过
		if checkThroughAddr(addr){
			continue
		}
		//不是公网地址，跳过
		if !manet.IsPublicAddr(addr){
			continue
		}
		//如果无法匹配到传输器，跳过
		tpt := s.TransportForDialing(addr)
		if tpt == nil {
			continue
		}
		validAddress++
		log.Debugf("%s swarm ping %s %s", s.local, p, addr)
		go tpt.Ping(addr,timeout)
	}
	if validAddress==0{
		return ErrPingInvalidAddress
	}
	select {
	case <-time.After(timeout):
		return nil
	}
	return nil
}