package main

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"

	"github.com/grandcat/zeroconf"
)

type lanAddress struct {
	IP        string
	Interface net.Interface
}

func eligibleLAN(name string, ip netip.Addr) bool {
	if !ip.Is4() || !ip.IsPrivate() || ip.IsLoopback() {
		return false
	}
	for _, prefix := range []string{"lo", "docker", "br-", "veth", "virbr", "tailscale", "tun", "tap", "wg", "zt"} {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}
	return true
}

func localAddresses() ([]lanAddress, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var result []lanAddress
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			prefix, err := netip.ParsePrefix(address.String())
			if err != nil {
				continue
			}
			if eligibleLAN(iface.Name, prefix.Addr()) {
				result = append(result, lanAddress{prefix.Addr().String(), iface})
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Interface.Name+result[i].IP < result[j].Interface.Name+result[j].IP
	})
	return result, nil
}

func selectLAN(requested string) (lanAddress, error) {
	addresses, err := localAddresses()
	if err != nil {
		return lanAddress{}, err
	}
	for _, address := range addresses {
		if requested == address.IP {
			return address, nil
		}
	}
	if requested != "" {
		return lanAddress{}, errors.New("--host must be a private IPv4 address on an active local network interface")
	}
	if len(addresses) == 0 {
		return lanAddress{}, errors.New("no local network found; connect to the same Wi-Fi or LAN as your device")
	}
	if len(addresses) > 1 {
		return lanAddress{}, errors.New("multiple local networks found; use --host with the address on your device’s network (see clipsend doctor)")
	}
	return addresses[0], nil
}

func advertise(lan lanAddress, port int, computerID, sessionID string) (*zeroconf.Server, error) {
	// Discovery identifies a candidate only. The phone must verify its stored TLS pin.
	instance := "ClipSend-" + sessionID
	host := "clipsend-" + computerID
	s, err := zeroconf.RegisterProxy(instance, "_clipsend._tcp", "local.", port, host, []string{lan.IP}, []string{"v=1", "computerID=" + computerID}, []net.Interface{lan.Interface})
	if err != nil {
		return nil, fmt.Errorf("cannot announce ClipSend on the local network: %w", err)
	}
	return s, nil
}

func advertiseReceive(lan lanAddress, port int, computerID string) (*zeroconf.Server, error) {
	instance := "ClipSend-Receive-" + computerID
	host := "clipsend-" + computerID
	return zeroconf.RegisterProxy(instance, "_clipsend._tcp", "local.", port, host, []string{lan.IP}, []string{"v=1", "computerID=" + computerID, "receive=1"}, []net.Interface{lan.Interface})
}
