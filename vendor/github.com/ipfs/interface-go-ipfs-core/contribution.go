package iface

import (
	"context"
	ipath "github.com/ipfs/interface-go-ipfs-core/path"
)
//Share bandwidth and hard disk related APIs
type ContributionApi interface{

	//Gets the contributor information of the specified IPFs file
	NameList(context.Context,ipath.Path) (map[string]int, error)

	//Set the revenue wallet address for sharing bandwidth
	SetWallet(string) error

	//Read the revenue wallet address of sharing bandwidth
	GetWallet() (string,error)
}