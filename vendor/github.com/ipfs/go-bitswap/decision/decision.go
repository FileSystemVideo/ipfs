package decision

import intdec "github.com/ipfs/go-bitswap/internal/decision"
import "sync"

// Expose Receipt externally
type Receipt = intdec.Receipt

// Expose ScoreLedger externally
type ScoreLedger = intdec.ScoreLedger

// Expose ScorePeerFunc externally
type ScorePeerFunc = intdec.ScorePeerFunc

func GetAccountContribute() sync.Map {
	return intdec.AccountContribute
}