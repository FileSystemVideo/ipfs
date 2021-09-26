package through

import (
	"context"
	"fmt"
	"sync"
	"time"
	pb "github.com/libp2p/go-libp2p-through/pb"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/peerstore"
	ggio "github.com/gogo/protobuf/io"
	tptu "github.com/libp2p/go-libp2p-transport-upgrader"
	logging "github.com/ipfs/go-log"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr-net"
)

var log = logging.Logger("through")

const ProtoID = "/libp2p/through/0.1.0"		//协议id

const maxMessageSize = 4096

var (
	StreamAcceptTimeout   = 10 * time.Second
	HopConnectTimeout    = 10 * time.Second
	StopHandshakeTimeout = 1 * time.Minute

	HopStreamBufferSize = 4096
	ClientInteractiveDelay = 3 * time.Second		//服务端发给两个需要通讯的客户端，让他们延迟多久进行连接
	ClientInteractiveTicker = time.Millisecond*200	//客户端之间通讯的时候，检查约定时间是否到的每次间隔

	RegistrationLimit = 1000	//客户端注册服务端数量限制
)

//当前接入的客户端信息
type ClientInfo struct {
	stream network.Stream
	timeDiff int64			//时间差
}

// Through  是穿透功能 服务端和服务端的实现
type Through struct {
	host     				host.Host
	upgrader 				*tptu.Upgrader
	ctx      				context.Context
	self     				peer.ID

	incoming chan *Conn						//和穿透服务器建立的连接
	clients map[peer.ID]*ClientInfo			//当前接入的所有客户端
	clientsMapLock *sync.Mutex
	mx     sync.Mutex

	// atomic counters
	streamCount  int32
	liveHopCount int32
}


type ThroughError struct {
	Code pb.Through_Status
}

func (e ThroughError) Error() string {
	return fmt.Sprintf("error opening through: %s (%d)", pb.Through_Status_name[int32(e.Code)], e.Code)
}

// 构建穿透模块
func NewThrough(ctx context.Context,h host.Host, upgrader *tptu.Upgrader) (*Through, error) {
	log.Debug("NewThrough()-------------")

	r := &Through{
		upgrader: upgrader,
		host:     h,
		ctx:      ctx,
		self:     h.ID(),
		incoming: make(chan *Conn),
		clients: make(map[peer.ID]*ClientInfo),
		clientsMapLock: &sync.Mutex{},
	}

	//订阅穿透协议的连接消息
	h.SetStreamHandler(ProtoID, r.handleNewStream)

	//注册网络状态消息订阅
	h.Network().Notify(r) // 注册网络状态消息订阅

	return r, nil
}


//处理穿透的链接消息
func (r *Through) handleNewStream(s network.Stream) {

	s.Protocol()
	rd := newDelimitedReader(s, maxMessageSize)
	defer rd.Close()
	var msg pb.Through

	err := rd.ReadMsg(&msg)
	if err != nil {
		r.handleError(s, pb.Through_MALFORMED_MESSAGE) //畸形的消息
		return
	}

	log.Infof("receive (%s) message from: %s [%s]", msg.GetType().Enum().String(), s.Conn().RemotePeer(), s.Conn().RemoteMultiaddr().String())

	switch msg.GetType() {
	case pb.Through_SERVICE:  // 客户端->服务端(请求链接其他人)
		r.handleThroughStream(s, &msg)
	case pb.Through_DISPOSE: //  服务端->客户端(通知有人要链接它)
		r.handleDisposeStream(s, &msg)
	case pb.Through_CAN_SERVICE: // 客户端->服务端(查询是否支持穿透)
		r.handleCanService(s, &msg)
	case pb.Through_REGISTER: // 客户端->服务端(注册登记)
		r.handleRegister(s,&msg)
	case pb.Through_PING: // 客户端->服务端（已注册 Peer 的心跳包）
		r.handlePing(s,&msg)
	default:
		log.Warnf("unexpected relay handshake: %d", msg.GetType())
		r.handleError(s, pb.Through_MALFORMED_MESSAGE)
	}
}


