package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

type Info struct {
	MachineDigest string `json:"machineDigest"`
	Hostname      string `json:"hostname"`
	PrimaryIP     string `json:"primaryIp"`
	MACAddress    string `json:"macAddress"`
}

func Discover(machineIDPath, managementURL string) (Info, error) {
	machineID, err := os.ReadFile(machineIDPath)
	if err != nil {
		return Info{}, fmt.Errorf("read machine identity: %w", err)
	}
	hostname, err := os.Hostname()
	if err != nil {
		return Info{}, fmt.Errorf("read hostname: %w", err)
	}
	u, err := url.Parse(managementURL)
	if err != nil || u.Hostname() == "" {
		return Info{}, fmt.Errorf("invalid management URL")
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	connection, err := net.Dial("udp", net.JoinHostPort(u.Hostname(), port))
	if err != nil {
		return Info{}, fmt.Errorf("resolve primary route: %w", err)
	}
	primaryIP := connection.LocalAddr().(*net.UDPAddr).IP
	_ = connection.Close()
	interfaces, err := net.Interfaces()
	if err != nil {
		return Info{}, fmt.Errorf("list network interfaces: %w", err)
	}
	for _, networkInterface := range interfaces {
		addresses, addressErr := networkInterface.Addrs()
		if addressErr != nil {
			continue
		}
		for _, address := range addresses {
			ip, _, parseErr := net.ParseCIDR(address.String())
			if parseErr == nil && ip.Equal(primaryIP) {
				return Build(string(machineID), hostname, primaryIP.String(), networkInterface.HardwareAddr.String())
			}
		}
	}
	return Info{}, fmt.Errorf("primary route interface was not found")
}

func Build(machineID, hostname, primaryIP, macAddress string) (Info, error) {
	machineID = strings.TrimSpace(machineID)
	if machineID == "" || strings.TrimSpace(hostname) == "" {
		return Info{}, fmt.Errorf("machine identity is incomplete")
	}
	ip := net.ParseIP(primaryIP)
	if ip == nil || ip.To4() == nil {
		return Info{}, fmt.Errorf("primary IP must be IPv4")
	}
	mac, err := net.ParseMAC(macAddress)
	if err != nil || len(mac) != 6 {
		return Info{}, fmt.Errorf("invalid MAC address")
	}
	digest := sha256.Sum256([]byte(machineID))
	return Info{MachineDigest: hex.EncodeToString(digest[:]), Hostname: strings.TrimSpace(hostname), PrimaryIP: ip.String(), MACAddress: strings.ToLower(mac.String())}, nil
}
