package coreapi

import (
	"context"
	"github.com/ipfs/go-bitswap/decision"
	"github.com/ipfs/go-ipfs/core/wallet"
	coreiface "github.com/ipfs/interface-go-ipfs-core"
	ipath "github.com/ipfs/interface-go-ipfs-core/path"
)

//贡献者接口模块
type ContributionApi CoreAPI

//设置分享带宽收益钱包地址
func (api *ContributionApi) SetWallet(addr string) error {
	wallet.Wallet = addr
	return nil
}

//读取分享带宽收益钱包地址
func (api *ContributionApi) GetWallet() (string,error) {
	return wallet.Wallet,nil
}

//打印贡献值数据
//返回格式:
//[fsvxxxxxxx0]10
//[fsvxxxxxxx1]5
//[fsvxxxxxxx2]8
func (api *ContributionApi) NameList(ctx context.Context, path ipath.Path) (accountCount map[string]int, err error) {
	accountCount = make(map[string]int)
	links, err := api.core().Object().Links(ctx, path)
	if err != nil {
		return nil, err
	}
	cidContributes := decision.GetAccountContribute()
	for _, link := range links {
		if accountInterface, ok := cidContributes.Load(link.Cid.String()); ok {
			account := accountInterface.(string)
			if _, ok := accountCount[account]; !ok {
				accountCount[account] = 1
			} else {
				accountCount[account] += 1
			}
		}
	}
	return
}

func (api *ContributionApi) core() coreiface.CoreAPI {
	return (*CoreAPI)(api)
}