//请求穿透服务，返回计划打洞的时间
func (self *Through) SendService(rd *delimitedReader,wr ggio.WriteCloser,s network.Stream,src *pb.Through_Peer,dst *pb.Through_Peer) (int64,error) {
	var msg pb.Through
	msg.Type = pb.Through_SERVICE.Enum()
	msg.SrcPeer = src
	msg.DstPeer = dst
	msg.Time = time.Now().UnixNano()

	//向 服务端 请求穿透
	log.Infof("send (%s) message to %s [%s]", msg.Type.String() , s.Conn().RemotePeer() , s.Conn().RemoteMultiaddr().String())
	err := wr.WriteMsg(&msg)
	if err != nil {
		s.Reset()
		return 0,err
	}

	msg.Reset()

	//等待消息返回
	err = rd.ReadMsg(&msg)
	if err != nil {
		return 0,err
	}

	log.Infof("receive (%s) message from %s [%s] code:(%s)",msg.Type.String(), s.Conn().RemotePeer(), s.Conn().RemoteMultiaddr().String(), msg.GetCode().String())

	//如果服务端返回的不是状态消息
	if msg.GetType() != pb.Through_STATUS {
		return 0,fmt.Errorf("unexpected through response; not a status message (%d)", msg.GetType())
	}

	//如果服务端返回的不是成功
	if msg.GetCode() != pb.Through_SUCCESS {
		return 0,ThroughError{msg.GetCode()}
	}

	dstPeer,err := peerToPeerInfo(msg.GetDstPeer())
	if err==nil && len(dstPeer.Addrs) > 0 {
		log.Info("dst peer length:",len(dstPeer.Addrs))
		self.host.Peerstore().AddAddrs(dstPeer.ID, dstPeer.Addrs, peerstore.TempAddrTTL) //保存目标地址2分钟
		log.Infof("update peer: %s of new addr: %s", dstPeer.ID, dstPeer.Addrs[0])
	}

	return msg.Time,nil
}

//使用穿透服务 service 对 dest 进行连接
func (r *Through) DialPeer(ctx context.Context, service peer.AddrInfo, dest peer.AddrInfo) (*Conn, error) {

	log.Infof("pass service %s dial peer %s", service.ID, dest.ID)

	if len(service.Addrs) > 0 {
		r.host.Peerstore().AddAddrs(service.ID, service.Addrs, peerstore.AddressTTL) //保存这个地址1小时
	}

	//建立和 service 的流
	log.Debug("DialPeer----------1")
	s, err := r.host.NewStream(ctx, service.ID, ProtoID)
	if err != nil {
		return nil, err
	}

	rd := newDelimitedReader(s, maxMessageSize)
	wr := newDelimitedWriter(s)
	defer rd.Close()

	log.Debug("DialPeer----------2")
	srcPeerInfo := r.host.Peerstore().PeerInfo(r.self)


	/*******************************************************/
	//过滤监听地址只保留公网地址
	checkThroughAddr := func(multiaddr ma.Multiaddr) bool {
		for j:=0;j<len(multiaddr.Protocols());j++{
			if multiaddr.Protocols()[j].Code == ma.P_THROUGH{
				return true
			}
		}
		return false
	}
	hostAddrs := r.host.Addrs()
	for i:=0;i<len(hostAddrs);i++{
		hAddr := hostAddrs[i]
		//如果不是注册的穿透地址，并且是公网
		if !checkThroughAddr(hAddr) && manet.IsPublicAddr(hAddr){
			srcPeerInfo.Addrs = append(srcPeerInfo.Addrs,hAddr) //源peer附加当前的公网地址
		}
	}
	/*******************************************************/
	srcPeer := peerInfoToPeer(srcPeerInfo)
	dstPeer := peerInfoToPeer(dest)


	log.Debug("DialPeer----------3")
	//发送请求穿透的消息,这里会阻塞等待服务端通知目标主机来连接,成功以后服务端会带来目标主机的真实地址
	planThroughTime,err := r.SendService(rd,wr,s,srcPeer,dstPeer)
	if err != nil{
		s.Reset()
		return nil, err
	}

	log.Infof("Through->DialPeer() planTime:",time.Unix(0,planThroughTime).Format("2006-01-02 15:04:05.999999999"))

	jishiqi := time.NewTicker(ClientInteractiveTicker)
	outerFor:
	for{
		select {
		case <-jishiqi.C:
			log.Info(time.Now().UnixNano(),">=",planThroughTime, planThroughTime,time.Now().UnixNano() >= planThroughTime)
			if time.Now().UnixNano() >= planThroughTime{ //如果当前时间大于等于约定好的时间
				break outerFor
			}
		}
	}

	log.Debug("DialPeer----------4")

	//底层发送ping包，用于打洞
	err = r.host.Ping(dest,time.Second*6)
	log.Debug("PingPeer---------- error:",err)

	//尝试连接到目标主机,10秒超时
	dstStream, err := r.host.NewStream(network.WithDialPeerTimeout(ctx,time.Second*5), dest.ID)
	if err != nil {
		r.host.Peerstore().ClearAddrs(dest.ID)  //丢弃目标地址,这样下次再链接的时候 ，才能继续使用穿透服务
		log.Debug("DialPeer----------4 error:",err)
		return nil, err
	}
	log.Debug("DialPeer----------5")
	return &Conn{stream: dstStream, remote: dest, host: r.host}, nil
}

