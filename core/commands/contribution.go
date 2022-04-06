package commands

import (
	"encoding/json"
	"fmt"
	cmds "github.com/ipfs/go-ipfs-cmds"
	"github.com/ipfs/go-ipfs/core/commands/cmdenv"
	corePath "github.com/ipfs/interface-go-ipfs-core/path"
	"io"
)

type walletAddress struct {
	Address string
}

var ContributionCmd = &cmds.Command{
	Helptext: cmds.HelpText{
		Tagline: "ipfs contribution module",
		ShortDescription: `
'ipfs contribution module.
`,
	},
	Subcommands: map[string]*cmds.Command{
		"namelist": contributionNameListCmd,
		"setwallet" : contributionSetWalletCmd,
		"getwallet" : contributionGetWalletCmd,
	},
}

var contributionGetWalletCmd = &cmds.Command{
	Helptext: cmds.HelpText{
		Tagline:          "get contribution wallet address",
		ShortDescription: `get contribution wallet address`,
	},
	Options: []cmds.Option{},
	Run: func(req *cmds.Request, res cmds.ResponseEmitter, env cmds.Environment) error {
		api, err := cmdenv.GetApi(env, req)
		if err != nil {
			return err
		}
		wallet,err := api.Contribution().GetWallet()
		if err !=nil{
			return err
		}

		return cmds.EmitOnce(res, &walletAddress{Address:wallet})
	},
	Encoders: cmds.EncoderMap{
		cmds.Text: cmds.MakeTypedEncoder(func(req *cmds.Request, w io.Writer, out *walletAddress) error {
			fmt.Println(out.Address)
			return nil
		}),
	},
	Type: walletAddress{},
}
var contributionSetWalletCmd = &cmds.Command{
	Helptext: cmds.HelpText{
		Tagline:          "set contributes account",
		ShortDescription: `set contributes account`,
	},
	Arguments: []cmds.Argument{
		cmds.StringArg("address", true, false, "wallet address.").EnableStdin(),
	},
	Options: []cmds.Option{},
	Run: func(req *cmds.Request, res cmds.ResponseEmitter, env cmds.Environment) error {
		wallet := req.Arguments[0]
		api, err := cmdenv.GetApi(env, req)
		if err != nil {
			return err
		}
		api.Contribution().SetWallet(wallet)

		return cmds.EmitOnce(res, &walletAddress{Address:wallet})
	},
	Encoders: cmds.EncoderMap{
		cmds.Text: cmds.MakeTypedEncoder(func(req *cmds.Request, w io.Writer, out *walletAddress) error {
			fmt.Println("set wallet address:",out.Address)
			return nil
		}),
	},
	Type: walletAddress{},
}
var contributionNameListCmd = &cmds.Command{
	Helptext: cmds.HelpText{
		Tagline:          "Print host contributes map",
		ShortDescription: `Print host contributes map`,
	},
	Arguments: []cmds.Argument{
		cmds.StringArg("ipfs-path", true, false, "The path to the IPFS object(s) to be outputted.").EnableStdin(),
	},
	Options: []cmds.Option{},
	Run: func(req *cmds.Request, res cmds.ResponseEmitter, env cmds.Environment) error {
		p := corePath.New(req.Arguments[0])
		api, err := cmdenv.GetApi(env, req)
		if err != nil {
			return err
		}
		accountCount, err := api.Contribution().NameList(req.Context, p)
		if err != nil {
			return err
		}

		return cmds.EmitOnce(res, accountCount)
	},
	Encoders: cmds.EncoderMap{
		cmds.Text: cmds.MakeTypedEncoder(func(req *cmds.Request, w io.Writer, cmap map[string]int) error {
			bb, err := json.Marshal(cmap)
			if err != nil {
				return err
			}
			fmt.Println(string(bb))
			return nil
		}),
	},
	Type: make(map[string]int),
}