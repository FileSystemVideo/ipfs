package cid

import (
	"github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
	"testing"
)

func TestCid(t *testing.T) {
	cid, err := nsToCid("/libp2p/relay")
	if err != nil {
		t.Fatal(err)
		return
	}
	t.Log(cid.String())
}

func nsToCid(ns string) (cid.Cid, error) {
	h, err := mh.Sum([]byte(ns), mh.SHA2_256, -1)
	if err != nil {
		return cid.Undef, err
	}

	return cid.NewCidV1(cid.Raw, h), nil
}