//检查指定的地址是否属于穿透地址
func (r *Through) Matches(addr ma.Multiaddr) bool {
	// TODO: Look at the prefix transport as well.
	_, err := addr.ValueForProtocol(ma.P_THROUGH)
	return err == nil
}

//新建与指定peer id 的流连接，并发送消息 msg
func sendStreamMessage(ctx context.Context, host host.Host, id peer.ID, msg pb.Through) (ma.Multiaddr, bool, error) {
	s, err := host.NewStream(ctx, id, ProtoID)
	if err != nil {
		return nil,false, err
	}
	multiaddr := s.Conn().RemoteMultiaddr() //远程mul地址
	rd := newDelimitedReader(s, maxMessageSize)
	wr := newDelimitedWriter(s)
	defer rd.Close()

	if err := wr.WriteMsg(&msg); err != nil {
		s.Reset()
		return multiaddr,false, err
	}

	log.Infof("send (%s) message to %s [%s]", msg.Type.String(), id ,s.Conn().RemoteMultiaddr().String(),)
	msg.Reset()

	if err := rd.ReadMsg(&msg); err != nil {
		s.Reset()
		return multiaddr,false, err
	}


	log.Infof("receive (%s) message from %s [%s] code: %s", msg.Type.String() ,id, s.Conn().RemoteMultiaddr().String(), msg.GetCode().String())

	s.Close() //关闭流

	if msg.GetType() != pb.Through_STATUS {
		return multiaddr,false, fmt.Errorf("unexpected through response; not a status message (%d)", msg.GetType())
	}

	if msg.GetCode() == pb.Through_REGISTER_LIMIT {
		return multiaddr,false,	fmt.Errorf("register limit")
	}

	return multiaddr, msg.GetCode() == pb.Through_SUCCESS, nil
}

// 向指定的穿透服务器发送心跳包
func SendAlivePing(ctx context.Context, host host.Host, id peer.ID,addr peer.AddrInfo) (bool, error) {
	var msg pb.Through
	msg.SrcPeer = peerInfoToPeer(addr)
	msg.Type = pb.Through_PING.Enum()
	msg.Time = time.Now().UnixNano()
	_,result,err := sendStreamMessage(ctx, host, id, msg)
	return result,err
}

