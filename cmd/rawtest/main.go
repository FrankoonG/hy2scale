package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"strconv"
	"syscall"
	"time"
)

func main() {
	ifName := "ipsec0"
	dstIP := "192.168.26.2"
	if len(os.Args) > 1 {
		ifName = os.Args[1]
	}
	if len(os.Args) > 2 {
		dstIP = os.Args[2]
	}

	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_RAW, syscall.IPPROTO_RAW)
	if err != nil {
		fmt.Printf("socket error: %v\n", err)
		return
	}
	defer syscall.Close(fd)
	syscall.SetsockoptInt(fd, syscall.IPPROTO_IP, syscall.IP_HDRINCL, 1)
	syscall.SetsockoptString(fd, syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, ifName)

	// Test 1: zero-filled packets (like before)
	fmt.Println("=== Zero-filled packets ===")
	for _, size := range []int{500, 1000, 1300} {
		testSend(fd, ifName, dstIP, size, false)
	}

	// Test 2: packets with realistic TCP content (SYN-ACK like)
	fmt.Println("\n=== TCP-like packets (proto=6, real headers) ===")
	for _, size := range []int{500, 1000, 1300} {
		testSend(fd, ifName, dstIP, size, true)
	}
}

func testSend(fd int, ifName, dstIP string, totalLen int, realTCP bool) {
	txBefore := readTXPackets(ifName)
	pkt := make([]byte, totalLen)

	// IP header
	pkt[0] = 0x45
	binary.BigEndian.PutUint16(pkt[2:4], uint16(totalLen))
	pkt[6] = 0x40 // DF
	pkt[8] = 64   // TTL
	pkt[9] = 6    // TCP
	dst := net.ParseIP(dstIP).To4()
	copy(pkt[12:16], []byte{52, 6, 211, 202}) // src = httpbin IP
	copy(pkt[16:20], dst)

	if realTCP {
		// TCP header at offset 20
		binary.BigEndian.PutUint16(pkt[20:22], 80)    // src port
		binary.BigEndian.PutUint16(pkt[22:24], 38856)  // dst port
		binary.BigEndian.PutUint32(pkt[24:28], 1000)   // seq
		binary.BigEndian.PutUint32(pkt[28:32], 2000)   // ack
		pkt[32] = 0x50 // data offset = 5 (20 bytes)
		pkt[33] = 0x18 // ACK+PSH flags
		binary.BigEndian.PutUint16(pkt[34:36], 65535)  // window
		// Fill payload with 'A'
		for i := 40; i < totalLen; i++ {
			pkt[i] = 'A'
		}
		// TCP checksum (pseudo header + TCP)
		tcpLen := totalLen - 20
		var csum uint32
		// pseudo header
		csum += uint32(pkt[12])<<8 + uint32(pkt[13])
		csum += uint32(pkt[14])<<8 + uint32(pkt[15])
		csum += uint32(pkt[16])<<8 + uint32(pkt[17])
		csum += uint32(pkt[18])<<8 + uint32(pkt[19])
		csum += 6 // proto TCP
		csum += uint32(tcpLen)
		// TCP data
		for i := 20; i < totalLen-1; i += 2 {
			csum += uint32(pkt[i])<<8 + uint32(pkt[i+1])
		}
		if totalLen%2 == 1 {
			csum += uint32(pkt[totalLen-1]) << 8
		}
		for csum > 0xffff {
			csum = (csum >> 16) + (csum & 0xffff)
		}
		binary.BigEndian.PutUint16(pkt[36:38], ^uint16(csum))
	}

	// IP checksum
	pkt[10] = 0
	pkt[11] = 0
	var sum uint32
	for i := 0; i < 20; i += 2 {
		sum += uint32(binary.BigEndian.Uint16(pkt[i : i+2]))
	}
	for sum > 0xffff {
		sum = (sum >> 16) + (sum & 0xffff)
	}
	binary.BigEndian.PutUint16(pkt[10:12], ^uint16(sum))

	var sa4 syscall.SockaddrInet4
	copy(sa4.Addr[:], dst)
	err := syscall.Sendto(fd, pkt, 0, &sa4)
	time.Sleep(50 * time.Millisecond)
	txAfter := readTXPackets(ifName)
	fmt.Printf("  size=%4d  err=%-20v TX_delta=%d\n", totalLen, err, txAfter-txBefore)
}

func readTXPackets(ifName string) int {
	data, _ := os.ReadFile("/proc/net/dev")
	for _, line := range splitLines(string(data)) {
		if contains(line, ifName+":") {
			f := fields(line)
			if len(f) >= 11 {
				n, _ := strconv.Atoi(f[10])
				return n
			}
		}
	}
	return 0
}
func splitLines(s string) []string {
	var r []string; start := 0
	for i := range s { if s[i] == '\n' { r = append(r, s[start:i]); start = i+1 } }
	if start < len(s) { r = append(r, s[start:]) }; return r
}
func contains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ { if s[i:i+len(sub)] == sub { return true } }; return false
}
func fields(s string) []string {
	var r []string; in := false; start := 0
	for i := range s {
		if s[i]==' '||s[i]=='\t'||s[i]==':' { if in { r = append(r, s[start:i]); in = false } } else { if !in { start = i; in = true } }
	}
	if in { r = append(r, s[start:]) }; return r
}
