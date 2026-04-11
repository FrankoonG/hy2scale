package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

// Test: fetch a URL through a relay using dialExit (same as SOCKS5 path)
// vs through gvisor TCP forwarder (IKEv2 compat path)
// This tests the relay connection independently from gvisor.

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: relaytest <url>")
		fmt.Println("Fetches URL directly (like SOCKS5 would), bypassing gvisor")
		return
	}
	url := os.Args[1]

	// Direct fetch (same as what SOCKS5 proxy does — Go standard TCP)
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				// Direct dial — same as SOCKS5 client-side dial
				return net.DialTimeout(network, addr, 5*time.Second)
			},
		},
	}

	start := time.Now()
	resp, err := client.Get(url)
	if err != nil {
		fmt.Printf("ERROR: %v\n", err)
		return
	}
	defer resp.Body.Close()
	n, _ := io.Copy(io.Discard, resp.Body)
	fmt.Printf("Direct: %d %d bytes %.3fs\n", resp.StatusCode, n, time.Since(start).Seconds())
}