// 向指定的穿透服务器注册
func SendRegister(ctx context.Context, host host.Host, id peer.ID, addr peer.AddrInfo) (ma.Multiaddr, bool, error) {
	var msg pb.Through
	msg.SrcPeer = peerInfoToPeer(addr)
	msg.Type = pb.Through_REGISTER.Enum()
	msg.Time = time.Now().UnixNano()
	return sendStreamMessage(ctx, host, id, msg)
}

// 查询一个peer是否支持穿透服务
func SendCanService(ctx context.Context, host host.Host, id peer.ID, addr peer.AddrInfo) (bool, error) {
	var msg pb.Through
	msg.SrcPeer = peerInfoToPeer(addr)
	msg.Type = pb.Through_CAN_SERVICE.Enum()
	msg.Time = time.Now().UnixNano()
	_,result,err := sendStreamMessage(ctx, host, id, msg)
	return result,err
}

//布尔值表示当前是否开启服务端
var serviceStatus = false


//获取当前服务端状态
func GetServiceStatus() bool {
	return serviceStatus
}

//更新当前服务端状态
func SetServiceStatus(result bool) {
	serviceStatus = result
}

func (r *Through) CanService(ctx context.Context, id peer.ID) (bool, error) {
	addr := peer.AddrInfo{ID: r.host.ID(), Addrs: r.host.Addrs()}
	return SendCanService(ctx, r.host, id, addr)
}


//计算时间误差
func (self *Through) calculationTimeDiff(msgTime int64) int64 {
	return msgTime - time.Now().UnixNano()
	//1613720947345176976
	//             000000
}


//处理【已注册Peer的心跳包】的请求
func (self *Through) handlePing(s network.Stream, msg *pb.Through) {
	//如果当前未开启 穿透服务 则返回
	if !GetServiceStatus() {
		self.handleError(s, pb.Through_SERVICE_CANT_SPEAK_THROUGH)
		return
	}

	//解码源peer信息
	src, err := peerToPeerInfo(msg.GetSrcPeer())
	if err != nil {
		self.handleError(s, pb.Through_SERVICE_SRC_MULTIADDR_INVALID)
		return
	}

	//如果未注册
	srcClientInfo,ok := self.clients[src.ID]
	if !ok{
		self.handleError(s, pb.Through_PING_UNREGISTER)
		return
	}

	//更新时间差
	if srcClientInfo.timeDiff == 0 || srcClientInfo.timeDiff > msg.Time {
		srcClientInfo.timeDiff = self.calculationTimeDiff(msg.Time)
	}

	totalMillsecond := srcClientInfo.timeDiff/time.Millisecond.Nanoseconds()
	log.Info("peer:",src.ID," time diff: ",totalMillsecond,"Millsecond")


	//给源peer发送成功消息
	err = self.writeResponse(s, pb.Through_SUCCESS,nil)
	if err != nil {
		log.Infof("error writing through ping response: %s", err.Error())
		s.Reset()
		return
	}
}

// 写入客户端信息
func (self *Through) putClientInfo(pid peer.ID,info *ClientInfo){
	self.clientsMapLock.Lock()
	defer self.clientsMapLock.Unlock()
	self.clients[pid] = info
}

//删除客户端信息
func (self *Through) deleteClientInfo(pid peer.ID){
	//不存在时直接返回
	if _,ok := self.clients[pid];!ok{
		return
	}
	self.clientsMapLock.Lock()
	defer self.clientsMapLock.Unlock()
	delete(self.clients,pid)
}

