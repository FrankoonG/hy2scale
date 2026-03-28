// Package relay provides decentralized peer-to-peer traffic forwarding
// through Hysteria 2 tunnels.
//
// Every node is equal — each can run both an hy2 server (accepting peers)
// and multiple hy2 clients (connecting to peers). Peers can route traffic
// through each other's network.
//
// Peer discovery is local by default (only directly connected peers).
// Nested discovery (seeing a peer's peers) is opt-in per peer.
package relay

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	hyclient "github.com/apernet/hysteria/core/v2/client"
)

// ErrNotHy2scale indicates the remote is a plain hy2 server without relay protocol.
var ErrNotHy2scale = fmt.Errorf("relay: remote is not hy2scale")

const (
	streamRegister      = "_relay_register_:0"
	streamCtrlS2C       = "_relay_s2c_ctrl_:0"
	streamListPeers     = "_relay_list_peers_:0"
	streamLatencyReport = "_relay_latency_:0"
	streamViaPrefix     = "_relay_via_"
	streamDataPrefix    = "_relay_data_"
	streamDataSuffix    = ":0"
)

// IsRelayStream returns true if addr is a relay internal stream.
func IsRelayStream(addr string) bool {
	return addr == streamRegister ||
		addr == streamCtrlS2C ||
		addr == streamListPeers ||
		addr == streamLatencyReport ||
		strings.HasPrefix(addr, streamViaPrefix) ||
		strings.HasPrefix(addr, streamDataPrefix)
}

// PeerInfo describes a connected peer.
type PeerInfo struct {
	Name      string `json:"name"`
	ExitNode  bool   `json:"exit_node"`
	Direction string `json:"direction"`
	Native    bool   `json:"native"`
	LatencyMs int    `json:"latency_ms"` // self-reported latency to this peer
	Version   string `json:"version,omitempty"`
}

// NodeVersion is the version string sent during peer registration.
// Set by the app package at init time.
var NodeVersion = "1.0.0"

// peerMeta is extensible metadata exchanged after basic handshake.
// New fields can be added freely — old peers ignore unknown fields.
type peerMeta struct {
	Version string `json:"v,omitempty"`
	// Future fields go here (e.g., Capabilities, Region, etc.)
}

// writeMeta sends a length-prefixed JSON metadata blob.
func writeMeta(w io.Writer, m peerMeta) {
	data, _ := json.Marshal(m)
	var lenBuf [2]byte
	binary.BigEndian.PutUint16(lenBuf[:], uint16(len(data)))
	w.Write(lenBuf[:])
	w.Write(data)
}

// readMeta reads a length-prefixed JSON metadata blob with timeout.
// Returns zero-value peerMeta if the remote is old (doesn't send metadata).
func readMeta(r net.Conn, timeout time.Duration) peerMeta {
	r.SetReadDeadline(time.Now().Add(timeout))
	defer r.SetReadDeadline(time.Time{})
	var lenBuf [2]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return peerMeta{}
	}
	n := binary.BigEndian.Uint16(lenBuf[:])
	if n == 0 || n > 4096 {
		return peerMeta{}
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(r, data); err != nil {
		return peerMeta{}
	}
	var m peerMeta
	json.Unmarshal(data, &m)
	return m
}

// --- Node ---

type peer struct {
	info      PeerInfo
	client    hyclient.Client // primary outbound client (first IP)
	ctrlW     net.Conn        // write dial requests to this peer
	writeMu   sync.Mutex
	waiting   map[string]chan net.Conn
	txBytes   atomic.Uint64
	rxBytes   atomic.Uint64
	failCount atomic.Int32 // consecutive ping failures
	cancel    context.CancelFunc

	// Multi-IP: additional outbound QUIC connections
	extraConns   []hyclient.Client // extra clients (index 0 = second IP, etc.)
	connAddrs    []string          // address per connection (index 0 = primary, 1+ = extras)
	connStatuses []string          // per-IP status: "online", "offline", "mismatch", "native"
	connSeq      atomic.Uint64    // round-robin counter for stream distribution
}

// pickClient returns the next QUIC client using round-robin across healthy connections.
func (p *peer) pickClient() hyclient.Client {
	if len(p.extraConns) == 0 {
		return p.client
	}
	// Build list of healthy clients (primary + online extras)
	healthy := []hyclient.Client{p.client}
	for i, c := range p.extraConns {
		status := "online"
		if i < len(p.connStatuses) {
			status = p.connStatuses[i]
		}
		if status == "online" {
			healthy = append(healthy, c)
		}
	}
	if len(healthy) == 1 {
		return p.client
	}
	idx := int(p.connSeq.Add(1)) % len(healthy)
	return healthy[idx]
}

