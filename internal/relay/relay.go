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
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	hyclient "github.com/apernet/hysteria/core/v2/client"
)

// localExitGW caches the Docker gateway for local exit address rewriting.
var localExitGW = sync.OnceValue(func() string {
	if gw := os.Getenv("LOCAL_EXIT_GATEWAY"); gw != "" {
		log.Printf("[relay] using LOCAL_EXIT_GATEWAY=%s", gw)
		return gw
	}
	ips, err := net.LookupHost("host.docker.internal")
	if err == nil && len(ips) > 0 {
		log.Printf("[relay] detected Docker gateway: %s", ips[0])
		return ips[0]
	}
	return ""
})

// rewriteLocalAddr replaces the destination host with the Docker gateway
// when running inside Docker. This prevents services on the host from
// seeing connections from their own IP (Docker NAT hairpin issue).
func (n *Node) rewriteLocalAddr(addr string) string {
	gw := localExitGW()
	if gw == "" {
		return addr
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	rewritten := net.JoinHostPort(gw, port)
	log.Printf("[relay] rewriting exit %s → %s", addr, rewritten)
	return rewritten
}

// ErrNotHy2scale indicates the remote is a plain hy2 server without relay protocol.
var ErrNotHy2scale = fmt.Errorf("relay: remote is not hy2scale")

const (
	streamRegister      = "_relay_register_:0"
	streamCtrlS2C       = "_relay_s2c_ctrl_:0"
	streamListPeers     = "_relay_list_peers_:0"
	streamPing          = "_relay_ping_:0"
	streamLatencyReport = "_relay_latency_:0"
	streamViaPrefix     = "_relay_via_"
	streamDataPrefix    = "_relay_data_"
	streamDataSuffix    = ":0"
	streamIPTunPrefix   = "_relay_iptun_"
	// StreamAPI is the well-known relay address the web UI's remote proxy
	// uses to reach a peer's own API server. It bypasses the normal
	// loopback-rewrite logic (which would redirect 127.0.0.1 to the Docker
	// gateway and break this case).
	StreamAPI = "_relay_api_:0"
)

// IsRelayStream returns true if addr is a relay internal stream.
func IsRelayStream(addr string) bool {
	return addr == streamRegister ||
		addr == streamCtrlS2C ||
		addr == streamListPeers ||
		addr == streamPing ||
		addr == streamLatencyReport ||
		addr == StreamAPI ||
		strings.HasPrefix(addr, streamViaPrefix) ||
		strings.HasPrefix(addr, streamDataPrefix) ||
		strings.HasPrefix(addr, streamIPTunPrefix) ||
		strings.HasPrefix(addr, bridgeRebindAddr)
}

// isLocalRelayStream returns true if addr is a relay stream that can be handled
// locally via HandleStream in a reverse-dial context. Excludes transport-level
// streams (register, ctrl, data delivery) that are part of connection setup.
func isLocalRelayStream(addr string) bool {
	if addr == streamRegister || addr == streamCtrlS2C {
		return false
	}
	if strings.HasPrefix(addr, streamDataPrefix) {
		return false
	}
	return IsRelayStream(addr)
}

// PeerInfo describes a connected peer.
type PeerInfo struct {
	Name         string `json:"name"`
	ExitNode     bool   `json:"exit_node"`
	Direction    string `json:"direction"`
	Native       bool   `json:"native"`
	LatencyMs    int    `json:"latency_ms"`
	Version      string `json:"version,omitempty"`
	Incompatible bool   `json:"incompatible,omitempty"`
	Conflict     bool   `json:"conflict,omitempty"`
	TunCapable   bool   `json:"tun_capable,omitempty"`
	// PV is the remote's wire-protocol generation. Higher than ours
	// means the remote may speak frames we can't parse — see
	// ProtocolVersion comment. Forwarded to the topology API so the
	// frontend can hide nested sub-peers from such links and render
	// the direct connection itself as `unsupported`.
	PV          int  `json:"pv,omitempty"`
	Unsupported bool `json:"unsupported,omitempty"`
}

// NodeVersion is the version string sent during peer registration.
// Set by the app package at init time.
var NodeVersion = "1.0.0"

// ProtocolVersion is an integer identifying the inter-node wire format
// generation. Bumped (independently of NodeVersion semver) whenever a
// breaking peer-protocol change ships. Older instances that see a
// remote peer with a HIGHER ProtocolVersion treat it as opaque:
// hide nested sub-peers entirely (they may use frames we can't parse)
// and mark direct peers as unsupported. Newer instances accept any
// older peer and just downgrade behavior locally to the older feature
// set. v1.3.x ships protocol v3 — v1 = pre-1.0, v2 = 1.0–1.2.
const ProtocolVersion = 3

// NodeTunCapable is set by the app layer at startup if this node can handle
// exit-side TUN (has NET_ADMIN + /dev/net/tun).
var NodeTunCapable bool

// MinCompatVersion is the minimum peer version we can work with.
// Peers below this version are marked incompatible: relay blocked, nested disabled.
const MinCompatVersion = "1.3.0"

// isCompatible checks if a peer version meets minimum requirements.
func isCompatible(version string) bool {
	if version == "" || version == "1.0.0" {
		return false // old peers that don't send version
	}
	// Simple semver major.minor comparison
	return version >= MinCompatVersion
}

// peerMeta is extensible metadata exchanged after basic handshake.
// New fields can be added freely — old peers ignore unknown fields.
type peerMeta struct {
	Version    string `json:"v,omitempty"`
	TunCapable bool   `json:"tun,omitempty"` // true if node can handle exit TUN (has NET_ADMIN + /dev/net/tun)
	// PV is the wire-protocol generation. Absent on pre-1.3.x peers
	// (treat as 0 — accept everything for backward compatibility).
	PV int `json:"pv,omitempty"`
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
	failCount atomic.Int32 // consecutive ping failures (reset on success or active traffic)
	// Snapshot of tx/rx at last completed probe; used to spot peers whose
	// probe stream is starved by their data stream (saturated bottleneck
	// link) so we don't disconnect a link that is, by every other metric,
	// healthy and actively transferring.
	lastProbeTx atomic.Uint64
	lastProbeRx atomic.Uint64
	ctx         context.Context // canceled when peer disconnects
	cancel      context.CancelFunc

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

// pickClientIdx returns a specific QUIC client by index.
// Index 0 = primary, 1+ = extra connections.
func (p *peer) pickClientIdx(idx int) hyclient.Client {
	if idx == 0 || len(p.extraConns) == 0 {
		return p.client
	}
	// Build healthy list same as pickClient
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
	if idx >= len(healthy) {
		idx = idx % len(healthy)
	}
	return healthy[idx]
}

// ConnCount returns the total number of active QUIC connections for this peer.
func (p *peer) ConnCount() int {
	if p.client == nil {
		return 0
	}
	return 1 + len(p.extraConns)
}

// AddConn adds or replaces an extra QUIC connection for a secondary IP address.
func (p *peer) AddConn(client hyclient.Client, addr, status string) {
	// Replace existing entry with same addr
	for i, a := range p.connAddrs {
		if a == addr {
			if p.extraConns[i] != nil {
				p.extraConns[i].Close()
			}
			p.extraConns[i] = client
			p.connStatuses[i] = status
			return
		}
	}
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

	blockedMu sync.RWMutex
	blocked   map[string]bool

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

	// Per-qualified-path byte counters, keyed by "p1/p2/.../pN" — the same
	// format cfg.Peers uses. Populated by DialVia / DialViaBridged whenever
	// the outgoing chain has more than one hop, and also whenever handleVia
	// continues a chain on behalf of a remote. Lets the UI attribute
	// relayed traffic to the specific nested descendant it's destined for,
	// not just to the first-hop direct peer.
	pathBytesMu sync.RWMutex
	pathBytes   map[string]*pathCounters
	pathRateMu  sync.RWMutex
	pathRates   map[string]PeerTraffic

	// Per-peer latency (updated by background prober)
	latencyMu      sync.RWMutex
	latencies      map[string]int
	addrLatencies  map[string]map[string]int // peer → addr → ms
	peersOfCache map[string][]PeerInfo // cached PeersOf results from prober
	conflicts    map[string]string    // client addr → conflicting name

	// Injected handler: returns rich peer list JSON (with children) for nested discovery.
	// If nil, falls back to flat n.Peers() JSON.
	listPeersFunc func() []byte

	// IP tunnel handler for TUN-based raw packet forwarding
	ipTunHandler func(peerName string, stream net.Conn)

	// API proxy handler for remote web-UI tunneling. Set by the app layer at
	// startup so the relay can deliver _relay_api_ streams directly to the
	// local HTTP API listener without routing through rewriteLocalAddr.
	apiHandler func(stream net.Conn)

	// Stream bridge manager for connection persistence across QUIC reconnects
	bridges *bridgeManager
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

// pathCounters holds the tx/rx byte totals for a single qualified nested path.
type pathCounters struct {
	tx atomic.Uint64
	rx atomic.Uint64
}

// getOrCreatePathCounters returns the counter record for the given qualified
// path (e.g. "au/au-r1/au-r1-a"), creating it lazily on first use.
func (n *Node) getOrCreatePathCounters(key string) *pathCounters {
	n.pathBytesMu.RLock()
	rec, ok := n.pathBytes[key]
	n.pathBytesMu.RUnlock()
	if ok {
		return rec
	}
	n.pathBytesMu.Lock()
	if rec, ok = n.pathBytes[key]; ok {
		n.pathBytesMu.Unlock()
		return rec
	}
	rec = &pathCounters{}
	n.pathBytes[key] = rec
	n.pathBytesMu.Unlock()
	return rec
}

// PathRates returns per-qualified-path traffic rates, keyed identically to
// cfg.Peers (e.g. "au", "au/au-r1", "au/au-r1/au-r1-a"). Only paths with
// more than one hop are populated — single-hop flows are covered by the
// per-peer counter returned from PeerRates.
func (n *Node) PathRates() map[string]PeerTraffic {
	n.pathRateMu.RLock()
	defer n.pathRateMu.RUnlock()
	out := make(map[string]PeerTraffic, len(n.pathRates))
	for k, v := range n.pathRates {
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

// GetLatency returns stored latency for a peer. For multi-addr peers, returns
// average of all addr latencies. -1 if unknown.
func (n *Node) GetLatency(peerName string) int {
	n.latencyMu.RLock()
	defer n.latencyMu.RUnlock()
	// If per-addr latencies exist, return average
	if al, ok := n.addrLatencies[peerName]; ok && len(al) > 0 {
		sum, count := 0, 0
		for _, ms := range al {
			if ms > 0 {
				sum += ms
				count++
			}
		}
		if count > 0 {
			return sum / count
		}
	}
	if ms, ok := n.latencies[peerName]; ok {
		return ms
	}
	return -1
}

// GetAddrLatencies returns per-address latency for a multi-addr peer.
func (n *Node) GetAddrLatencies(peerName string) map[string]int {
	n.latencyMu.RLock()
	defer n.latencyMu.RUnlock()
	if al, ok := n.addrLatencies[peerName]; ok {
		out := make(map[string]int, len(al))
		for k, v := range al {
			out[k] = v
		}
		return out
	}
	return nil
}

// setAddrLatency stores latency for a specific address of a peer.
func (n *Node) setAddrLatency(peerName, addr string, ms int) {
	n.latencyMu.Lock()
	defer n.latencyMu.Unlock()
	if n.addrLatencies[peerName] == nil {
		n.addrLatencies[peerName] = make(map[string]int)
	}
	n.addrLatencies[peerName][addr] = ms
}

// probeExtraConns pings each extra QUIC connection and stores per-addr latency.
func (n *Node) probeExtraConns(name string, p *peer) {
	if len(p.extraConns) == 0 {
		return
	}
	for i, ec := range p.extraConns {
		if ec == nil {
			continue
		}
		addr := ""
		if i+1 < len(p.connAddrs) {
			addr = p.connAddrs[i+1] // connAddrs[0] is primary, extras start at [1]
		}
		if addr == "" {
			continue
		}
		rtt := n.pingClient(ec)
		if rtt >= 0 {
			n.setAddrLatency(name, addr, int(rtt.Milliseconds()))
			// Update status to online
			if i < len(p.connStatuses) {
				p.connStatuses[i] = "online"
			}
		} else {
			n.setAddrLatency(name, addr, -1)
			if i < len(p.connStatuses) {
				p.connStatuses[i] = "offline"
			}
		}
	}
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
				go func(name string, p *peer) {
					if p.client != nil {
						// Outbound peer: ping + nested discovery
						rtt := n.PingPeer(name)
						if rtt >= 0 {
							n.SetLatency(name, int(rtt.Milliseconds()))
							p.failCount.Store(0)
							// Store per-addr latency for primary
							if len(p.connAddrs) > 0 {
								n.setAddrLatency(name, p.connAddrs[0], int(rtt.Milliseconds()))
							}
						} else {
							n.SetLatency(name, -1)
							// If the peer pushed/pulled bytes since the last probe,
							// the link is alive — the ping just got queued behind
							// data on a saturated QUIC connection (e.g. long upload
							// through a rate-limited downstream hop). Treat as
							// "probe starved by congestion", reset failCount, do
							// not disconnect. Active byte counters are a stronger
							// liveness signal than a stuck control stream.
							curTx := p.txBytes.Load()
							curRx := p.rxBytes.Load()
							prevTx := p.lastProbeTx.Load()
							prevRx := p.lastProbeRx.Load()
							if curTx > prevTx || curRx > prevRx {
								p.failCount.Store(0)
								log.Printf("[relay] %s: ping failed but link active (Δtx=%d Δrx=%d) — congestion, not disconnect",
									name, curTx-prevTx, curRx-prevRx)
							} else {
								fails := p.failCount.Add(1)
								if fails >= 3 && p.cancel != nil {
									log.Printf("[relay] %s: %d consecutive ping failures (no traffic), disconnecting for reconnect", name, fails)
									p.cancel()
								}
							}
							if len(p.connAddrs) > 0 {
								n.setAddrLatency(name, p.connAddrs[0], -1)
							}
						}
						// Snapshot tx/rx for next probe's congestion-vs-dead disambiguation.
						p.lastProbeTx.Store(p.txBytes.Load())
						p.lastProbeRx.Store(p.rxBytes.Load())
						// Probe extra connections for per-addr latency
						n.probeExtraConns(name, p)
					}
					// Cache nested sub-peers for both outbound AND inbound peers
					if n.IsNestedEnabled(name) {
						if subPeers, err := n.PeersOf(name); err == nil {
							n.peerRateMu.Lock()
							n.peersOfCache[name] = subPeers
							n.peerRateMu.Unlock()
						}
					} else {
						// Nested disabled: remove stale cache
						n.peerRateMu.Lock()
						delete(n.peersOfCache, name)
						n.peerRateMu.Unlock()
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
	prevPathTx := make(map[string]uint64)
	prevPathRx := make(map[string]uint64)
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

			// Per-qualified-path rates
			pathRates := make(map[string]PeerTraffic)
			n.pathBytesMu.RLock()
			for key, rec := range n.pathBytes {
				ptx := rec.tx.Load()
				prx := rec.rx.Load()
				pathRates[key] = PeerTraffic{
					Name:   key,
					TxRate: ptx - prevPathTx[key],
					RxRate: prx - prevPathRx[key],
				}
				prevPathTx[key] = ptx
				prevPathRx[key] = prx
			}
			n.pathBytesMu.RUnlock()
			n.pathRateMu.Lock()
			n.pathRates = pathRates
			n.pathRateMu.Unlock()
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
		blocked:   make(map[string]bool),
		peerRates: make(map[string]PeerTraffic),
		pathBytes: make(map[string]*pathCounters),
		pathRates: make(map[string]PeerTraffic),
		latencies:     make(map[string]int),
		addrLatencies: make(map[string]map[string]int),
		peersOfCache: make(map[string][]PeerInfo),
		bridges:   newBridgeManager(),
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
		ctx:     childCtx,
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
	writeMeta(regStream, peerMeta{Version: NodeVersion, TunCapable: NodeTunCapable, PV: ProtocolVersion})

	if onID != nil {
		onID(actualName)
	}
	// Check if onID flagged a conflict — abort registration to avoid overwriting real peer
	n.mu.RLock()
	hasConflict := false
	for _, cname := range n.conflicts {
		if cname == actualName {
			hasConflict = true
			break
		}
	}
	n.mu.RUnlock()
	if hasConflict {
		return fmt.Errorf("relay: peer %s has name conflict (claims %q)", peerName, actualName)
	}

	// Open s2c ctrl for dial requests from remote
	s2cCtrl, err := client.TCP(streamCtrlS2C)
	if err != nil {
		return fmt.Errorf("relay: s2c ctrl: %w", err)
	}
	defer s2cCtrl.Close()
	writeString(s2cCtrl, n.name)

	// Register peer with remote's actual name
	compat := isCompatible(remoteMeta.Version)
	if !compat {
		log.Printf("[%s] peer %s version %s is incompatible (min %s)", n.name, actualName, remoteMeta.Version, MinCompatVersion)
	}
	unsupported := remoteMeta.PV > ProtocolVersion
	if unsupported {
		log.Printf("[%s] peer %s wire protocol v%d is newer than ours (v%d) — direct only, sub-peers hidden", n.name, actualName, remoteMeta.PV, ProtocolVersion)
	}
	childCtx, childCancel := context.WithCancel(ctx)
	p := &peer{
		info:    PeerInfo{Name: actualName, Direction: "outbound", Version: remoteMeta.Version, Incompatible: !compat, TunCapable: remoteMeta.TunCapable, PV: remoteMeta.PV, Unsupported: unsupported},
		client:  client,
		waiting: make(map[string]chan net.Conn),
		ctx:     childCtx,
		cancel:  childCancel,
	}
	n.mu.Lock()
	oldPeer, hadOld := n.peers[actualName]
	// Carry over extra IP connections from old peer (they have independent QUIC clients)
	if hadOld && len(oldPeer.extraConns) > 0 {
		p.extraConns = oldPeer.extraConns
		p.connAddrs = oldPeer.connAddrs
		p.connStatuses = oldPeer.connStatuses
	}
	n.peers[actualName] = p
	n.mu.Unlock()
	// Close old PRIMARY QUIC client to force-terminate stale streams.
	// Extra connections are preserved above.
	if hadOld {
		if oldPeer.cancel != nil {
			oldPeer.cancel()
		}
		if oldPeer.client != nil {
			oldPeer.client.Close()
		}
	}
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

	case streamPing:
		stream.Write([]byte{1})
		stream.Close()

	case streamListPeers:
		n.handleListPeers(stream)

	case StreamAPI:
		if n.apiHandler != nil {
			n.apiHandler(stream)
		} else {
			stream.Close()
		}

	default:
		// IP tunnel stream for TUN-based raw packet forwarding
		if strings.HasPrefix(reqAddr, streamIPTunPrefix) {
			peerName := reqAddr[len(streamIPTunPrefix):]
			if n.ipTunHandler != nil {
				n.ipTunHandler(peerName, stream)
			} else {
				stream.Close()
			}
			return
		}
		if nodeName, targetAddr, ok := parseVia(reqAddr); ok {
			n.handleVia(ctx, nodeName, targetAddr, stream)
			return
		}
		if strings.HasPrefix(reqAddr, streamDataPrefix) {
			id := strings.TrimSuffix(reqAddr[len(streamDataPrefix):], streamDataSuffix)
			n.deliverDataStream(id, stream)
			return
		}
		// Stream rebind: reconnect a suspended bridge to a new QUIC stream
		if strings.HasPrefix(reqAddr, bridgeRebindAddr) {
			bridgeID := strings.TrimSuffix(reqAddr[len(bridgeRebindAddr):], ":0")
			if n.bridges.Rebind(bridgeID, stream) {
				log.Printf("[bridge] rebind accepted: %s", bridgeID)
			} else {
				log.Printf("[bridge] rebind rejected: %s (not found or not suspended)", bridgeID)
				stream.Close()
			}
			return
		}
	}
}

func (n *Node) handleRegister(ctx context.Context, stream net.Conn) {
	var flags [1]byte
	if _, err := io.ReadFull(stream, flags[:]); err != nil {
		log.Printf("[%s] register: read flags failed: %v", n.name, err)
		stream.Close()
		return
	}
	name, err := readString(stream)
	if err != nil {
		log.Printf("[%s] register: read name failed: %v", n.name, err)
		stream.Close()
		return
	}
	log.Printf("[%s] register: inbound %s (flags=0x%02x)", n.name, name, flags[0])

	// Send back our name
	writeString(stream, n.name)

	// Metadata exchange: only if client set the metadata flag (bit 1)
	var remoteMeta peerMeta
	if flags[0]&0x02 != 0 {
		// Send our metadata, then read client's
		writeMeta(stream, peerMeta{Version: NodeVersion, TunCapable: NodeTunCapable, PV: ProtocolVersion})
		remoteMeta = readMeta(stream, 2*time.Second)
	}
	if remoteMeta.Version == "" {
		remoteMeta.Version = "1.0.0" // old peers don't send version
	}

	// Reject blocked peers (disabled nodes)
	if n.IsPeerBlocked(name) {
		log.Printf("[%s] register: %s is blocked, rejecting", n.name, name)
		stream.Close()
		return
	}

	compat := isCompatible(remoteMeta.Version)
	if !compat {
		log.Printf("[%s] inbound peer %s version %s is incompatible (min %s)", n.name, name, remoteMeta.Version, MinCompatVersion)
	}
	unsupported := remoteMeta.PV > ProtocolVersion
	if unsupported {
		log.Printf("[%s] inbound peer %s wire protocol v%d is newer than ours (v%d) — direct only, sub-peers hidden", n.name, name, remoteMeta.PV, ProtocolVersion)
	}
	peerCtx, peerCancel := context.WithCancel(ctx)
	p := &peer{
		info: PeerInfo{
			Name:         name,
			ExitNode:     flags[0]&0x01 != 0,
			Direction:    "inbound",
			Version:      remoteMeta.Version,
			Incompatible: !compat,
			TunCapable:   remoteMeta.TunCapable,
			PV:           remoteMeta.PV,
			Unsupported:  unsupported,
		},
		waiting: make(map[string]chan net.Conn),
		ctx:     peerCtx,
		cancel:  peerCancel,
	}

	n.mu.Lock()
	existing, exists := n.peers[name]
	if exists && existing.info.Direction == "inbound" {
		// An inbound register is always initiated over a FRESH QUIC connection
		// on the remote side — so receiving a new one means the old ctrl
		// stream is dead (either QUIC timed out on the remote or the remote
		// explicitly reconnected). Always replace; never skip. Skipping would
		// leave the dead ctrl in place and lock out the peer for the entire
		// QUIC idle-timeout window (observed: kbv stuck in a 30min register
		// rejection loop on AUB because the old inbound ctrl was dead but
		// n.peers[kbv].ctrlW was still non-nil).
		log.Printf("[%s] register: %s re-registering (replacing stale inbound ctrl, ver %s→%s, tun %v→%v)",
			n.name, name, existing.info.Version, remoteMeta.Version,
			existing.info.TunCapable, remoteMeta.TunCapable)
		// fall through to the replacement branch below
	}
	if exists {
		log.Printf("[%s] register: %s replacing existing peer (dir=%s, hasCtrl=%v)", n.name, name, existing.info.Direction, existing.ctrlW != nil)
	}
	// Cancel old peer context to clean up stale connections
	if exists && existing.cancel != nil {
		existing.cancel()
	}
	n.peers[name] = p
	n.mu.Unlock()

	// Watchdog: tie the peer entry's lifetime to the register stream.
	//
	// Before this watchdog, `ctx` passed in from hy2's Outbound.TCP was
	// the server-lifetime context — it never cancelled on peer disconnect,
	// so this goroutine would block on <-peerCtx.Done() forever and the
	// n.peers[name] entry would persist until either (a) the same peer
	// re-registered (existing.cancel() above replaces it) or (b) the node
	// restarted. Result: ghost inbound peers (latency=-1, conn=false) in
	// topology, routes referencing them that hang in 30s dialExit retry
	// loops, and masked capacity/health decisions elsewhere.
	//
	// The register stream is held open for the lifetime of the peer's
	// QUIC connection. When the remote disconnects (clean shutdown, QUIC
	// idle timeout, health-check kill, or crash) the stream closes; a
	// blocking Read returns an error, which is our signal to cancel
	// peerCtx and remove n.peers[name].
	go func() {
		var buf [1]byte
		_, _ = stream.Read(buf[:])
		peerCancel()
	}()

	<-peerCtx.Done()
	stream.Close() // defensive — may already be closed by the remote or by existing.cancel()

	n.mu.Lock()
	// Only delete if we're still the current entry (not replaced by another registration)
	if current, ok := n.peers[name]; ok && current == p {
		delete(n.peers, name)
	}
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
	if n.listPeersFunc != nil {
		stream.Write(n.listPeersFunc())
		return
	}
	peers := n.Peers()
	data, _ := json.Marshal(peers)
	stream.Write(data)
}

func (n *Node) handleVia(ctx context.Context, peerName, targetAddr string, stream net.Conn) {
	// Parse bridge tag from target address for rebind support
	actualAddr, bridgeID, isBridged := ParseBridgeAddr(targetAddr)

	// Check if this is a rebind to an existing via bridge
	if isBridged {
		log.Printf("[via] checking rebind %s for peer %s", bridgeID, peerName)
		if rebindConn := n.bridges.TryRebind(bridgeID); rebindConn != nil {
			log.Printf("[via] rebind accepted: %s (peer %s)", bridgeID, peerName)
			defer rebindConn.Close()
			defer stream.Close()
			go func() { io.Copy(rebindConn, stream); rebindConn.Close() }()
			io.Copy(stream, rebindConn)
			return
		}
		log.Printf("[via] rebind failed: %s (not found or not suspended)", bridgeID)
	}

	// New connection: dial the next hop
	var exitConn net.Conn
	var err error
	if parts := strings.Split(peerName, "/"); len(parts) > 1 {
		exitConn, err = n.DialVia(ctx, parts, actualAddr)
	} else {
		exitConn, _, err = n.DialTCPBridged(peerName, actualAddr)
		if err != nil {
			exitConn, err = n.DialTCP(ctx, peerName, actualAddr)
		}
	}
	if err != nil {
		stream.Close()
		return
	}

	if isBridged {
		pipeConn := n.bridges.CreateWithID(bridgeID, actualAddr, exitConn)
		defer pipeConn.Close()
		defer stream.Close()
		done := make(chan struct{})
		go func() { relayCopy(pipeConn, stream); pipeConn.Close(); close(done) }()
		relayCopy(stream, pipeConn)
		// Main exit-side copy returned (pipeConn EOF/err). Force-close
		// stream + pipeConn so the inner goroutine blocked on
		// stream.Read unblocks; otherwise <-done below hangs forever
		// and deferred closes never fire. Observed in production as
		// hundreds of stuck handleVia.bridged sessions on flappy
		// CN↔AU links where the next-hop dies while the peer stream
		// stays alive under QUIC keepalive.
		stream.Close()
		pipeConn.Close()
		<-done
	} else {
		defer exitConn.Close()
		defer stream.Close()
		done := make(chan struct{})
		go func() { relayCopy(exitConn, stream); close(done) }()
		relayCopy(stream, exitConn)
		// Same race as the bridged branch above.
		stream.Close()
		exitConn.Close()
		<-done
	}
}

// relayCopy is an optimized io.Copy for relay hops with a smaller buffer
// to minimize latency for interactive traffic. 8KB is enough for most packets
// while avoiding the 32KB default that delays small writes.
func relayCopy(dst, src net.Conn) {
	buf := make([]byte, 8192)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}
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
	// Block requests from incompatible peers
	n.mu.RLock()
	if p, ok := n.peers[peerName]; ok && n.isPeerBlocked(p) {
		n.mu.RUnlock()
		return
	}
	n.mu.RUnlock()

	// UDP dial request: addr starts with "udp:"
	if strings.HasPrefix(addr, "udp:") {
		n.dialUDPAndStream(ctx, peerName, client, id, addr[4:])
		return
	}

	// Relay internal stream: handle locally via HandleStream — the same dispatch
	// used for direct inbound QUIC streams. Any future relay stream added to
	// HandleStream automatically works for both outbound and inbound peers.
	// Excludes transport-level streams (register, ctrl, data delivery).
	if isLocalRelayStream(addr) {
		stream, err := client.TCP(streamDataPrefix + id + streamDataSuffix)
		if err != nil {
			return
		}
		n.HandleStream(ctx, addr, stream)
		return
	}

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

	// Create a bridge to enable stream rebinding.
	// If the QUIC stream dies (peer reconnects), the TCP connection to the
	// destination stays alive and waits for a rebind from the requester.
	bridge := n.bridges.Create(peerName, addr, stream)
	bridge.RunRelay(target, n.bridges)
}

// dialUDPAndStream handles a remote UDP dial request.
// Dials UDP to addr, then relays datagrams over a QUIC stream with 2-byte length framing.
func (n *Node) dialUDPAndStream(ctx context.Context, peerName string, client hyclient.Client, id, addr string) {
	udpConn, err := net.DialTimeout("udp", addr, 5*time.Second)
	if err != nil {
		return
	}
	defer udpConn.Close()

	stream, err := client.TCP(streamDataPrefix + id + streamDataSuffix)
	if err != nil {
		return
	}
	defer stream.Close()

	n.conns.Add(1)
	defer n.conns.Add(-1)

	// stream → UDP: read framed datagrams from stream, send to UDP
	go func() {
		defer udpConn.Close()
		defer stream.Close()
		var lenBuf [2]byte
		for {
			if _, err := io.ReadFull(stream, lenBuf[:]); err != nil {
				return
			}
			dlen := int(binary.BigEndian.Uint16(lenBuf[:]))
			buf := make([]byte, dlen)
			if _, err := io.ReadFull(stream, buf); err != nil {
				return
			}
			udpConn.Write(buf)
		}
	}()

	// UDP → stream: read datagrams from UDP, write framed to stream
	buf := make([]byte, 65535)
	for {
		udpConn.SetReadDeadline(time.Now().Add(30 * time.Second))
		n, err := udpConn.Read(buf)
		if err != nil {
			return
		}
		var lenBuf [2]byte
		binary.BigEndian.PutUint16(lenBuf[:], uint16(n))
		if _, err := stream.Write(lenBuf[:]); err != nil {
			return
		}
		if _, err := stream.Write(buf[:n]); err != nil {
			return
		}
	}
}

// --- Peer queries ---

// Peers returns directly connected peers.
func (n *Node) Peers() []PeerInfo {
	n.mu.RLock()
	defer n.mu.RUnlock()
	n.latencyMu.RLock()
	defer n.latencyMu.RUnlock()
	result := make([]PeerInfo, 0, len(n.peers))
	// Build set of conflicting peer names
	conflictNames := make(map[string]bool)
	for _, name := range n.conflicts {
		conflictNames[name] = true
	}
	for name, p := range n.peers {
		info := p.info
		if ms, ok := n.latencies[name]; ok {
			info.LatencyMs = ms
		}
		if conflictNames[name] {
			info.Conflict = true
		}
		result = append(result, info)
	}
	return result
}

// PeersOfRaw returns the raw JSON from a peer's streamListPeers response.
// Used by the API server to get rich tree data (with children).
func (n *Node) PeersOfRaw(peerName string) ([]byte, error) {
	n.mu.RLock()
	p, ok := n.peers[peerName]
	n.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("relay: peer %q not connected", peerName)
	}

	type result struct {
		data []byte
		err  error
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
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, cerr := n.DialTCP(ctx, peerName, streamListPeers)
			if cerr != nil {
				ch <- result{nil, cerr}
				return
			}
			defer conn.Close()
			data, err = io.ReadAll(conn)
		}
		if err != nil {
			ch <- result{nil, err}
			return
		}
		ch <- result{data, nil}
	}()
	select {
	case r := <-ch:
		return r.data, r.err
	case <-time.After(3 * time.Second):
		return nil, fmt.Errorf("relay: peer %q query timeout", peerName)
	}
}

// PeersOf returns a peer's peers (flat list). Used by the prober for cache.
func (n *Node) PeersOf(peerName string) ([]PeerInfo, error) {
	// Return empty for incompatible peers (no nested discovery)
	n.mu.RLock()
	if p, ok := n.peers[peerName]; ok && n.isPeerBlocked(p) {
		n.mu.RUnlock()
		return nil, nil
	}
	n.mu.RUnlock()
	data, err := n.PeersOfRaw(peerName)
	if err != nil {
		return nil, err
	}
	var peers []PeerInfo
	if err := json.Unmarshal(data, &peers); err != nil {
		return nil, err
	}
	return peers, nil
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

// DisconnectPeer forcibly disconnects a peer (both inbound and outbound).
// Closes the QUIC client to kill all active streams through this peer,
// ensuring no connections hang in a black hole after reconnection.
func (n *Node) DisconnectPeer(name string) {
	n.mu.Lock()
	p, ok := n.peers[name]
	if ok {
		if p.cancel != nil {
			p.cancel()
		}
		// Close QUIC client(s) to force-terminate all active streams.
		// Without this, streams on the dead QUIC connection hang forever
		// because quic-go's Read() doesn't return on Close().
		if p.client != nil {
			p.client.Close()
		}
		for _, c := range p.extraConns {
			if c != nil {
				c.Close()
			}
		}
	}
	delete(n.peers, name)
	n.mu.Unlock()
}

// IsPeerTunCapable checks if a peer supports exit-side TUN.
// For multi-hop paths ("A/B/C") it walks each segment: first hop must be a
// direct peer, then each subsequent segment is looked up in the sub-peer
// cache under the qualified parent path ("A", then "A/B", ...). This
// disambiguates cases where a nested peer shares a name with a direct peer
// of the local node (e.g. HUB has a direct "2400" AND another "2400" exists
// as a child of some intermediate peer).
func (n *Node) IsPeerTunCapable(peerName string) bool {
	parts := strings.Split(peerName, "/")
	// Strip leading self name (e.g. "AUB/tz-cm-temp" on node AUB → ["tz-cm-temp"])
	if len(parts) > 0 && parts[0] == n.name {
		parts = parts[1:]
	}
	if len(parts) == 0 {
		return NodeTunCapable
	}

	// First hop must be a direct peer.
	firstHop := parts[0]
	n.mu.RLock()
	p, ok := n.peers[firstHop]
	n.mu.RUnlock()
	if !ok {
		return false // first hop not connected yet
	}
	if len(parts) == 1 {
		return p.info.TunCapable
	}

	// Multi-hop: walk children via qualified-path cache.
	n.peerRateMu.RLock()
	defer n.peerRateMu.RUnlock()

	path := firstHop
	for i := 1; i < len(parts); i++ {
		seg := parts[i]
		children, ok := n.peersOfCache[path]
		if !ok {
			// No cache yet under this exact qualified path — fall back to the
			// bare-name cache of the immediate parent as a best-effort (may be
			// ambiguous but still better than returning false blindly).
			parentParts := strings.Split(path, "/")
			bareParent := parentParts[len(parentParts)-1]
			children, ok = n.peersOfCache[bareParent]
			if !ok {
				return false
			}
		}
		var found *PeerInfo
		for j := range children {
			if children[j].Name == seg {
				c := children[j]
				found = &c
				break
			}
		}
		if found == nil {
			return false
		}
		if i == len(parts)-1 {
			return found.TunCapable
		}
		path = path + "/" + seg
	}
	return false
}

// BlockPeer prevents a peer from registering (inbound connections rejected).
func (n *Node) BlockPeer(name string) {
	n.blockedMu.Lock()
	n.blocked[name] = true
	n.blockedMu.Unlock()
}

// UnblockPeer allows a peer to register again.
func (n *Node) UnblockPeer(name string) {
	n.blockedMu.Lock()
	delete(n.blocked, name)
	n.blockedMu.Unlock()
}

// IsPeerBlocked checks if a peer is blocked.
func (n *Node) IsPeerBlocked(name string) bool {
	n.blockedMu.RLock()
	defer n.blockedMu.RUnlock()
	return n.blocked[name]
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
	Addr      string `json:"addr"`
	Status    string `json:"status"` // "online", "offline", "mismatch", "native"
	LatencyMs int    `json:"latency_ms,omitempty"`
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
	if len(p.connAddrs) > 1 {
		for i, addr := range p.connAddrs[1:] {
			status := "online"
			if i < len(p.connStatuses) {
				status = p.connStatuses[i]
			}
			result = append(result, IPStatus{Addr: addr, Status: status})
		}
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
		// Try lightweight ping first, fall back to listPeers for compat with <1.3
		rtt := pingStream(p.pickClient(), streamPing)
		if rtt < 0 {
			rtt = pingStream(p.pickClient(), streamListPeers)
		}
		ch <- rtt
	}()
	select {
	case d := <-ch:
		return d
	case <-time.After(2 * time.Second):
		return -1
	}
}

// pingStream opens a stream, reads 1 byte, and returns the round-trip duration.
func pingStream(c hyclient.Client, addr string) time.Duration {
	if c == nil {
		return -1
	}
	start := time.Now()
	stream, err := c.TCP(addr)
	if err != nil {
		return -1
	}
	buf := make([]byte, 1)
	stream.Read(buf)
	stream.Close()
	return time.Since(start)
}

// pingClient measures RTT to a specific QUIC client connection. Returns -1 if unreachable.
func (n *Node) pingClient(c hyclient.Client) time.Duration {
	if c == nil {
		return -1
	}
	ch := make(chan time.Duration, 1)
	go func() {
		rtt := pingStream(c, streamPing)
		if rtt < 0 {
			rtt = pingStream(c, streamListPeers)
		}
		ch <- rtt
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
	// Optional counter sets, one per qualified nested-path PREFIX with
	// ≥2 hops. For a stream dialed through "au/au-r1/au-r1-a" we update
	// the counters for both "au/au-r1" AND "au/au-r1/au-r1-a" — so the
	// graph view's per-hop realtime-speed badges all increment together
	// instead of only the final-hop counter.
	pathTxs, pathRxs []*atomic.Uint64
	conns            *atomic.Int64
	closed           atomic.Bool
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
		add := uint64(n)
		c.rx.Add(add)
		if c.peerRx != nil {
			c.peerRx.Add(add)
		}
		for _, p := range c.pathRxs {
			p.Add(add)
		}
	}
	return n, err
}

func (c *countedConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 {
		add := uint64(n)
		c.tx.Add(add)
		if c.peerTx != nil {
			c.peerTx.Add(add)
		}
		for _, p := range c.pathTxs {
			p.Add(add)
		}
	}
	return n, err
}

func (n *Node) wrapConn(peerName string, conn net.Conn) net.Conn {
	return n.wrapConnPath(peerName, "", conn)
}

// wrapConnPath wraps an outbound stream for byte accounting. `peerName` is
// always the direct (first-hop) peer. `pathKey`, when non-empty, is the full
// qualified chain (e.g. "au/au-r1-a") — used by the Nodes-page UI to show
// traffic attributed to the specific nested descendant rather than only to
// the first-hop peer. Pass "" for single-hop dials.
func (n *Node) wrapConnPath(peerName, pathKey string, conn net.Conn) net.Conn {
	n.mu.RLock()
	p := n.peers[peerName]
	n.mu.RUnlock()
	cc := &countedConn{Conn: conn, tx: &n.txBytes, rx: &n.rxBytes, conns: &n.conns}
	if p != nil {
		cc.peerTx = &p.txBytes
		cc.peerRx = &p.rxBytes
	}
	// Attach a counter for every nested-path PREFIX of length ≥ 2 along
	// the qualified path. The single-hop direct-peer prefix is already
	// covered by peerTx/peerRx above, so we start prefix accumulation
	// from the 2-hop slice. e.g. "au/au-r1/au-r1-a" → counters for both
	// "au/au-r1" and "au/au-r1/au-r1-a", so the graph's realtime-speed
	// badges on every intermediate hop animate together.
	if pathKey != "" {
		segs := strings.Split(pathKey, "/")
		for i := 2; i <= len(segs); i++ {
			rec := n.getOrCreatePathCounters(strings.Join(segs[:i], "/"))
			cc.pathTxs = append(cc.pathTxs, &rec.tx)
			cc.pathRxs = append(cc.pathRxs, &rec.rx)
		}
	}
	n.conns.Add(1)
	return cc
}

// --- Dial ---

// DialTCP dials addr through a directly connected peer's network.
// DialUDP opens a UDP session through a peer's Hysteria2 QUIC connection.
// Returns a net.Conn-compatible wrapper for a single destination address.
func (n *Node) DialUDP(ctx context.Context, peerName string, addr string) (net.Conn, error) {
	n.mu.RLock()
	p, ok := n.peers[peerName]
	n.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("relay: peer %q not connected", peerName)
	}

	// Outbound peer: use hy2 client's native UDP
	if p.client != nil {
		cl := p.pickClient()
		uc, err := cl.UDP()
		if err != nil {
			return nil, fmt.Errorf("relay: UDP session: %w", err)
		}
		return &hyUDPConnWrapper{uc: uc, addr: addr}, nil
	}

	// Inbound peer: request UDP dial via control stream (same as TCP but with "udp:" prefix)
	if p.ctrlW == nil {
		return nil, fmt.Errorf("relay: peer %q control not ready", peerName)
	}

	id := fmt.Sprintf("%d", n.seq.Add(1))
	ch := make(chan net.Conn, 1)
	p.writeMu.Lock()
	p.waiting[id] = ch
	err := writeRequest(p.ctrlW, id, "udp:"+addr)
	p.writeMu.Unlock()
	if err != nil {
		return nil, err
	}

	select {
	case conn := <-ch:
		if conn == nil {
			return nil, fmt.Errorf("relay: UDP dial via %q failed", peerName)
		}
		// Wrap the QUIC stream with datagram framing for UDP
		return &streamUDPConn{stream: conn}, nil
	case <-ctx.Done():
		p.writeMu.Lock()
		delete(p.waiting, id)
		p.writeMu.Unlock()
		return nil, ctx.Err()
	}
}

// streamUDPConn wraps a QUIC stream with 2-byte length framing for UDP datagrams.
type streamUDPConn struct {
	stream net.Conn
}

func (c *streamUDPConn) Read(b []byte) (int, error) {
	var lenBuf [2]byte
	if _, err := io.ReadFull(c.stream, lenBuf[:]); err != nil {
		return 0, err
	}
	dlen := int(binary.BigEndian.Uint16(lenBuf[:]))
	if dlen > len(b) {
		dlen = len(b)
	}
	return io.ReadFull(c.stream, b[:dlen])
}

func (c *streamUDPConn) Write(b []byte) (int, error) {
	var lenBuf [2]byte
	binary.BigEndian.PutUint16(lenBuf[:], uint16(len(b)))
	if _, err := c.stream.Write(lenBuf[:]); err != nil {
		return 0, err
	}
	return c.stream.Write(b)
}

func (c *streamUDPConn) Close() error                       { return c.stream.Close() }
func (c *streamUDPConn) LocalAddr() net.Addr                { return c.stream.LocalAddr() }
func (c *streamUDPConn) RemoteAddr() net.Addr               { return c.stream.RemoteAddr() }
func (c *streamUDPConn) SetDeadline(t time.Time) error      { return c.stream.SetDeadline(t) }
func (c *streamUDPConn) SetReadDeadline(t time.Time) error  { return c.stream.SetReadDeadline(t) }
func (c *streamUDPConn) SetWriteDeadline(t time.Time) error { return c.stream.SetWriteDeadline(t) }

// hyUDPConnWrapper adapts HyUDPConn to net.Conn for a single destination.
type hyUDPConnWrapper struct {
	uc   interface{ Send([]byte, string) error; Receive() ([]byte, string, error); Close() error }
	addr string
}

func (w *hyUDPConnWrapper) Read(b []byte) (int, error) {
	data, _, err := w.uc.Receive()
	if err != nil {
		return 0, err
	}
	n := copy(b, data)
	return n, nil
}

func (w *hyUDPConnWrapper) Write(b []byte) (int, error) {
	if err := w.uc.Send(b, w.addr); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (w *hyUDPConnWrapper) Close() error                       { return w.uc.Close() }
func (w *hyUDPConnWrapper) LocalAddr() net.Addr                { return nil }
func (w *hyUDPConnWrapper) RemoteAddr() net.Addr               { return nil }
func (w *hyUDPConnWrapper) SetDeadline(t time.Time) error      { return nil }
func (w *hyUDPConnWrapper) SetReadDeadline(t time.Time) error  { return nil }
func (w *hyUDPConnWrapper) SetWriteDeadline(t time.Time) error { return nil }

// PeerCtx returns the context for a peer, canceled when the peer disconnects.
// Used by idle timeout to detect dead relay streams immediately.
// Bridges returns the bridge manager for stream rebinding.
func (n *Node) Bridges() *bridgeManager { return n.bridges }

// isPeerBlocked checks if a peer should be blocked from relay.
// Must be called with n.mu held (at least RLock).
func (n *Node) isPeerBlocked(p *peer) bool {
	if p.info.Incompatible {
		return true
	}
	// Check name conflict
	for _, conflictName := range n.conflicts {
		if conflictName == p.info.Name {
			return true
		}
	}
	return false
}

// SetPeerConflict records a name conflict for a client address.
// The conflict is tracked separately from the peer entry to avoid
// marking legitimate peers as conflicting.
func (n *Node) SetPeerConflict(clientAddr string, conflictName string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.conflicts == nil {
		n.conflicts = make(map[string]string)
	}
	if conflictName == "" {
		delete(n.conflicts, clientAddr)
	} else {
		n.conflicts[clientAddr] = conflictName
	}
}

// PeerConflicts returns a map of client addr → conflicting name.
func (n *Node) PeerConflicts() map[string]string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	if len(n.conflicts) == 0 {
		return nil
	}
	result := make(map[string]string, len(n.conflicts))
	for k, v := range n.conflicts {
		result[k] = v
	}
	return result
}

func (n *Node) PeerCtx(peerName string) context.Context {
	n.mu.RLock()
	p, ok := n.peers[peerName]
	n.mu.RUnlock()
	if !ok || p.ctx == nil {
		return context.Background()
	}
	return p.ctx
}

// DialTCPIdx dials TCP through a specific QUIC client index (for bond path pinning).
// Index -1 means round-robin (same as DialTCP).
func (n *Node) DialTCPIdx(ctx context.Context, peerName string, addr string, clientIdx int) (net.Conn, error) {
	n.mu.RLock()
	p, ok := n.peers[peerName]
	n.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("relay: peer %q not connected", peerName)
	}
	if n.isPeerBlocked(p) {
		return nil, fmt.Errorf("relay: peer %q is blocked (incompatible or conflict)", peerName)
	}
	if p.client != nil {
		var cl hyclient.Client
		if clientIdx >= 0 {
			cl = p.pickClientIdx(clientIdx)
		} else {
			cl = p.pickClient()
		}
		conn, err := cl.TCP(addr)
		if err != nil {
			return nil, err
		}
		return n.wrapConn(peerName, conn), nil
	}
	return n.DialTCP(ctx, peerName, addr)
}

func (n *Node) DialTCP(ctx context.Context, peerName string, addr string) (net.Conn, error) {
	n.mu.RLock()
	p, ok := n.peers[peerName]
	n.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("relay: peer %q not connected", peerName)
	}
	if n.isPeerBlocked(p) {
		return nil, fmt.Errorf("relay: peer %q is blocked (incompatible or conflict)", peerName)
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

// SetListPeersFunc registers a function that returns rich peer list JSON (with children).
// Used by handleListPeers so both outbound and inbound nested discovery return identical data.
func (n *Node) SetListPeersFunc(fn func() []byte) {
	n.listPeersFunc = fn
}

// SetIPTunHandler registers a handler for incoming IP tunnel streams from peers.
func (n *Node) SetIPTunHandler(handler func(peerName string, stream net.Conn)) {
	n.ipTunHandler = handler
}

// SetAPIHandler registers a handler for incoming _relay_api_ streams (remote
// web-UI proxy tunneling).
func (n *Node) SetAPIHandler(handler func(stream net.Conn)) {
	n.apiHandler = handler
}

// DialIPTun opens a bidirectional IP packet tunnel stream to a peer.
// The stream uses 2-byte length-prefixed framing for raw IP packets.
func (n *Node) DialIPTun(ctx context.Context, peerName string) (net.Conn, error) {
	// Handle path with self-prefix (e.g. "AUB/au-kbv" on AUB → strip to "au-kbv")
	// and multi-hop via paths
	if parts := strings.Split(peerName, "/"); len(parts) > 1 {
		// Strip leading self-name segments
		for len(parts) > 1 && parts[0] == n.name {
			parts = parts[1:]
		}
		if len(parts) > 1 {
			return n.DialVia(ctx, parts, streamIPTunPrefix+n.name)
		}
		peerName = parts[0]
	}

	n.mu.RLock()
	p, ok := n.peers[peerName]
	n.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("relay: peer %q not connected", peerName)
	}

	addr := streamIPTunPrefix + n.name

	if p.client != nil {
		cl := p.pickClient()
		return cl.TCP(addr)
	}

	// Inbound peer: send request via control stream
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
			return nil, fmt.Errorf("relay: IP tunnel to %q failed", peerName)
		}
		return conn, nil
	case <-ctx.Done():
		p.writeMu.Lock()
		delete(p.waiting, id)
		p.writeMu.Unlock()
		return nil, ctx.Err()
	}
}

