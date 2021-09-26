package through

import (
	"net"

	pb "github.com/libp2p/go-libp2p-through/pb"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr-net"
)

var _ manet.Listener = (*ThroughListener)(nil)

type ThroughListener Through

func (l *ThroughListener) Through() *Through {
	return (*Through)(l)
}

func (r *Through) Listener() *ThroughListener {
	// TODO: Only allow one!
	return (*ThroughListener)(r)
}


func (l *ThroughListener) Accept() (manet.Conn, error) {
	select {
	case c := <-l.incoming:
		//针对新接入的连接自动回应成功消息
		err := l.Through().writeResponse(c.stream, pb.Through_SUCCESS,nil)
		if err != nil {
			log.Debugf("error writing through response: %s", err.Error())
			c.stream.Reset()
			return nil, err
		}


		// TODO: Pretty print.
		log.Infof("accepted through connection: %q", c)

		return c, nil
	case <-l.ctx.Done():
		return nil, l.ctx.Err()
	}
}

func (l *ThroughListener) Addr() net.Addr {
	return &NetAddr{
		Relay:  "any",
		Remote: "any",
	}
}

func (l *ThroughListener) Multiaddr() ma.Multiaddr {
	return throughAddr
}

func (l *ThroughListener) Close() error {
	// TODO: noop?
	return nil
}