// ConnCount returns the total number of active QUIC connections for this peer.
func (p *peer) ConnCount() int {
	if p.client == nil {
		return 0
	}
	return 1 + len(p.extraConns)
}

// AddConn adds an extra QUIC connection for a secondary IP address.
func (p *peer) AddConn(client hyclient.Client, addr, status string) {
	p.extraConns = append(p.extraConns, client)
	p.connAddrs = append(p.connAddrs, addr)
	p.connStatuses = append(p.connStatuses, status)
}

// PeerTraffic holds per-peer traffic info.
type PeerTraffic struct {
	Name   string
	TxRate uint64
	RxRate uint64
}

// Node is a unified relay endpoint. It can accept peers (hy2 server side)
// and connect to peers (hy2 client side) simultaneously.
// Stats holds traffic counters.
type Stats struct {
	TxBytes    uint64 // total uploaded
	RxBytes    uint64 // total downloaded
	TxRate     uint64 // bytes/sec upload (snapshot)
	RxRate     uint64 // bytes/sec download (snapshot)
	Conns      int    // active stream count
	ExitClients int   // peers using this node as exit
}

type Node struct {
	name  string
	exit  bool
	mu    sync.RWMutex
	peers map[string]*peer
	seq   atomic.Uint64

	nestedMu sync.RWMutex
	nested   map[string]bool

	// Traffic counters
	txBytes   atomic.Uint64
	rxBytes   atomic.Uint64
	prevTx    uint64
	prevRx    uint64
	txRate    atomic.Uint64
	rxRate    atomic.Uint64
	conns     atomic.Int64

	// Per-peer rate snapshots (updated by rate ticker)
	peerRateMu sync.RWMutex
	peerRates  map[string]PeerTraffic

	// Per-peer latency (updated by background prober)
	latencyMu    sync.RWMutex
	latencies    map[string]int
	peersOfCache map[string][]PeerInfo // cached PeersOf results from prober
}

// PeerRates returns per-peer traffic rates.
func (n *Node) PeerRates() map[string]PeerTraffic {
	n.peerRateMu.RLock()
	defer n.peerRateMu.RUnlock()
	out := make(map[string]PeerTraffic, len(n.peerRates))
	for k, v := range n.peerRates {
		out[k] = v
	}
	return out
}

// SetLatency stores a latency measurement for a peer (called by prober or inbound report).
func (n *Node) SetLatency(peerName string, ms int) {
	n.latencyMu.Lock()
	if ms > 0 || n.latencies[peerName] <= 0 {
		// Only update if new value is valid, or no previous good value exists
		n.latencies[peerName] = ms
	}
	n.latencyMu.Unlock()
}

// GetLatency returns stored latency for a peer. -1 if unknown.
func (n *Node) GetLatency(peerName string) int {
	n.latencyMu.RLock()
	defer n.latencyMu.RUnlock()
	if ms, ok := n.latencies[peerName]; ok {
		return ms
	}
	return -1
}

// SetPeersOfCache sets the cached peer list for a given peer name.
// Called by the API sub-peers updater to share nested topology data.
func (n *Node) SetPeersOfCache(peerName string, peers []PeerInfo) {
	n.peerRateMu.Lock()
	defer n.peerRateMu.Unlock()
	if n.peersOfCache == nil {
		n.peersOfCache = make(map[string][]PeerInfo)
	}
	n.peersOfCache[peerName] = peers
}

// PeersOfCached returns cached peer list from the last prober cycle.
func (n *Node) PeersOfCached(peerName string) ([]PeerInfo, bool) {
	n.peerRateMu.RLock()
	defer n.peerRateMu.RUnlock()
	if n.peersOfCache == nil {
		return nil, false
	}
	peers, ok := n.peersOfCache[peerName]
	return peers, ok
}