//处理【Peer注册】的请求
func (self *Through) handleRegister(s network.Stream, msg *pb.Through) {
	//解码源peer信息
	src, err := peerToPeerInfo(msg.GetSrcPeer())
	if err != nil {
		log.Infof("handleRegister error %s",err)
		self.handleError(s, pb.Through_SERVICE_SRC_MULTIADDR_INVALID)
		return
	}

	//限制客户端注册服务端数量
	registerCount := len(self.clients)
	log.Infof("throuth service regist num : %d",registerCount)
	if registerCount >= RegistrationLimit {
		log.Infof("RegistrationLimit error Maximum limit")
		self.handleError(s, pb.Through_REGISTER_LIMIT)
		return
	}

	//将peer信息持久化保存
	self.putClientInfo(src.ID,&ClientInfo{
		stream: s,
		timeDiff: self.calculationTimeDiff(msg.Time),
	})

	//给源peer发送成功消息
	err = self.writeResponse(s, pb.Through_SUCCESS,nil)
	if err != nil {
		log.Infof("error writing through register response: %s", err.Error())
		s.Reset()
		return
	}
}

// 处理请求穿透的消息
func (r *Through) handleThroughStream(s network.Stream, msg *pb.Through) {

	//如果当前未开启 穿透服务端 则返回
	if !GetServiceStatus() {
		r.handleError(s, pb.Through_SERVICE_CANT_SPEAK_THROUGH)
		return
	}

	// 源 peer
	src, err := peerToPeerInfo(msg.GetSrcPeer())
	if err != nil {
		r.handleError(s, pb.Through_SERVICE_SRC_MULTIADDR_INVALID)
		return
	}

	if src.ID != s.Conn().RemotePeer() {
		r.handleError(s, pb.Through_SERVICE_SRC_MULTIADDR_INVALID)
		return
	}

	// 目标 peer
	dst, err := peerToPeerInfo(msg.GetDstPeer())
	if err != nil {
		r.handleError(s, pb.Through_SERVICE_DST_MULTIADDR_INVALID)
		return
	}

	if dst.ID == r.self {
		r.handleError(s, pb.Through_SERVICE_CANT_RELAY_TO_SELF)
		return
	}

	//如果目标peer未注册
	dstClientInfo,ok := r.clients[dst.ID]
	if !ok{
		r.handleError(s, pb.Through_SERVICE_TARGET_UNREGISTERED)
		return
	}

	var srcTimeDiff int64
	//如果源peer未注册,自动计算时间偏移
	srcCLientInfo,ok := r.clients[src.ID]
	if !ok{
		srcTimeDiff = r.calculationTimeDiff(msg.Time)
	}else{
		srcTimeDiff = srcCLientInfo.timeDiff
	}
	/*
	之后按此逻辑进行处理:
	1.计算协商链接的时间
	2.给源peer发送连接目标peer的消息
	3.给目标peer发送连接源peer的消息
	*/
	ctx, cancel := context.WithTimeout(r.ctx, HopConnectTimeout)
	defer cancel()

	//创建到目标peer的流
	bs, err := r.host.NewStream(ctx, dst.ID, ProtoID)
	if err != nil {
		log.Infof("error opening through stream to %s: %s", dst.ID.Pretty(), err.Error())
		if err == network.ErrNoConn {
			r.handleError(s, pb.Through_SERVICE_NO_CONN_TO_DST)
		} else {
			r.handleError(s, pb.Through_SERVICE_CANT_DIAL_DST)
		}
		return
	}



	wr := newDelimitedWriter(bs)

	// 设置空闲超时,超过该时间没有数据，则自动断开链接
	bs.SetDeadline(time.Now().Add(StopHandshakeTimeout))

	nowTimestamp := time.Now().UnixNano()


	msg.Type = pb.Through_DISPOSE.Enum()
	src.Addrs = append(src.Addrs, s.Conn().RemoteMultiaddr()) //增加源peer得地址
	msg.SrcPeer = peerInfoToPeer(src) //更新消息源peer
	msg.Time = nowTimestamp

	// 给目标peer发送 穿透处理 消息
	err = wr.WriteMsg(msg)
	if err != nil {
		log.Infof("error writing dispose handshake: %s", err.Error())
		bs.Reset()
		r.handleError(s, pb.Through_SERVICE_CANT_OPEN_DST_STREAM)
		return
	}

	msg.Reset()

	////等待目标Peer返回 确认收到消息
	rd := newDelimitedReader(bs,4096)
	//bs.SetReadDeadline(time.Now().Add(time.Second*6)) //设置读取超时的时间为6秒
	if err := rd.ReadMsg(msg); err != nil {
		s.Reset()
		log.Debugf("target peer " + dst.ID.String() +" wait reponse timeout", msg.GetType())
		r.handleError(s, pb.Through_SERVICE_CANT_OPEN_DST_STREAM)
		return
	}

	if msg.GetType() != pb.Through_STATUS {
		log.Debugf("unexpected through response; not a status message (%d)", msg.GetType())
		return
	}

	// 计算目标peer的计划打洞时间
	//dstPlanTime := timeDiffToUnix(msg.Time,dstClientInfo.timeDiff)  //目标计划打洞的时间

	// 计算源peer的计划打洞时间(修复偏移量，修复后为相当于本机当前时间)
	dstPlanTimeCorrect := msg.Time - dstClientInfo.timeDiff

	//msg.Time  + timeDiff
	srcPlanTime := dstPlanTimeCorrect + srcTimeDiff

	log.Infof("Src Plan Time:",time.Unix(0,srcPlanTime).Format("2006-01-02 15:04:05.999999999"))
	log.Infof("Dst Plan Time:",time.Unix(0,msg.Time).Format("2006-01-02 15:04:05.999999999"))

	//给源peer发送 【穿透成功】 消息 ,并且带上目标Peer得真实地址
	dst.Addrs = []ma.Multiaddr{dstClientInfo.stream.Conn().RemoteMultiaddr()} //更新目标peer得真实地址

	var sourceMsg pb.Through
	sourceMsg.Type = pb.Through_STATUS.Enum()
	sourceMsg.Code = pb.Through_SUCCESS.Enum()
	sourceMsg.DstPeer = peerInfoToPeer(dst)
	sourceMsg.Time = srcPlanTime
	err = r.writeResponse(s, pb.Through_SUCCESS,&sourceMsg)
	if err != nil {
		log.Debugf("error writing response: %s", err.Error())
		bs.Reset()
		s.Reset()
		return
	}

	// relay connection
	log.Infof("throughing connection between %s and %s", src.ID.Pretty(), dst.ID.Pretty())

	// reset deadline
	bs.SetDeadline(time.Time{})
}

