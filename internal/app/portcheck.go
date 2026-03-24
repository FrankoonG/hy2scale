package app

import (
	"fmt"
	"net"
)

// PortConflict describes a port that is already in use.
type PortConflict struct {
	Port  int    `json:"port"`
	Proto string `json:"proto"` // "tcp" or "udp"
	Desc  string `json:"desc"`  // what's using it
}

// CheckPorts tests if the given ports are available.
// Returns a list of conflicts (empty if all available).
func CheckPorts(ports []PortConflict) []PortConflict {
	var conflicts []PortConflict
	for _, p := range ports {
		if !isPortAvailable(p.Proto, p.Port) {
			conflicts = append(conflicts, p)
		}
	}
	return conflicts
}

func isPortAvailable(proto string, port int) bool {
	addr := fmt.Sprintf(":%d", port)
	switch proto {
	case "tcp":
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return false
		}
		ln.Close()
		return true
	case "udp":
		conn, err := net.ListenPacket("udp", addr)
		if err != nil {
			return false
		}
		conn.Close()
		return true
	}
	return false
}