// StartLatencyProber periodically pings outbound peers and caches their peer lists.
func (n *Node) StartLatencyProber(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n.mu.RLock()
			peers := make([]*peer, 0, len(n.peers))
			names := make([]string, 0, len(n.peers))
			for name, p := range n.peers {
				peers = append(peers, p)
				names = append(names, name)
			}
			n.mu.RUnlock()
			for i, p := range peers {
				if p.client == nil {
					continue
				}
				go func(name string, p *peer) {
					rtt := n.PingPeer(name)
					if rtt >= 0 {
						n.SetLatency(name, int(rtt.Milliseconds()))
						p.failCount.Store(0)
						// Cache peer's sub-peers only if nested discovery is enabled
						if n.IsNestedEnabled(name) {
							if subPeers, err := n.PeersOf(name); err == nil {
								n.peerRateMu.Lock()
								n.peersOfCache[name] = subPeers
								n.peerRateMu.Unlock()
							}
						}
					} else {
						n.SetLatency(name, -1)
						fails := p.failCount.Add(1)
						if fails >= 3 && p.cancel != nil {
							log.Printf("[relay] %s: %d consecutive ping failures, disconnecting for reconnect", name, fails)
							p.cancel()
						}
					}
				}(names[i], p)
			}
		}
	}
}

// Name returns the node's name.
func (n *Node) Name() string { return n.name }

// SetName updates the node's name.
func (n *Node) SetName(name string) { n.name = name }

// IsExit returns whether this node is an exit node.
func (n *Node) IsExit() bool { return n.exit }

// SetExit updates the exit node flag.
func (n *Node) SetExit(exit bool) { n.exit = exit }

// GetStats returns current traffic statistics.
func (n *Node) GetStats() Stats {
	n.mu.RLock()
	exitCount := 0
	for _, p := range n.peers {
		if p.info.Direction == "inbound" {
			exitCount++
		}
	}
	n.mu.RUnlock()
	return Stats{
		TxBytes:     n.txBytes.Load(),
		RxBytes:     n.rxBytes.Load(),
		TxRate:      n.txRate.Load(),
		RxRate:      n.rxRate.Load(),
		Conns:       int(n.conns.Load()),
		ExitClients: exitCount,
	}
}

// StartRateTicker updates per-second rate counters. Call in a goroutine.
func (n *Node) StartRateTicker(ctx context.Context) {
	prevPeerTx := make(map[string]uint64)
	prevPeerRx := make(map[string]uint64)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tx := n.txBytes.Load()
			rx := n.rxBytes.Load()
			n.txRate.Store(tx - n.prevTx)
			n.rxRate.Store(rx - n.prevRx)
			n.prevTx = tx
			n.prevRx = rx

			// Per-peer rates
			rates := make(map[string]PeerTraffic)
			n.mu.RLock()
			for name, p := range n.peers {
				ptx := p.txBytes.Load()
				prx := p.rxBytes.Load()
				rates[name] = PeerTraffic{
					Name:   name,
					TxRate: ptx - prevPeerTx[name],
					RxRate: prx - prevPeerRx[name],
				}
				prevPeerTx[name] = ptx
				prevPeerRx[name] = prx
			}
			n.mu.RUnlock()
			n.peerRateMu.Lock()
			n.peerRates = rates
			n.peerRateMu.Unlock()
		}
	}
}

// NewNode creates a node.
func NewNode(name string, exitNode bool) *Node {
	return &Node{
		name:      name,
		exit:      exitNode,
		peers:     make(map[string]*peer),
		nested:    make(map[string]bool),
		peerRates: make(map[string]PeerTraffic),
		latencies:    make(map[string]int),
		peersOfCache: make(map[string][]PeerInfo),
	}
}

// --- Outbound: connect to a remote node's hy2 server ---

// AttachTo connects to a remote node via an hy2 client, registers, and
// handles dial requests from the remote. Blocks until ctx or connection ends.
// AttachTo connects to a remote node via an hy2 client, registers, and
// handles dial requests from the remote. Blocks until ctx or connection ends.
// Returns the remote's actual node ID via the onID callback (if non-nil).
// AttachNative registers a plain hy2 server (no relay protocol) as a peer.
// Only DialTCP works (direct proxy). No nested discovery, no control stream.
func (n *Node) AttachNative(ctx context.Context, peerName string, client hyclient.Client) error {
	childCtx, cancel := context.WithCancel(ctx)
	p := &peer{
		info:    PeerInfo{Name: peerName, Direction: "outbound", Native: true},
		client:  client,
		waiting: make(map[string]chan net.Conn),
		cancel:  cancel,
	}
	n.mu.Lock()
	n.peers[peerName] = p
	n.mu.Unlock()
	defer func() {
		cancel()
		n.mu.Lock()
		delete(n.peers, peerName)
		n.mu.Unlock()
	}()
	<-childCtx.Done()
	return childCtx.Err()
}