//处理【穿透中】消息
func (self *Through) handleDisposeStream(s network.Stream, msg *pb.Through) {

	log.Infof("Through->handleDisposeStream() ")
	//源peer
	src, err := peerToPeerInfo(msg.GetSrcPeer())
	if err != nil {
		self.handleError(s, pb.Through_DISPOSE_SRC_MULTIADDR_INVALID)
		return
	}
	//目标Peer,这里等于自己
	dst, err := peerToPeerInfo(msg.GetDstPeer())
	if err != nil || dst.ID != self.self {
		self.handleError(s, pb.Through_DISPOSE_DST_MULTIADDR_INVALID)
		return
	}

	if len(src.Addrs) > 0 {
		self.host.Peerstore().AddAddrs(src.ID, src.Addrs, peerstore.TempAddrTTL) //设置地址保存时间为2分钟
		log.Infof("dispose target addr: %s", src.Addrs)
	}

	//var srcQuicAddr ma.Multiaddr
	//AAA:
	//for i:=0;i<len(src.Addrs);i++{
	//	_addr := src.Addrs[0]
	//	protos := _addr.Protocols()
	//	for j:=0;j<len(protos);j++{
	//		if protos[j].Name == "quic"{
	//			srcQuicAddr = _addr
	//			break AAA
	//		}
	//	}
	//}
	//if srcQuicAddr==nil{
	//	self.handleError(s, pb.Through_SERVICE_NO_CONN_TO_DST) //对方没有课穿透的quic地址
	//	return
	//}

	//给穿透服务器回个消息  告知自己收到这个消息了,同时打洞时间约定为6秒后
	planConnTime := time.Now().UnixNano() + (time.Second.Nanoseconds() * 6)
	msg = &pb.Through{}
	msg.Type = pb.Through_STATUS.Enum()
	msg.Code = pb.Through_SUCCESS.Enum()
	//更新打洞时间为当前时间+6秒
	msg.Time = planConnTime
	self.writeResponse(s, pb.Through_SUCCESS,nil)
	log.Infof("Dispose planTime:",time.Unix(0,planConnTime).Format("2006-01-02 15:04:05.999999999"))

	// 在这里需要去做:
	// 1.给源peer建立连接发送请求
	// 2.发送完请求之后再给穿透服务器回应状态
	// 创建和目标peer的流
	go func() {
		jishiqi := time.NewTicker(ClientInteractiveTicker)
		outerFor:
		for{
			select {
			case <-jishiqi.C:
				if time.Now().UnixNano() >= planConnTime{ //如果当前时间等于约定好的时间
					break outerFor
				}
			}
		}
		log.Info("------begin open through stream time:",msg.Time)
		err = self.host.Ping(src,time.Second*6)
		log.Debug("PingPeer---------- error:",err)

		bs, err := self.host.NewStream(network.WithDialPeerTimeout(self.ctx,time.Second*5), src.ID, ProtoID)
		if err != nil{
			self.host.Peerstore().ClearAddrs(src.ID)  //拨号失败时，清理掉对方的地址，这样当自己主动连接他时才会使用穿透服务
			log.Info("------through error:",err.Error())
		}else{
			if bs != nil { //这里说明链上了
				log.Infof("success dispose target addr: %s", src.ID)
				wr := newDelimitedWriter(bs)
				// 设置握手超时
				bs.SetDeadline(time.Now().Add(time.Second*60))

				msg.Type = pb.Through_CAN_SERVICE.Enum()

				//给目标peer发送 是否 ping消息
				wr.WriteMsg(msg)
				//wr.Close()
			}
		}

	}()


	//select {
	//case r.incoming <- &Conn{stream: s, remote: src, host: r.host}:
	//case <-time.After(RelayAcceptTimeout): //10秒之后返回拒绝
	//	r.handleError(s, pb.Through_DISPOSE_THROUGH_REFUSED)
	//}
}