// DialViaBridged dials addr through a chain of peers with bridge support.
// Returns a bridgedConn that survives first-hop QUIC reconnects.
func (n *Node) DialViaBridged(ctx context.Context, path []string, addr string) (net.Conn, string, error) {
	if len(path) < 2 {
		return nil, "", fmt.Errorf("relay: DialViaBridged requires multi-hop path")
	}
	firstPeer := path[0]
	remaining := strings.Join(path[1:], "/")
	// Tag the target address with bridge ID so the exit node creates a bridge
	bridge := n.bridges.Create(firstPeer, addr, nil)
	taggedAddr := addr + "#bridge=" + bridge.id
	viaAddr := streamViaPrefix + remaining + "_" + taggedAddr + ":0"

	n.mu.RLock()
	p, ok := n.peers[firstPeer]
	n.mu.RUnlock()
	if !ok {
		n.bridges.Remove(bridge.id)
		return nil, "", fmt.Errorf("relay: peer %q not connected", firstPeer)
	}
	if p.info.Incompatible {
		n.bridges.Remove(bridge.id)
		return nil, "", fmt.Errorf("relay: peer %q version %s is incompatible", firstPeer, p.info.Version)
	}
	if p.client == nil {
		n.bridges.Remove(bridge.id)
		return nil, "", fmt.Errorf("relay: peer %q is inbound (no client)", firstPeer)
	}

	cl := p.pickClient()
	stream, err := cl.TCP(viaAddr)
	if err != nil {
		n.bridges.Remove(bridge.id)
		return nil, "", err
	}
	bridge.mu.Lock()
	bridge.relayStream = stream
	bridge.mu.Unlock()

	bc := &bridgedConn{
		bridge:   bridge,
		node:     n,
		peerName: firstPeer,
		stream:   n.wrapConnPath(firstPeer, strings.Join(path, "/"), stream),
		triggers: defaultTriggers(),
	}
	// Store the via address template for rebind
	bc.viaPath = path
	bc.viaTargetAddr = addr
	return bc, bridge.id, nil
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
	// Qualified path key for per-path counters — covers origin dials and
	// bridge sub-dials alike, since handleVia also lands here for multi-
	// hop continuations.
	pathKey := strings.Join(path, "/")

	if p.client != nil {
		cl := p.pickClient()
		conn, err := cl.TCP(viaAddr)
		if err != nil {
			return nil, err
		}
		return n.wrapConnPath(firstPeer, pathKey, conn), nil
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
		return n.wrapConnPath(firstPeer, pathKey, conn), nil
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
