package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"strings"

	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/types"
	current "github.com/containernetworking/cni/pkg/types/100"
	"github.com/containernetworking/cni/pkg/version"
	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/vishvananda/netlink"
)

type cniConfig struct {
	types.NetConf
}

func createVethPair(containerID, netns string) error {
	// ip link add veth0 type veth peer name veth1
	// ip link set veth0 netns $netns
	// ip netns exec $netns ip link set veth0 up
	// ip netns exec $netns ip addr add 10.100.1.2/24 dev veth0
	// ip netns exec $netns ip link set veth0 up
	// ip netns exec $netns mac address set 00:02:00:00:00:01 dev veth0
	veth0 := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{
			Name: "veth0",
		},
		PeerName: "veth1",
	}
	if err := netlink.LinkAdd(veth0); err != nil {
		fmt.Println("netlink.LinkAdd error for veth0", err)
		return err
	}
	netnsFd, err := ns.GetNS(netns)
	if err != nil {
		fmt.Println("ns.GetNS error for netns", err)
		return err
	}
	if err := netlink.LinkSetNsFd(veth0, int(netnsFd.Fd())); err != nil {
		fmt.Println("netlink.LinkSetNsFd error for veth0", err)
		return err
	}
	ipStr := "10.100.1.2"
	macStr := "00:02:00:00:00:01"
	// Create logical switch port
	portName := fmt.Sprintf("pod-%s", containerID[:12]) // or whatever naming
	// Enter the container's network namespace before configuring the interface.
	// Use github.com/containernetworking/plugins/pkg/ns for ns handling.
	err = ns.WithNetNSPath(netns, func(_ ns.NetNS) error {
		link, err := netlink.LinkByName("veth0")
		if err != nil {
			fmt.Println("netlink.LinkByName error for veth0", err)
			return err
		}
		if err := netlink.LinkSetUp(link); err != nil {
			fmt.Println("netlink.LinkSetUp error for veth0", err)
			return err
		}
		// Define IP address and MAC address variables

		addr, err := netlink.ParseAddr(ipStr + "/24")
		if err != nil {
			fmt.Println("netlink.ParseAddr error for veth0", err)
			return err
		}
		if err := netlink.AddrAdd(link, addr); err != nil {
			fmt.Println("netlink.AddrAdd error for veth0", err)
			return err
		}

		mac, err := net.ParseMAC(macStr)
		if err != nil {
			fmt.Println("net.ParseMAC error for veth0", err)
			return err
		}
		if err := netlink.LinkSetHardwareAddr(link, mac); err != nil {
			fmt.Println("netlink.LinkSetHardwareAddr error for veth0", err)
			return err
		}
		return nil
	})
	if err != nil {
		fmt.Println("ns.WithNetNSPath error for veth0", err)
		return err
	}

	fmt.Println("createVethPair success for veth0")

	// lets deal with veth1, it got created as part of veth peer and now it is in the host namespace
	// ip link set veth1 up
	veth1, err := netlink.LinkByName("veth1")
	if err != nil {
		fmt.Println("netlink.LinkByName error for veth1", err)
		return err
	}
	if err := netlink.LinkSetUp(veth1); err != nil {
		fmt.Println("netlink.LinkSetUp error for veth1", err)
		return err
	}

	cmd := exec.Command("ovs-vsctl", "add-port", "br-int", "veth1")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to add port to br-int: %v", err)
	}

	fmt.Println("exec.Command success for ovs-vsctl add-port br-int veth1")
	cmd = exec.Command("ovs-vsctl", "set", "interface", "veth1",
		fmt.Sprintf("external_ids:iface-id=%s", portName))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to set interface veth1: %v", err)
	}
	// we need to move this side of the veth to the ovs bridge called br-int
	fmt.Println("createVethPair success for veth1")

	// now we need to add the togical topology to ovn

	// Get the ovn-remote from OVS
	cmd = exec.Command("ovs-vsctl", "get", "open_vswitch", ".", "external_ids:ovn-remote")
	output, err := cmd.Output()
	if err != nil {
		return err
	}
	// output will be like: "tcp:172.16.0.2:6642"
	// Parse it and change 6642 -> 6641 for northbound
	ovnRemote := strings.Trim(string(output), "\"\n")
	ovnNB := strings.Replace(ovnRemote, "6642", "6641", 1)

	cmd = exec.Command("ovn-nbctl", fmt.Sprintf("--db=%s", ovnNB),
		"lsp-add", "ls1", portName)
	if err := cmd.Run(); err != nil {
		return err
	}

	// Set addresses
	cmd = exec.Command("ovn-nbctl", fmt.Sprintf("--db=%s", ovnNB),
		"lsp-set-addresses", portName, fmt.Sprintf("%s %s", macStr, ipStr))
	if err := cmd.Run(); err != nil {
		return err
	}

	return nil
}

func cmdAdd(args *skel.CmdArgs) error {
	var conf cniConfig
	if err := json.Unmarshal(args.StdinData, &conf); err != nil {
		fmt.Println("cmdAdd unmarshal error", err)
		return err
	}
	fmt.Println("received cmdAdd cni config", conf)
	// create veth pair and move one side to container namespace

	if err := createVethPair(args.ContainerID, args.Netns); err != nil {
		fmt.Println("createVethPair error", err)
		return err
	}

	// Build the result
	result := &current.Result{
		CNIVersion: conf.CNIVersion,
		IPs: []*current.IPConfig{
			{
				Address: net.IPNet{
					IP:   net.ParseIP("10.100.1.2"),
					Mask: net.CIDRMask(24, 32),
				},
				Gateway: net.ParseIP("10.100.1.1"), // optional for now
			},
		},
	}

	return types.PrintResult(result, conf.CNIVersion)
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
