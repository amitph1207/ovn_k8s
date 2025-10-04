package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
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

var logger *log.Logger

func initLogger() {
	logFile, err := os.OpenFile("/tmp/ovn-cni.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Fatal("Failed to open log file:", err)
	}
	logger = log.New(logFile, "", log.LstdFlags)
}

func createVethPair(containerID, netns string) error {
	// Generate unique veth peer name based on containerID
	vethPeerName := fmt.Sprintf("veth-%s", containerID[:12])
	logger.Printf("Creating veth pair: veth0 <-> %s for container %s", vethPeerName, containerID)

	// Check if veth peer already exists in host namespace and delete it
	if existingLink, err := netlink.LinkByName(vethPeerName); err == nil {
		logger.Printf("Found existing interface %s, deleting it first", vethPeerName)
		if err := netlink.LinkDel(existingLink); err != nil {
			logger.Printf("Failed to delete existing %s: %v", vethPeerName, err)
		}
	}

	veth0 := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{
			Name: "veth0",
		},
		PeerName: vethPeerName,
	}
	if err := netlink.LinkAdd(veth0); err != nil {
		logger.Printf("netlink.LinkAdd error for veth0: %v", err)
		return err
	}

	// lets deal with the peer veth, it got created as part of veth peer and now it is in the host namespace
	vethPeer, err := netlink.LinkByName(vethPeerName)
	if err != nil {
		logger.Printf("netlink.LinkByName error for %s: %v", vethPeerName, err)
		return err
	}
	if err := netlink.LinkSetUp(vethPeer); err != nil {
		logger.Printf("netlink.LinkSetUp error for %s: %v", vethPeerName, err)
		return err
	}

	// Check and clean up any existing veth0 in the target namespace
	netnsFd, err := ns.GetNS(netns)
	if err != nil {
		logger.Printf("ns.GetNS error for netns: %v", err)
		return err
	}

	// Clean up old veth0 if it exists in the target namespace
	err = ns.WithNetNSPath(netns, func(_ ns.NetNS) error {
		if oldLink, err := netlink.LinkByName("veth0"); err == nil {
			logger.Printf("Found existing veth0 in target namespace, deleting it")
			if err := netlink.LinkDel(oldLink); err != nil {
				logger.Printf("Failed to delete existing veth0: %v", err)
				return err
			}
		}
		return nil
	})
	if err != nil {
		logger.Printf("Error during veth0 cleanup: %v", err)
		return err
	}

	if err := netlink.LinkSetNsFd(veth0, int(netnsFd.Fd())); err != nil {
		logger.Printf("netlink.LinkSetNsFd error for veth0: %v", err)
		return err
	}
	ipStr := "10.100.1.2"
	macStr := "00:02:00:00:00:01"
	// Create logical switch port
	portName := fmt.Sprintf("pod-%s", containerID[:12])
	logger.Printf("Configuring veth0 in container namespace with IP: %s, MAC: %s", ipStr, macStr)

	// Enter the container's network namespace before configuring the interface.
	err = ns.WithNetNSPath(netns, func(_ ns.NetNS) error {
		link, err := netlink.LinkByName("veth0")
		if err != nil {
			logger.Printf("netlink.LinkByName error for veth0: %v", err)
			return err
		}
		if err := netlink.LinkSetUp(link); err != nil {
			logger.Printf("netlink.LinkSetUp error for veth0: %v", err)
			return err
		}

		addr, err := netlink.ParseAddr(ipStr + "/24")
		if err != nil {
			logger.Printf("netlink.ParseAddr error for veth0: %v", err)
			return err
		}
		if err := netlink.AddrAdd(link, addr); err != nil {
			logger.Printf("netlink.AddrAdd error for veth0: %v", err)
			return err
		}

		mac, err := net.ParseMAC(macStr)
		if err != nil {
			logger.Printf("net.ParseMAC error for veth0: %v", err)
			return err
		}
		if err := netlink.LinkSetHardwareAddr(link, mac); err != nil {
			logger.Printf("netlink.LinkSetHardwareAddr error for veth0: %v", err)
			return err
		}
		return nil
	})
	if err != nil {
		logger.Printf("ns.WithNetNSPath error for veth0: %v", err)
		return err
	}

	logger.Printf("Successfully configured veth0 in container namespace")

	// Add the peer veth to OVS bridge
	logger.Printf("Adding %s to br-int OVS bridge", vethPeerName)
	cmd := exec.Command("ovs-vsctl", "add-port", "br-int", vethPeerName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Printf("Failed to add port to br-int: %v, output: %s", err, string(output))
		return fmt.Errorf("failed to add port to br-int: %v, output: %s", err, string(output))
	}

	logger.Printf("Successfully added %s to br-int", vethPeerName)
	cmd = exec.Command("ovs-vsctl", "set", "interface", vethPeerName,
		fmt.Sprintf("external_ids:iface-id=%s", portName))
	output, err = cmd.CombinedOutput()
	if err != nil {
		logger.Printf("Failed to set interface %s: %v, output: %s", vethPeerName, err, string(output))
		return fmt.Errorf("failed to set interface %s: %v, output: %s", vethPeerName, err, string(output))
	}
	logger.Printf("Successfully set external_ids:iface-id=%s for %s", portName, vethPeerName)

	// Now add the logical topology to OVN
	logger.Printf("Configuring OVN logical switch port")

	// Get the ovn-remote from OVS
	cmd = exec.Command("ovs-vsctl", "get", "open_vswitch", ".", "external_ids:ovn-remote")
	output, err = cmd.CombinedOutput()
	if err != nil {
		logger.Printf("Failed to get ovn-remote: %v, output: %s", err, string(output))
		return fmt.Errorf("failed to get ovn-remote: %v, output: %s", err, string(output))
	}
	// output will be like: "tcp:172.16.0.2:6642"
	// Parse it and change 6642 -> 6641 for northbound
	ovnRemote := strings.Trim(string(output), "\"\n")
	ovnNB := strings.Replace(ovnRemote, "6642", "6641", 1)
	logger.Printf("Using OVN NB database: %s", ovnNB)

	cmd = exec.Command("ovn-nbctl", fmt.Sprintf("--db=%s", ovnNB),
		"lsp-add", "ls1", portName)
	output, err = cmd.CombinedOutput()
	if err != nil {
		logger.Printf("Failed to add logical switch port: %v, output: %s", err, string(output))
		return fmt.Errorf("failed to add logical switch port: %v, output: %s", err, string(output))
	}
	logger.Printf("Successfully added logical switch port: %s", portName)

	// Set addresses
	cmd = exec.Command("ovn-nbctl", fmt.Sprintf("--db=%s", ovnNB),
		"lsp-set-addresses", portName, fmt.Sprintf("%s %s", macStr, ipStr))
	output, err = cmd.CombinedOutput()
	if err != nil {
		logger.Printf("Failed to set addresses: %v, output: %s", err, string(output))
		return fmt.Errorf("failed to set addresses: %v, output: %s", err, string(output))
	}
	logger.Printf("Successfully set addresses %s %s for port %s", macStr, ipStr, portName)

	return nil
}

