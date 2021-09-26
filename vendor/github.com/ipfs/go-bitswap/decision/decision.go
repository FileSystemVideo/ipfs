package decision

import intdec "github.com/ipfs/go-bitswap/internal/decision"
import "sync"
// Expose type externally
type Receipt = intdec.Receipt
func GetAccountContribute() sync.Map {
	return intdec.AccountContribute
}