func (n *Node) AttachTo(ctx context.Context, peerName string, client hyclient.Client, onID func(string)) error {
	// Register with remote
	regStream, err := client.TCP(streamRegister)
	if err != nil {
		// If the error looks like a proxy failure (native hy2 tried to TCP-proxy
		// the relay stream address), it's a native server. Otherwise it's a
		// transient network error that should be retried.
		errStr := err.Error()
		if strings.Contains(errStr, "NXDOMAIN") ||
			strings.Contains(errStr, "connection refused") ||
			strings.Contains(errStr, "no such host") ||
			strings.Contains(errStr, "connect:") {
			return ErrNotHy2scale
		}
		return fmt.Errorf("relay: register: %w", err)
	}
	defer regStream.Close()

	var flags byte
	if n.exit {
		flags |= 0x01
	}
	flags |= 0x02 // bit 1: supports metadata exchange
	regStream.Write([]byte{flags})
	writeString(regStream, n.name)

	// Read back the remote's actual node ID
	remoteID, err := readString(regStream)
	if err != nil {
		return ErrNotHy2scale
	}
	actualName := peerName
	if remoteID != "" {
		actualName = remoteID
	}
	// Read remote metadata (2s timeout — old servers don't send it)
	remoteMeta := readMeta(regStream, 2*time.Second)
	if remoteMeta.Version == "" {
		remoteMeta.Version = "1.0.0" // old peers don't send version
	}
	// Send our metadata (remote reads it only if it sent the flag)
	writeMeta(regStream, peerMeta{Version: NodeVersion})

	if onID != nil {
		onID(actualName)
	}

	// Open s2c ctrl for dial requests from remote
	s2cCtrl, err := client.TCP(streamCtrlS2C)
	if err != nil {
		return fmt.Errorf("relay: s2c ctrl: %w", err)
	}
	defer s2cCtrl.Close()
	writeString(s2cCtrl, n.name)

	// Register peer with remote's actual name
	childCtx, childCancel := context.WithCancel(ctx)
	p := &peer{
		info:    PeerInfo{Name: actualName, Direction: "outbound", Version: remoteMeta.Version},
		client:  client,
		waiting: make(map[string]chan net.Conn),
		cancel:  childCancel,
	}
	n.mu.Lock()
	n.peers[actualName] = p
	n.mu.Unlock()
	defer func() {
		childCancel()
		n.mu.Lock()
		delete(n.peers, actualName)
		n.mu.Unlock()
	}()

	// Serve dial requests from remote
	errCh := make(chan error, 1)
	go func() {
		for {
			id, addr, err := readRequest(s2cCtrl)
			if err != nil {
				errCh <- err
				return
			}
			go n.dialAndStream(ctx, actualName, client, id, addr)
		}
	}()

	// Periodically report our latency to the remote so it knows inbound latency
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				ms := n.GetLatency(actualName)
				if ms <= 0 {
					continue
				}
				stream, err := client.TCP(streamLatencyReport)
				if err != nil {
					continue
				}
				writeString(stream, n.name)
				var lb [2]byte
				if ms > 65535 {
					ms = 65535
				}
				binary.BigEndian.PutUint16(lb[:], uint16(ms))
				stream.Write(lb[:])
				stream.Close()
			}
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("relay: peer %s disconnected: %w", peerName, err)
	case <-childCtx.Done():
		return fmt.Errorf("relay: peer %s: health check triggered disconnect", peerName)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// --- Inbound: accept peers connecting to our hy2 server ---

// HandleStream routes relay streams from the hy2 server's Outbound.TCP.
func (n *Node) HandleStream(ctx context.Context, reqAddr string, stream net.Conn) {
	switch reqAddr {
	case streamRegister:
		n.handleRegister(ctx, stream)

	case streamCtrlS2C:
		name, err := readString(stream)
		if err != nil {
			stream.Close()
			return
		}
		n.mu.Lock()
		if p, ok := n.peers[name]; ok {
			p.ctrlW = stream
		}
		n.mu.Unlock()
		<-ctx.Done()

	case streamLatencyReport:
		n.handleLatencyReport(stream)

	case streamListPeers:
		n.handleListPeers(stream)

	default:
		if nodeName, targetAddr, ok := parseVia(reqAddr); ok {
			n.handleVia(ctx, nodeName, targetAddr, stream)
			return
		}
		if strings.HasPrefix(reqAddr, streamDataPrefix) {
			id := strings.TrimSuffix(reqAddr[len(streamDataPrefix):], streamDataSuffix)
			n.deliverDataStream(id, stream)
		}
	}
}

