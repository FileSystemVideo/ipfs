package through

import (
	"encoding/binary"
	"errors"
	"io"
	"math"

	pb "github.com/libp2p/go-libp2p-through/pb"

	"github.com/libp2p/go-libp2p-core/peer"

	ggio "github.com/gogo/protobuf/io"
	proto "github.com/gogo/protobuf/proto"
	pool "github.com/libp2p/go-buffer-pool"
	ma "github.com/multiformats/go-multiaddr"
)

func peerToPeerInfo(p *pb.Through_Peer) (peer.AddrInfo, error) {
	if p == nil {
		return peer.AddrInfo{}, errors.New("nil peer")
	}

	id, err := peer.IDFromBytes(p.Id)
	if err != nil {
		return peer.AddrInfo{}, err
	}

	addrs := make([]ma.Multiaddr, 0, len(p.Addrs))
	for _, addrBytes := range p.Addrs {
		a, err := ma.NewMultiaddrBytes(addrBytes)
		if err == nil {
			addrs = append(addrs, a)
		}
	}

	return peer.AddrInfo{ID: id, Addrs: addrs}, nil
}

func peerInfoToPeer(pi peer.AddrInfo) *pb.Through_Peer {
	addrs := make([][]byte, len(pi.Addrs))
	for i, addr := range pi.Addrs {
		addrs[i] = addr.Bytes()
	}

	p := new(pb.Through_Peer)
	p.Id = []byte(pi.ID)
	p.Addrs = addrs

	return p
}

func incrementTag(v int) int {
	return v + 1
}

func decrementTag(v int) int {
	if v > 0 {
		return v - 1
	} else {
		return v
	}
}

type delimitedReader struct {
	r   io.Reader
	buf []byte
}

// The gogo protobuf NewDelimitedReader is buffered, which may eat up stream data.
// So we need to implement a compatible delimited reader that reads unbuffered.
// There is a slowdown from unbuffered reading: when reading the message
// it can take multiple single byte Reads to read the length and another Read
// to read the message payload.
// However, this is not critical performance degradation as
// - the reader is utilized to read one (dialer, stop) or two messages (hop) during
//   the handshake, so it's a drop in the water for the connection lifetime.
// - messages are small (max 4k) and the length fits in a couple of bytes,
//   so overall we have at most three reads per message.
func newDelimitedReader(r io.Reader, maxSize int) *delimitedReader {
	return &delimitedReader{r: r, buf: pool.Get(maxSize)}
}

func (d *delimitedReader) Close() {
	if d.buf != nil {
		pool.Put(d.buf)
		d.buf = nil
	}
}

func (d *delimitedReader) ReadByte() (byte, error) {
	buf := d.buf[:1]
	_, err := d.r.Read(buf)
	return buf[0], err
}

func (d *delimitedReader) ReadMsg(msg proto.Message) error {
	mlen, err := binary.ReadUvarint(d)
	if err != nil {
		return err
	}

	if uint64(len(d.buf)) < mlen {
		return errors.New("Message too large")
	}

	buf := d.buf[:mlen]
	_, err = io.ReadFull(d.r, buf)
	if err != nil {
		return err
	}

	return proto.Unmarshal(buf, msg)
}

func newDelimitedWriter(w io.Writer) ggio.WriteCloser {
	return ggio.NewDelimitedWriter(w)
}

//比较2个时间，返回比较大的那个
func maxTimeDiff(d1,d2 int64) int64{
	if math.Abs(float64(d1)) > math.Abs(float64(d2)){
		return d1
	}else{
		return d2
	}
}

//根据时间差，返回一个约定的平衡时间
func timeDiffToUnix(now int64,timeDiff int64) int64{
	return now + timeDiff + ClientInteractiveDelay.Nanoseconds() //+3 为增加3秒的缓冲期
}
