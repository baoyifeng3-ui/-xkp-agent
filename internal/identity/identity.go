package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
)

type Info struct {
	MachineDigest string `json:"machineDigest"`
	Hostname      string `json:"hostname"`
	PrimaryIP     string `json:"primaryIp"`
	MACAddress    string `json:"macAddress"`
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