func (n *Node) handleRegister(ctx context.Context, stream net.Conn) {
	var flags [1]byte
	if _, err := io.ReadFull(stream, flags[:]); err != nil {
		stream.Close()
		return
	}
	name, err := readString(stream)
	if err != nil {
		stream.Close()
		return
	}
	// Send back our name
	writeString(stream, n.name)

	// Metadata exchange: only if client set the metadata flag (bit 1)
	var remoteMeta peerMeta
	if flags[0]&0x02 != 0 {
		// Send our metadata, then read client's
		writeMeta(stream, peerMeta{Version: NodeVersion})
		remoteMeta = readMeta(stream, 2*time.Second)
	}
	if remoteMeta.Version == "" {
		remoteMeta.Version = "1.0.0" // old peers don't send version
	}

	p := &peer{
		info: PeerInfo{
			Name:      name,
			ExitNode:  flags[0]&0x01 != 0,
			Direction: "inbound",
			Version:   remoteMeta.Version,
		},
		waiting: make(map[string]chan net.Conn),
	}

	n.mu.Lock()
	n.peers[name] = p
	n.mu.Unlock()

	<-ctx.Done()

	n.mu.Lock()
	delete(n.peers, name)
	n.mu.Unlock()
}

// handleLatencyReport reads a latency report from an inbound peer.
// Format: peer name (string) + latency ms (uint16 big-endian)
func (n *Node) handleLatencyReport(stream net.Conn) {
	defer stream.Close()
	name, err := readString(stream)
	if err != nil {
		return
	}
	var lb [2]byte
	if _, err := io.ReadFull(stream, lb[:]); err != nil {
		return
	}
	ms := int(binary.BigEndian.Uint16(lb[:]))
	n.SetLatency(name, ms)
}

func (n *Node) handleListPeers(stream net.Conn) {
	defer stream.Close()
	peers := n.Peers()
	data, _ := json.Marshal(peers)
	stream.Write(data)
}

func (n *Node) handleVia(ctx context.Context, peerName, targetAddr string, stream net.Conn) {
	var exitConn net.Conn
	var err error
	if parts := strings.Split(peerName, "/"); len(parts) > 1 {
		exitConn, err = n.DialVia(ctx, parts, targetAddr)
	} else {
		exitConn, err = n.DialTCP(ctx, peerName, targetAddr)
	}
	if err != nil {
		stream.Close()
		return
	}
	defer exitConn.Close()
	defer stream.Close()
	// exitConn is already a countedConn from DialTCP/DialVia, so just use plain io.Copy
	go func() { io.Copy(exitConn, stream); exitConn.Close() }()
	io.Copy(stream, exitConn)
}

func (n *Node) deliverDataStream(id string, stream net.Conn) {
	n.mu.RLock()
	for _, p := range n.peers {
		p.writeMu.Lock()
		ch, ok := p.waiting[id]
		if ok {
			delete(p.waiting, id)
			p.writeMu.Unlock()
			n.mu.RUnlock()
			ch <- stream
			return
		}
		p.writeMu.Unlock()
	}
	n.mu.RUnlock()

	// Retry with delay
	for i := 0; i < 50; i++ {
		time.Sleep(10 * time.Millisecond)
		n.mu.RLock()
		for _, p := range n.peers {
			p.writeMu.Lock()
			ch, ok := p.waiting[id]
			if ok {
				delete(p.waiting, id)
				p.writeMu.Unlock()
				n.mu.RUnlock()
				ch <- stream
				return
			}
			p.writeMu.Unlock()
		}
		n.mu.RUnlock()
	}
	stream.Close()
}

func (n *Node) dialAndStream(ctx context.Context, peerName string, client hyclient.Client, id, addr string) {
	var target net.Conn
	var err error

	// Check if addr is a via request (multi-hop forwarding)
	if viaPeer, targetAddr, ok := parseVia(addr); ok {
		if parts := strings.Split(viaPeer, "/"); len(parts) > 1 {
			target, err = n.DialVia(ctx, parts, targetAddr)
		} else {
			target, err = n.DialTCP(ctx, viaPeer, targetAddr)
		}
	} else {
		target, err = net.DialTimeout("tcp", addr, 10*time.Second)
	}
	if err != nil {
		return
	}
	defer target.Close()

	stream, err := client.TCP(streamDataPrefix + id + streamDataSuffix)
	if err != nil {
		return
	}
	defer stream.Close()

	n.conns.Add(1)
	defer n.conns.Add(-1)
	n.mu.RLock()
	p := n.peers[peerName]
	n.mu.RUnlock()
	var ptx, prx *atomic.Uint64
	if p != nil {
		ptx = &p.txBytes
		prx = &p.rxBytes
	}
	go func() { n.copyCount(stream, target, &n.rxBytes, prx); stream.Close() }()
	n.copyCount(target, stream, &n.txBytes, ptx)
}

