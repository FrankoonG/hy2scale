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
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	hyclient "github.com/apernet/hysteria/core/v2/client"
)

const (
	streamRegister   = "_relay_register_:0"
	streamCtrlS2C    = "_relay_s2c_ctrl_:0"
	streamListPeers  = "_relay_list_peers_:0"
	streamViaPrefix  = "_relay_via_"
	streamDataPrefix = "_relay_data_"
	streamDataSuffix = ":0"
)

// IsRelayStream returns true if addr is a relay internal stream.
func IsRelayStream(addr string) bool {
	return addr == streamRegister ||
		addr == streamCtrlS2C ||
		addr == streamListPeers ||
		strings.HasPrefix(addr, streamViaPrefix) ||
		strings.HasPrefix(addr, streamDataPrefix)
}

// PeerInfo describes a connected peer.
type PeerInfo struct {
	Name      string `json:"name"`
	ExitNode  bool   `json:"exit_node"`
	Direction string `json:"direction"` // "inbound" or "outbound"
}

// --- Node ---

type peer struct {
	info    PeerInfo
	client  hyclient.Client // non-nil for outbound (we connected to them)
	ctrlW   net.Conn        // write dial requests to this peer
	writeMu sync.Mutex
	waiting map[string]chan net.Conn
}

// Node is a unified relay endpoint. It can accept peers (hy2 server side)
// and connect to peers (hy2 client side) simultaneously.
type Node struct {
	name  string
	exit  bool
	mu    sync.RWMutex
	peers map[string]*peer
	seq   atomic.Uint64

	nestedMu sync.RWMutex
	nested   map[string]bool // peer name → nested discovery enabled
}

// NewNode creates a node.
func NewNode(name string, exitNode bool) *Node {
	return &Node{
		name:   name,
		exit:   exitNode,
		peers:  make(map[string]*peer),
		nested: make(map[string]bool),
	}
}

// --- Outbound: connect to a remote node's hy2 server ---

// AttachTo connects to a remote node via an hy2 client, registers, and
// handles dial requests from the remote. Blocks until ctx or connection ends.
func (n *Node) AttachTo(ctx context.Context, peerName string, client hyclient.Client) error {
	// Register with remote
	regStream, err := client.TCP(streamRegister)
	if err != nil {
		return fmt.Errorf("relay: register: %w", err)
	}
	defer regStream.Close()

	var flags byte
	if n.exit {
		flags |= 0x01
	}
	regStream.Write([]byte{flags})
	writeString(regStream, n.name)

	// Open s2c ctrl for dial requests from remote
	s2cCtrl, err := client.TCP(streamCtrlS2C)
	if err != nil {
		return fmt.Errorf("relay: s2c ctrl: %w", err)
	}
	defer s2cCtrl.Close()
	writeString(s2cCtrl, n.name)

	// Register peer (outbound)
	p := &peer{
		info:    PeerInfo{Name: peerName, Direction: "outbound"},
		client:  client,
		waiting: make(map[string]chan net.Conn),
	}
	n.mu.Lock()
	n.peers[peerName] = p
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		delete(n.peers, peerName)
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
			go n.dialAndStream(ctx, client, id, addr)
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("relay: peer %s disconnected: %w", peerName, err)
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

	p := &peer{
		info: PeerInfo{
			Name:      name,
			ExitNode:  flags[0]&0x01 != 0,
			Direction: "inbound",
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

func (n *Node) handleListPeers(stream net.Conn) {
	defer stream.Close()
	peers := n.Peers()
	data, _ := json.Marshal(peers)
	stream.Write(data)
}

func (n *Node) handleVia(ctx context.Context, peerName, targetAddr string, stream net.Conn) {
	exitConn, err := n.DialTCP(ctx, peerName, targetAddr)
	if err != nil {
		stream.Close()
		return
	}
	defer exitConn.Close()
	defer stream.Close()
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

func (n *Node) dialAndStream(ctx context.Context, client hyclient.Client, id, addr string) {
	var target net.Conn
	var err error

	// Check if addr is a via request (multi-hop forwarding)
	if peerName, targetAddr, ok := parseVia(addr); ok {
		// Route through one of our peers
		target, err = n.DialTCP(ctx, peerName, targetAddr)
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

	go func() { io.Copy(stream, target); stream.Close() }()
	io.Copy(target, stream)
}

// --- Peer queries ---

// Peers returns directly connected peers.
func (n *Node) Peers() []PeerInfo {
	n.mu.RLock()
	defer n.mu.RUnlock()
	result := make([]PeerInfo, 0, len(n.peers))
	for _, p := range n.peers {
		result = append(result, p.info)
	}
	return result
}

// PeersOf returns a peer's peers. Requires nested discovery enabled for that peer.
func (n *Node) PeersOf(peerName string) ([]PeerInfo, error) {
	n.nestedMu.RLock()
	enabled := n.nested[peerName]
	n.nestedMu.RUnlock()
	if !enabled {
		return nil, fmt.Errorf("relay: nested discovery not enabled for %q", peerName)
	}

	n.mu.RLock()
	p, ok := n.peers[peerName]
	n.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("relay: peer %q not connected", peerName)
	}

	if p.client == nil {
		return nil, fmt.Errorf("relay: peer %q is inbound, cannot query (no client)", peerName)
	}

	// Open a list-peers stream to the peer
	stream, err := p.client.TCP(streamListPeers)
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	data, err := io.ReadAll(stream)
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

// HasPeer checks if a peer is connected.
func (n *Node) HasPeer(name string) bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	_, ok := n.peers[name]
	return ok
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

	// Outbound peer: we have a client, use it directly
	if p.client != nil {
		return p.client.TCP(addr)
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
		return conn, nil
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
		return p.client.TCP(viaAddr)
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
		return conn, nil
	case <-ctx.Done():
		p.writeMu.Lock()
		delete(p.waiting, id)
		p.writeMu.Unlock()
		return nil, ctx.Err()
	}
}

// --- Wire helpers ---

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
