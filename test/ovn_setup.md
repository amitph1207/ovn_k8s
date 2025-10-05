# OVN Setup Guide

## Control Plane

### Install and Configure OVN Central

```bash
sudo apt install -y ovn-central 
sudo systemctl start ovn-central 
sudo systemctl enable ovn-central
sudo ovn-nbctl set-connection ptcp:6641:0.0.0.0 
sudo ovn-sbctl set-connection ptcp:6642:0.0.0.0
sudo ovn-nbctl ls-add ls1
```

## Worker Node

### Install OVS and OVN

```bash
sudo apt install -y openvswitch-switch openvswitch-common ovn-host ovn-common
```

### Start and Enable OVS

```bash
sudo systemctl start openvswitch-switch
sudo systemctl enable openvswitch-switch
```

### Configure OVS to Connect to OVN

```bash
sudo ovs-vsctl set open_vswitch . external_ids:ovn-remote="tcp:172.16.0.2:6642"
sudo ovs-vsctl set open_vswitch . external_ids:ovn-encap-type=geneve
sudo ovs-vsctl set open_vswitch . external_ids:ovn-encap-ip=172.16.0.3
```

### Start and Enable OVN Host

```bash
sudo systemctl start ovn-host
sudo systemctl enable ovn-host
```

### Build CNI Plugin

```bash
sudo su
apt-get update && apt install golang-go
git clone https://github.com/amitph1207/ovn_k8s.git
cd ovn_k8s
go build -o ovn main.go
```