// --- Peer queries ---

// Peers returns directly connected peers.
func (n *Node) Peers() []PeerInfo {
	n.mu.RLock()
	defer n.mu.RUnlock()
	n.latencyMu.RLock()
	defer n.latencyMu.RUnlock()
	result := make([]PeerInfo, 0, len(n.peers))
	for name, p := range n.peers {
		info := p.info
		if ms, ok := n.latencies[name]; ok {
			info.LatencyMs = ms
		}
		result = append(result, info)
	}
	return result
}

// PeersOf returns a peer's peers. Requires nested discovery enabled for that peer.
func (n *Node) PeersOf(peerName string) ([]PeerInfo, error) {
	n.mu.RLock()
	p, ok := n.peers[peerName]
	n.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("relay: peer %q not connected", peerName)
	}

	type result struct {
		peers []PeerInfo
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		var data []byte
		var err error
		if p.client != nil {
			stream, serr := p.client.TCP(streamListPeers)
			if serr != nil {
				ch <- result{nil, serr}
				return
			}
			defer stream.Close()
			data, err = io.ReadAll(stream)
		} else {
			// Inbound peer: query via HTTP through reverse tunnel to their API
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, cerr := n.DialTCP(ctx, peerName, "127.0.0.1:5565")
			if cerr != nil {
				ch <- result{nil, cerr}
				return
			}
			defer conn.Close()
			fmt.Fprintf(conn, "GET /scale/api/internal/peers HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n")
			resp, rerr := http.ReadResponse(bufio.NewReader(conn), nil)
			if rerr != nil {
				ch <- result{nil, rerr}
				return
			}
			defer resp.Body.Close()
			data, err = io.ReadAll(resp.Body)
		}
		if err != nil {
			ch <- result{nil, err}
			return
		}
		var peers []PeerInfo
		if err := json.Unmarshal(data, &peers); err != nil {
			ch <- result{nil, err}
			return
		}
		ch <- result{peers, nil}
	}()
	select {
	case r := <-ch:
		return r.peers, r.err
	case <-time.After(3 * time.Second):
		return nil, fmt.Errorf("relay: peer %q query timeout", peerName)
	}
}

// PeersOfVia returns a peer's peers through a multi-hop path.
// path = ["vm", "au"] means: query au's peers by routing through vm.
func (n *Node) PeersOfVia(ctx context.Context, path []string) ([]PeerInfo, error) {
	if len(path) == 0 {
		return nil, fmt.Errorf("relay: empty path")
	}
	if len(path) == 1 {
		return n.PeersOf(path[0])
	}
	// Route through the chain to the target's list-peers stream
	conn, err := n.DialVia(ctx, path, streamListPeers)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	data, err := io.ReadAll(conn)
	if err != nil {
		return nil, err
	}
	var peers []PeerInfo
	if err := json.Unmarshal(data, &peers); err != nil {
		return nil, err
	}
	return peers, nil
}

// SetNestedDiscovery enables/disables nested peer discovery for a peer.
func (n *Node) SetNestedDiscovery(peerName string, enabled bool) {
	n.nestedMu.Lock()
	n.nested[peerName] = enabled
	n.nestedMu.Unlock()
}

// IsNestedEnabled returns whether nested discovery is enabled for a peer.
func (n *Node) IsNestedEnabled(peerName string) bool {
	n.nestedMu.RLock()
	defer n.nestedMu.RUnlock()
	return n.nested[peerName]
}

// HasPeer checks if a peer is connected.
func (n *Node) HasPeer(name string) bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	_, ok := n.peers[name]
	return ok
}

// AddPeerConn adds an extra QUIC connection to an existing peer.
func (n *Node) AddPeerConn(name string, client hyclient.Client, addr, status string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	p, ok := n.peers[name]
	if !ok {
		return
	}
	p.AddConn(client, addr, status)
}

// IPStatus represents per-IP connection info.
type IPStatus struct {
	Addr   string `json:"addr"`
	Status string `json:"status"` // "online", "offline", "mismatch", "native"
}