func cmdAdd(args *skel.CmdArgs) error {
	var conf cniConfig
	if err := json.Unmarshal(args.StdinData, &conf); err != nil {
		logger.Printf("cmdAdd unmarshal error: %v", err)
		return err
	}
	logger.Printf("Received cmdAdd for container %s, netns: %s", args.ContainerID, args.Netns)

	if err := createVethPair(args.ContainerID, args.Netns); err != nil {
		logger.Printf("createVethPair error: %v", err)
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
				Gateway: net.ParseIP("10.100.1.1"),
			},
		},
	}

	logger.Printf("cmdAdd completed successfully for container %s", args.ContainerID)
	return types.PrintResult(result, conf.CNIVersion)
}

func cmdDel(args *skel.CmdArgs) error {
	var conf cniConfig
	if err := json.Unmarshal(args.StdinData, &conf); err != nil {
		logger.Printf("cmdDel unmarshal error: %v", err)
		return err
	}
	logger.Printf("Received cmdDel for container %s", args.ContainerID)

	// Clean up veth peer in host namespace
	vethPeerName := fmt.Sprintf("veth-%s", args.ContainerID[:12])
	portName := fmt.Sprintf("pod-%s", args.ContainerID[:12])

	// Remove from OVS bridge
	logger.Printf("Removing %s from br-int", vethPeerName)
	cmd := exec.Command("ovs-vsctl", "del-port", "br-int", vethPeerName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Printf("Failed to remove port from br-int (may not exist): %v, output: %s", err, string(output))
		// Don't return error, continue cleanup
	}

	// Delete the veth peer interface (this will also delete veth0 in the container)
	if link, err := netlink.LinkByName(vethPeerName); err == nil {
		logger.Printf("Deleting interface %s", vethPeerName)
		if err := netlink.LinkDel(link); err != nil {
			logger.Printf("Failed to delete %s: %v", vethPeerName, err)
		}
	} else {
		logger.Printf("Interface %s not found (may already be deleted)", vethPeerName)
	}

	// Remove OVN logical switch port
	cmd = exec.Command("ovs-vsctl", "get", "open_vswitch", ".", "external_ids:ovn-remote")
	output, err = cmd.CombinedOutput()
	if err != nil {
		logger.Printf("Failed to get ovn-remote during cleanup: %v", err)
		return nil // Don't fail if we can't clean up OVN
	}

	ovnRemote := strings.Trim(string(output), "\"\n")
	ovnNB := strings.Replace(ovnRemote, "6642", "6641", 1)

	logger.Printf("Removing logical switch port %s", portName)
	cmd = exec.Command("ovn-nbctl", fmt.Sprintf("--db=%s", ovnNB),
		"--if-exists", "lsp-del", portName)
	output, err = cmd.CombinedOutput()
	if err != nil {
		logger.Printf("Failed to delete logical switch port: %v, output: %s", err, string(output))
		// Don't return error
	}

	logger.Printf("cmdDel completed for container %s", args.ContainerID)
	return nil
}

func main() {
	initLogger()
	logger.Printf("Starting OVN CNI plugin")

	skel.PluginMainFuncs(skel.CNIFuncs{
		Add: cmdAdd,
		Del: cmdDel,
	}, version.All, "toy-ovn-cni")
}
