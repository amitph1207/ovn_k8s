package main

import (
	"encoding/json"
	"fmt"

	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/types"
	"github.com/containernetworking/cni/pkg/version"
)

type cniConfig struct {
	types.NetConf
}

func cmdAdd(args *skel.CmdArgs) error {
	var conf cniConfig
	if err := json.Unmarshal(args.StdinData, &conf); err != nil {
		fmt.Println("cmdAdd unmarshal error", err)
		return err
	}
	fmt.Println("received cmdAdd cni config", conf)
	return nil
}

func cmdDel(args *skel.CmdArgs) error {
	var conf cniConfig
	if err := json.Unmarshal(args.StdinData, &conf); err != nil {
		fmt.Println("cmdDel unmarshal error", err)
		return err
	}
	fmt.Println("received cmdDel cni config", conf)
	return nil
}

func main() {
	skel.PluginMainFuncs(skel.CNIFuncs{
		Add: cmdAdd,
		Del: cmdDel,
	}, version.All, "toy-ovn-cni")
}