// PeerIPStatuses returns per-IP status for a peer.
func (n *Node) PeerIPStatuses(name string) []IPStatus {
	n.mu.RLock()
	defer n.mu.RUnlock()
	p, ok := n.peers[name]
	if !ok {
		return nil
	}
	var result []IPStatus
	// Primary
	primaryAddr := ""
	if len(p.connAddrs) > 0 {
		primaryAddr = p.connAddrs[0]
	}
	if primaryAddr != "" {
		result = append(result, IPStatus{Addr: primaryAddr, Status: "online"})
	}
	// Extras
	for i, addr := range p.connAddrs[1:] {
		status := "online"
		if i < len(p.connStatuses) {
			status = p.connStatuses[i]
		}
		result = append(result, IPStatus{Addr: addr, Status: status})
	}
	return result
}

// PeerConnCount returns the number of QUIC connections to a peer.
func (n *Node) PeerConnCount(name string) int {
	n.mu.RLock()
	defer n.mu.RUnlock()
	p, ok := n.peers[name]
	if !ok {
		return 0
	}
	return p.ConnCount()
}

// ConnectedPeerNames returns names of all connected peers (outbound with active client).
func (n *Node) ConnectedPeerNames() []string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	var names []string
	for name, p := range n.peers {
		// Include both outbound (has client) and inbound (has ctrlW) peers
		if p.client != nil || p.ctrlW != nil {
			names = append(names, name)
		}
	}
	return names
}

// IsInbound returns true if the peer is connected inbound (no client).
func (n *Node) IsInbound(name string) bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	p, ok := n.peers[name]
	return ok && p.client == nil
}

// NativeMap returns a map of peer names to their native status.
func (n *Node) NativeMap() map[string]bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	m := make(map[string]bool)
	for name, p := range n.peers {
		m[name] = p.info.Native
	}
	return m
}

// PingPeer measures round-trip latency to a peer. Returns -1 if unreachable.
// Non-blocking: uses a 3-second timeout.
func (n *Node) PingPeer(name string) time.Duration {
	n.mu.RLock()
	p, ok := n.peers[name]
	n.mu.RUnlock()
	if !ok || p.client == nil {
		return -1
	}
	ch := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		if p.info.Native {
			// Native hy2: HTTP request through proxy to measure real latency
			stream, err := p.client.TCP("1.1.1.1:80")
			if err != nil {
				ch <- -1
				return
			}
			stream.Write([]byte("HEAD / HTTP/1.1\r\nHost: 1.1.1.1\r\nConnection: close\r\n\r\n"))
			buf := make([]byte, 1)
			stream.Read(buf)
			stream.Close()
			ch <- time.Since(start)
			return
		}
		stream, err := p.client.TCP(streamListPeers)
		if err != nil {
			ch <- -1
			return
		}
		io.ReadAll(stream)
		stream.Close()
		ch <- time.Since(start)
	}()
	select {
	case d := <-ch:
		return d
	case <-time.After(2 * time.Second):
		return -1
	}
}

// --- Counted connection wrapper ---

type countedConn struct {
	net.Conn
	tx, rx         *atomic.Uint64
	peerTx, peerRx *atomic.Uint64
	conns          *atomic.Int64
	closed         atomic.Bool
}

func (c *countedConn) Close() error {
	if c.closed.CompareAndSwap(false, true) {
		c.conns.Add(-1)
	}
	return c.Conn.Close()
}

func (c *countedConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.rx.Add(uint64(n))
		if c.peerRx != nil {
			c.peerRx.Add(uint64(n))
		}
	}
	return n, err
}

func (c *countedConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 {
		c.tx.Add(uint64(n))
		if c.peerTx != nil {
			c.peerTx.Add(uint64(n))
		}
	}
	return n, err
}

func (n *Node) wrapConn(peerName string, conn net.Conn) net.Conn {
	n.mu.RLock()
	p := n.peers[peerName]
	n.mu.RUnlock()
	cc := &countedConn{Conn: conn, tx: &n.txBytes, rx: &n.rxBytes, conns: &n.conns}
	if p != nil {
		cc.peerTx = &p.txBytes
		cc.peerRx = &p.rxBytes
	}
	n.conns.Add(1)
	return cc
}

// --- Dial ---