//处理【当前节点是否支持穿透】查询消息
func (r *Through) handleCanService(s network.Stream, msg *pb.Through) {
	var err error

	if GetServiceStatus() {
		err = r.writeResponse(s, pb.Through_SUCCESS,nil)  //支持穿透
	} else {
		err = r.writeResponse(s, pb.Through_SERVICE_CANT_SPEAK_THROUGH,nil) //不支持穿透
	}

	if err != nil {
		s.Reset()
		log.Infof("error writing can service response: %s", err.Error())
	} else {
		s.Close()
	}
}


//处理错误
func (r *Through) handleError(s network.Stream, code pb.Through_Status) {
	err := r.writeResponse(s, code,nil)
	if err != nil {
		s.Reset()
		log.Infof("error writing through response: %s", err.Error())
	} else {
		s.Close()
	}
}

//发送回应消息
func (r *Through) writeResponse(s network.Stream, code pb.Through_Status, msg *pb.Through) error {
	log.Infof("send (%s) message to %s [%s] status:(%s)", pb.Through_STATUS.Enum().String() , s.Conn().RemotePeer() ,s.Conn().RemoteMultiaddr().String() , pb.Through_Status_name[int32(code)])
	wr := newDelimitedWriter(s)

	if msg == nil{
		msg = &pb.Through{}
		msg.Type = pb.Through_STATUS.Enum()
		msg.Code = code.Enum()
		msg.Time = time.Now().UnixNano()
	}

	return wr.WriteMsg(msg)
}