// DialTCP dials addr through a directly connected peer's network.
func (n *Node) DialTCP(ctx context.Context, peerName string, addr string) (net.Conn, error) {
	n.mu.RLock()
	p, ok := n.peers[peerName]
	n.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("relay: peer %q not connected", peerName)
	}

	// Outbound peer: use round-robin across available QUIC connections
	if p.client != nil {
		cl := p.pickClient()
		conn, err := cl.TCP(addr)
		if err != nil {
			return nil, err
		}
		return n.wrapConn(peerName, conn), nil
	}

	// Inbound peer: send dial request via control stream
	if p.ctrlW == nil {
		return nil, fmt.Errorf("relay: peer %q control not ready", peerName)
	}

	id := fmt.Sprintf("%d", n.seq.Add(1))
	ch := make(chan net.Conn, 1)
	p.writeMu.Lock()
	p.waiting[id] = ch
	err := writeRequest(p.ctrlW, id, addr)
	p.writeMu.Unlock()
	if err != nil {
		return nil, err
	}

	select {
	case conn := <-ch:
		if conn == nil {
			return nil, fmt.Errorf("relay: dial via %q failed", peerName)
		}
		return n.wrapConn(peerName, conn), nil
	case <-ctx.Done():
		p.writeMu.Lock()
		delete(p.waiting, id)
		p.writeMu.Unlock()
		return nil, ctx.Err()
	}
}

// DialVia dials addr through a chain of peers.
// path = ["au", "jp"] means: this node → au → jp → internet.
func (n *Node) DialVia(ctx context.Context, path []string, addr string) (net.Conn, error) {
	if len(path) == 0 {
		return nil, fmt.Errorf("relay: empty path")
	}
	if len(path) == 1 {
		return n.DialTCP(ctx, path[0], addr)
	}

	// Multi-hop: connect to first peer, ask it to route via remaining path
	firstPeer := path[0]
	n.mu.RLock()
	p, ok := n.peers[firstPeer]
	n.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("relay: peer %q not connected", firstPeer)
	}

	// Build nested via address
	remaining := strings.Join(path[1:], "/")
	viaAddr := streamViaPrefix + remaining + "_" + addr + ":0"

	if p.client != nil {
		cl := p.pickClient()
		conn, err := cl.TCP(viaAddr)
		if err != nil {
			return nil, err
		}
		return n.wrapConn(firstPeer, conn), nil
	}

	// Inbound peer: send via address as a dial request through control stream.
	// The inbound peer receives viaAddr, its handleVia will parse and forward.
	if p.ctrlW == nil {
		return nil, fmt.Errorf("relay: peer %q control not ready", firstPeer)
	}

	id := fmt.Sprintf("%d", n.seq.Add(1))
	ch := make(chan net.Conn, 1)
	p.writeMu.Lock()
	p.waiting[id] = ch
	err := writeRequest(p.ctrlW, id, viaAddr)
	p.writeMu.Unlock()
	if err != nil {
		return nil, err
	}

	select {
	case conn := <-ch:
		if conn == nil {
			return nil, fmt.Errorf("relay: dial via %q failed", firstPeer)
		}
		return n.wrapConn(firstPeer, conn), nil
	case <-ctx.Done():
		p.writeMu.Lock()
		delete(p.waiting, id)
		p.writeMu.Unlock()
		return nil, ctx.Err()
	}
}

// --- Wire helpers ---

func (n *Node) copyCount(dst io.Writer, src io.Reader, counter *atomic.Uint64, peerCounter *atomic.Uint64) {
	buf := make([]byte, 32*1024)
	for {
		nr, er := src.Read(buf)
		if nr > 0 {
			nw, ew := dst.Write(buf[:nr])
			if nw > 0 {
				counter.Add(uint64(nw))
				if peerCounter != nil {
					peerCounter.Add(uint64(nw))
				}
			}
			if ew != nil {
				return
			}
		}
		if er != nil {
			return
		}
	}
}

func parseVia(reqAddr string) (nodeName, targetAddr string, ok bool) {
	if !strings.HasPrefix(reqAddr, streamViaPrefix) {
		return "", "", false
	}
	rest := reqAddr[len(streamViaPrefix):]
	idx := strings.Index(rest, "_")
	if idx <= 0 {
		return "", "", false
	}
	nodeName = rest[:idx]
	targetAddr = strings.TrimSuffix(rest[idx+1:], ":0")
	return nodeName, targetAddr, true
}

func writeString(w io.Writer, s string) error {
	b := []byte(s)
	var lb [2]byte
	binary.BigEndian.PutUint16(lb[:], uint16(len(b)))
	if _, err := w.Write(lb[:]); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}

func readString(r io.Reader) (string, error) {
	var lb [2]byte
	if _, err := io.ReadFull(r, lb[:]); err != nil {
		return "", err
	}
	buf := make([]byte, binary.BigEndian.Uint16(lb[:]))
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

func writeRequest(w io.Writer, id, addr string) error {
	if err := writeString(w, id); err != nil {
		return err
	}
	return writeString(w, addr)
}

func readRequest(r io.Reader) (id, addr string, err error) {
	id, err = readString(r)
	if err != nil {
		return
	}
	addr, err = readString(r)
	return
}
