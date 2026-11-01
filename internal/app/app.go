package app

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/FrankoonG/hy2scale/core/audit"
	"github.com/FrankoonG/hy2scale/core/exit"
	"github.com/FrankoonG/hy2scale/core/netfilter"
	relay "github.com/FrankoonG/hy2scale/core/node"
	"github.com/FrankoonG/hy2scale/core/policy"
	"github.com/FrankoonG/hy2scale/core/proto"
	"github.com/FrankoonG/hy2scale/core/server"
	"github.com/FrankoonG/hy2scale/core/sockmark"
	"github.com/FrankoonG/hy2scale/internal/history"
	"github.com/FrankoonG/hy2scale/internal/udphop"
	"github.com/FrankoonG/hy2scale/internal/vpnmux"
	hyclient "github.com/apernet/hysteria/core/v2/client"
	hyserver "github.com/apernet/hysteria/core/v2/server"
)

type ClientEntry struct {
	Name     string   `yaml:"name" json:"name"`
	Addr     string   `yaml:"addr" json:"addr"`           // primary address (backward compat)
	Addrs    []string `yaml:"addrs,omitempty" json:"addrs,omitempty"` // all addresses (including primary)
	Password string   `yaml:"password" json:"password"`

	// TLS
	SNI      string `yaml:"sni,omitempty" json:"sni"`
	Insecure bool   `yaml:"insecure" json:"insecure"`
	CA       string `yaml:"ca,omitempty" json:"ca"` // PEM content or path

	// Bandwidth (bytes/sec)
	MaxTx int `yaml:"max_tx,omitempty" json:"max_tx"`
	MaxRx int `yaml:"max_rx,omitempty" json:"max_rx"`

	// QUIC
	InitStreamWindow int `yaml:"init_stream_window,omitempty" json:"init_stream_window"`
	MaxStreamWindow  int `yaml:"max_stream_window,omitempty" json:"max_stream_window"`
	InitConnWindow   int `yaml:"init_conn_window,omitempty" json:"init_conn_window"`
	MaxConnWindow    int `yaml:"max_conn_window,omitempty" json:"max_conn_window"`

	// Misc
	FastOpen   bool   `yaml:"fast_open,omitempty" json:"fast_open"`
	BBRProfile string `yaml:"bbr_profile,omitempty" json:"bbr_profile,omitempty"` // "", "standard", "conservative", "aggressive"
	Disabled   bool   `yaml:"disabled,omitempty" json:"disabled"`

	// Punch: routes set up by hole punching (see punch.go); Advanced: tuning.
	Punch    *PunchConfig  `yaml:"punch,omitempty" json:"punch,omitempty"`
	Advanced *PeerAdvanced `yaml:"advanced,omitempty" json:"advanced,omitempty"`

	// Access: "" = a full peer (system password); "user" = node access by
	// user, Password being a user's hy2 password on that node (access.go).
	Access string `yaml:"access,omitempty" json:"access,omitempty"`
}

// AllAddrs returns the effective address list. If Addrs is populated, returns it.
// Otherwise falls back to the single Addr field for backward compatibility.
func (c ClientEntry) AllAddrs() []string {
	var out []string
	if len(c.Addrs) > 0 {
		out = append(out, c.Addrs...)
	} else if c.Addr != "" && !IsPunchAddr(c.Addr) {
		out = append(out, c.Addr)
	}
	return append(out, c.punchAddrs()...)
}

// PrimaryAddr returns the first address (used as stable key and default connection).
func (c ClientEntry) PrimaryAddr() string {
	if len(c.Addrs) > 0 {
		return c.Addrs[0]
	}
	return c.Addr
}

type PeerConfig struct {
	Nested   bool `yaml:"nested" json:"nested"`
	Disabled bool `yaml:"disabled,omitempty" json:"disabled,omitempty"`
}

type ServerConfig struct {
	Listen   string `yaml:"listen" json:"listen"`
	Password string `yaml:"password" json:"password"`
	TLSCert  string `yaml:"tls_cert" json:"tls_cert"`
	TLSKey   string `yaml:"tls_key" json:"tls_key"`
}

type SOCKS5Config struct {
	Listen  string `yaml:"listen"`
	ExitVia string `yaml:"exit_via"`
}

type Config struct {
	NodeID     string                `yaml:"node_id" json:"node_id"`
	// Legacy `name` field, retained ONLY for one-version migration: when
	// loading an older config that had `name` divergent from `node_id`,
	// LoadOrInitConfig adopts Name as the new NodeID and clears Name. New
	// configs never write this field (omitempty + always blank after
	// migration). Kept off JSON entirely — the API exposes only node_id.
	Name       string                `yaml:"name,omitempty" json:"-"`
	ExitNode     bool                  `yaml:"exit_node" json:"exit_node"`
	Hy2UserAuth  bool                  `yaml:"hy2_user_auth,omitempty" json:"hy2_user_auth"`
	Server     *ServerConfig         `yaml:"server" json:"server"`
	Clients    []ClientEntry         `yaml:"clients" json:"clients"`
	Peers      map[string]PeerConfig `yaml:"peers" json:"peers"`
	SOCKS5     *SOCKS5Config         `yaml:"socks5,omitempty" json:"-"`
	Users      []UserConfig          `yaml:"users" json:"users"`
	Proxies    []ProxyConfig         `yaml:"proxies" json:"proxies"`
	SS         *SSConfig             `yaml:"ss,omitempty" json:"ss,omitempty"`
	Trojan     *TrojanConfig         `yaml:"trojan,omitempty" json:"trojan,omitempty"`
	L2TP       *L2TPConfig           `yaml:"l2tp,omitempty" json:"l2tp,omitempty"`
	IKEv2      *IKEv2Config          `yaml:"ikev2,omitempty" json:"ikev2,omitempty"`
	WireGuard  *WireGuardConfig      `yaml:"wireguard,omitempty" json:"wireguard,omitempty"`
	OpenVPN    *OpenVPNConfig        `yaml:"openvpn,omitempty" json:"openvpn,omitempty"`
	// Rules: the pre-policy routing rules, read once for migration into
	// Policy (policy.go) and then dropped.
	Rules []RoutingRule `yaml:"rules,omitempty" json:"rules,omitempty"`
	// Policy: condition sets, policy sets, the direct entry and system
	// capture (core/policy; docs/v1.4.0-policy.md).
	Policy *PolicyConfig `yaml:"policy,omitempty" json:"policy,omitempty"`
	// Interfaces: child interfaces on physical NICs; Egress: where direct
	// flows leave (nil = never chosen: the system routes, as before 1.4.0);
	// PublicIP: public address detection. See ifaces.go / egress.go.
	Interfaces []InterfaceConfig     `yaml:"interfaces,omitempty" json:"interfaces,omitempty"`
	Egress     *EgressConfig         `yaml:"egress,omitempty" json:"egress,omitempty"`
	PublicIP   *PublicIPConfig       `yaml:"public_ip,omitempty" json:"public_ip,omitempty"`
	// Storage: logs and audit records kept on disk (storage.go); nil = both off.
	Storage *StorageConfig `yaml:"storage,omitempty" json:"storage,omitempty"`
	// GeoIP: how the GeoIP databases are kept (geoip.go); nil = manual.
	GeoIP *GeoIPConfig `yaml:"geoip,omitempty" json:"geoip,omitempty"`
	UIListen    string                `yaml:"ui_listen,omitempty" json:"ui_listen,omitempty"`
	UIBasePath  string                `yaml:"ui_base_path,omitempty" json:"ui_base_path,omitempty"`
	WebUsername string                `yaml:"web_username,omitempty" json:"web_username,omitempty"`
	WebPassword string                `yaml:"web_password,omitempty" json:"web_password,omitempty"`
	DNS         string                `yaml:"dns,omitempty" json:"dns,omitempty"`
	ForceHTTPS  bool                  `yaml:"force_https,omitempty" json:"force_https,omitempty"` // ignored: the web UI is always https
	HTTPSCertID     string                `yaml:"https_cert_id,omitempty" json:"https_cert_id,omitempty"`
	SessionTimeoutH int                   `yaml:"session_timeout_h,omitempty" json:"session_timeout_h,omitempty"` // hours, default 12
	// PathLossGraceS: quality/aggregate connections survive this many
	// seconds without any working path to their exit, then close.
	// 0 = default (msess.OrphanBudget); clamped to [3, 300].
	PathLossGraceS int `yaml:"path_loss_grace_s,omitempty" json:"path_loss_grace_s,omitempty"`
	// Pre-1.4.0 single shared topology layout (node key → coordinates).
	// Read only for migration into GraphCanvases (see MigrateGraphLayout).
	GraphLayout map[string]GraphLayoutPos `yaml:"graph_layout,omitempty" json:"graph_layout,omitempty"`
	// Custom topology-graph canvases (node positions, edge bends, label
	// positions). Stored server-side so they follow the user across
	// browsers and concurrent sessions converge on one source of truth.
	GraphCanvases []GraphCanvas `yaml:"graph_canvases,omitempty" json:"graph_canvases,omitempty"`
	// Nested-discovery hard limits. Defensive backstops on the deep-cache
	// machinery (see internal/api/server.go walkAndCache /
	// assembleDeepTree). Under a sane config rule 1 cycle drop + rule 2
	// gating already structurally bound the tree shape; these caps only
	// fire under pathological mis-config or hostile peers. Hot-reloadable:
	// updateUISettings refreshes the runtime atomic-pointer so changes
	// take effect within the next SubPeersUpdater tick (≤7 s) without a
	// restart. Zero / missing means "use built-in default".
	MaxNestedDepth   int `yaml:"max_nested_depth,omitempty" json:"max_nested_depth,omitempty"`     // default 5,         hard cap 10
	MaxResponseNodes int `yaml:"max_response_nodes,omitempty" json:"max_response_nodes,omitempty"` // default 1024,      hard cap 65536
	MaxCacheEntries  int `yaml:"max_cache_entries,omitempty" json:"max_cache_entries,omitempty"`   // default 5000,      hard cap 100000
	MaxResponseBytes int `yaml:"max_response_bytes,omitempty" json:"max_response_bytes,omitempty"` // default 1048576,   hard cap 16<<20
	MaxFetchFanOut   int `yaml:"max_fetch_fan_out,omitempty" json:"max_fetch_fan_out,omitempty"`   // default 8,         hard cap 64

	// RelayAdminPassthrough lets web/API requests delivered through the
	// authenticated peer-to-peer relay bridge (apiBridge in
	// internal/api/server.go) skip the local session/Bearer-token check.
	// Off by default — backwards compatible with every prior release where
	// every API route required login regardless of transport. When on, the
	// trust boundary becomes the relay handshake itself: a peer that has
	// already authenticated as a known node is allowed to administer this
	// node's web UI without a separate admin password.
	RelayAdminPassthrough bool `yaml:"relay_admin_passthrough,omitempty" json:"relay_admin_passthrough,omitempty"`

	// AcceptPunch lets nodes that reach this one through the mesh set up a
	// direct (hole-punched) connection to it. Off by default; like relay
	// trust, the mesh handshake is the trust boundary.
	AcceptPunch bool `yaml:"accept_punch,omitempty" json:"accept_punch,omitempty"`

	// DNSResolver routes hy2scale's own internal name lookups (rules-engine
	// `applyDomainRule`, future call sites) through a clean upstream DNS
	// reachable via the relay/exit, so a polluted local resolv.conf does
	// not contaminate which IPs the rule installs in iptables. Disabled by
	// default: hy2scale falls back to net.LookupHost (host's resolv.conf).
	DNSResolver DNSResolverConfig `yaml:"dns_resolver,omitempty" json:"dns_resolver,omitempty"`

	// KnownInboundIDs persists the last NodeID seen at each source IP for
	// inbound peers. Used by the inbound-identity sync path
	// (docs/peer-identity-sync-bugs.md Bug B) to detect when a peer that
	// previously connected from the same IP now reports a different NodeID
	// — the inbound counterpart of the outbound rename signal carried by
	// cfg.Clients[].Addr. Auto-populated on every inbound register; no
	// operator-facing UI. Map key is the remote IP (no port), value is the
	// NodeID the peer announced on its most recent connection from that IP.
	KnownInboundIDs map[string]string `yaml:"known_inbound_ids,omitempty" json:"known_inbound_ids,omitempty"`

	// SharedDeletes: shared users still to delete on target nodes (a target
	// removed, or the user deleted here, while the target was unreachable).
	SharedDeletes []SharedDelete `yaml:"shared_deletes,omitempty" json:"-"`
}

// DNSResolverConfig: see exit.ResolverConfig.
type DNSResolverConfig = exit.ResolverConfig

// GraphLayoutPos stores a single node's coordinates in the topology graph.
type GraphLayoutPos struct {
	X float64 `yaml:"x" json:"x"`
	Y float64 `yaml:"y" json:"y"`
}

type proxyHandle struct {
	listener net.Listener
	cancel   context.CancelFunc
}

type App struct {
	persisters   sync.WaitGroup // shutdown-time flushers (see WaitPersisted)
	store        *ConfigStore
	node         *relay.Node
	exit         *exit.Engine
	pol          *policy.Engine // condition / policy sets (policy.go)
	tree         *relay.Tree // nested discovery
	primaryDial  sync.Map    // client identity (Addr) → address its primary connection dials
	tls          *TLSStore
	dataDir      string
	appCtx       context.Context
	mu           sync.Mutex
	clientCancel map[string]context.CancelFunc

	hist *history.Store

	acc   *accessState

	// Track last successful exit path per primary exit_via (for UI display)
	proxyHandles map[string]*proxyHandle
	srvCancel    context.CancelFunc
	ssListener   net.Listener
	ssCancel     context.CancelFunc
	trojanMu        sync.Mutex // guards trojan
	trojanRestartMu sync.Mutex // serializes RestartTrojan
	trojan          *trojanServer
	serverCert   atomic.Pointer[tls.Certificate] // the hy2 server's certificate (punch listeners reuse it)
	punch        *punchManager
	vpnMu        sync.Mutex // guards vpn and vpnMux (IKEv2 / L2TP / OpenVPN servers)
	vpn          map[string]*vpnInstance
	vpnMux       *vpnmux.Mux
	usersMu       sync.RWMutex
	userIndex     map[string]*UserConfig           // username → user (for fast auth lookup)
	ratesMu       sync.RWMutex
	rates         map[string]*userRate // per-user rate limits (ratelimit.go)
	pwConflicted  map[string]map[string]bool       // username → proxy → conflicted
	pwConflictAll map[string]map[string][]string   // proxy → password → []usernames (for API)
	trafficDirty  sync.Map                         // username → true (needs flush)
	Sessions     *SessionManager

	ifaces *ifaceManager  // child interfaces, public address detection

	peerEgress *peerEgressProber // egress of peers that do not report it (peer_egress.go)

	st *stores // logs and audit records on disk (storage.go)
}

func New(dataDir string) (*App, error) {
	cfg, err := LoadOrInitConfig(dataDir)
	if err != nil {
		return nil, err
	}

	persistPath := dataDir + "/config.yaml"

	// NodeID is the single identity now — wire name and stable handle
	// were the same field-on-disk after the schema merge.
	node := relay.NewNode(cfg.NodeID, cfg.ExitNode)
	node.SetNodeID(cfg.NodeID) // expose stable id to register handshakes
	histPath := ""
	if dataDir != "" {
		histPath = dataDir + "/history.json"
	}
	hist := history.New(histPath)
	_ = hist.Load()

	a := &App{
		store:        NewConfigStore(cfg, persistPath),
		node:         node,
		tls:          NewTLSStore(dataDir),
		dataDir:      dataDir,
		clientCancel: make(map[string]context.CancelFunc),
		hist:         hist,
		proxyHandles: make(map[string]*proxyHandle),
		Sessions:     NewSessionManager(),
	}
	if c := a.store.Get(); len(c.GraphLayout) > 0 {
		if err := a.store.Update(func(c *Config) { MigrateGraphLayout(c) }); err != nil {
			log.Printf("[graph] layout migration not persisted: %v", err)
		}
	}
	a.punch = newPunchManager(a)
	a.ifaces = newIfaceManager(a)
	a.peerEgress = newPeerEgressProber(a)
	a.refreshEgress()
	a.installChildListen()
	a.exit = exit.New(a.node, exitHost{a})
	a.pol = policy.NewEngine()
	a.initPolicy()
	a.acc = newAccessState()
	a.node.SetRestrictedDial(a.restrictedDial)
	a.node.SetPlainDial(a.relayPlainDial)
	a.tree = relay.NewTree(a.node, a.treeConfig)
	a.tree.SetLimits(NestedLimits(cfg))
	a.initStorage()
	return a, nil
}

func (a *App) Store() *ConfigStore { return a.store }
func (a *App) Node() *relay.Node   { return a.node }
func (a *App) Tree() *relay.Tree   { return a.tree }
func (a *App) TLS() *TLSStore     { return a.tls }

// WaitPersisted waits (at most d) for the shutdown flush of traffic counters
// and path history after Run's context ended, so a stop or restart doesn't
// drop the last interval.
func (a *App) WaitPersisted(d time.Duration) {
	done := make(chan struct{})
	go func() { a.persisters.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
	}
}
func (a *App) DataDir() string     { return a.dataDir }
func (a *App) History() *history.Store { return a.hist }

func (a *App) GetConfig() Config { return a.store.Get() }

// ObjectCounts reports app-level live-object counts for /api/diag/runtime.
func (a *App) ObjectCounts() map[string]int {
	m := a.exit.Counts()
	m["hy2_auth_stash"] = server.Stashed()
	m["hy2_addr_stash"] = server.Stashed() // auth and address share one stash entry
	m["sessions"] = a.Sessions.Count()
	m["audit_open_flows"] = audit.OpenFlows()
	return m
}

// ActivePath returns the last successful exit path for a given primary exit_via.
func (a *App) ActivePath(exitVia string) string { return a.exit.ActivePath(exitVia) }

// AllActivePaths returns all tracked active paths.
func (a *App) AllActivePaths() map[string]string { return a.exit.ActivePaths() }

func (a *App) UpdateWebCredentials(username, passHash string) {
	a.store.Update(func(c *Config) {
		c.WebUsername = username
		c.WebPassword = passHash
	})
}

// PersistNodeID writes the node ID to the persistent file.
func (a *App) PersistNodeID(id string) {
	if a.dataDir != "" {
		os.MkdirAll(a.dataDir, 0755)
		os.WriteFile(a.dataDir+"/node-id", []byte(id), 0644)
	}
}

func (a *App) Run(ctx context.Context) error {
	a.appCtx = ctx
	cfg := a.store.Get()
	fmt.Println(`
 _   ___   ______   _____ _____ ___  _     _____
| | | \ \ / /___ \ / ____/ ____/ _ \| |   |  ___|
| |_| |\ V /  __) | (___| |  | |_| | |   | |_
|  _  | | |  |__ < \___ \| |  |  _  | |   |  _|
| | | | | |  ___) |____) | |__| | | | |___| |___
|_| |_| |_| |____/|_____/\____/_| |_|_____|_____|`)
	fmt.Printf("  v%s\n\n", AppVersion)
	log.Printf("[%s] starting node id=%s (exit=%v)", cfg.NodeID, cfg.NodeID, cfg.ExitNode)
	if debugMode() {
		log.Printf("[debug] DEBUG mode enabled (set DEBUG=true in environment)")
	}

	// Detect runtime mode early (normal/bridge/compat/limited)
	netfilter.DetectRuntimeMode()

	// Interfaces before anything that dials: peer addresses may be bound to
	// a child interface, and direct flows leave through the egress.
	a.ifaces.Start(ctx)
	a.ifaces.apply(cfg)
	go a.initEgress(ctx)
	go a.ifaces.pub.Run(ctx, a.node)
	go a.node.RunAttrs(ctx)
	go a.tree.Run(ctx)
	a.exit.Start(ctx)
	a.persisters.Add(1)
	go func() { defer a.persisters.Done(); a.peerEgress.run(ctx) }()

	// Stored logs and audit records (writers, retention)
	a.runStorage(ctx)

	// Start rate ticker, latency prober, and traffic flusher
	go a.node.StartRateTicker(ctx)
	go a.node.StartLatencyProber(ctx)
	// Both persist on ctx.Done; WaitPersisted lets main wait for that.
	a.persisters.Add(2)
	go func() { defer a.persisters.Done(); a.StartTrafficFlusher(ctx) }()
	go func() { defer a.persisters.Done(); a.runHistoryRecorder(ctx) }()

	a.rebuildUserIndex()
	go a.runTrafficResets(ctx)

	// Apply nested discovery
	for peerName, pc := range cfg.Peers {
		if pc.Nested {
			a.node.SetNestedDiscovery(peerName, true)
			log.Printf("[%s] nested discovery enabled for %q", cfg.NodeID, peerName)
		}
	}

	// Wire the inbound-identity sync callback — see docs/peer-identity-sync-bugs.md
	// Bug B. Fires inside relay.handleRegister after the meta exchange, lets
	// us detect inbound peer NodeID changes (using source IP as stable handle).
	a.node.SetOnInboundIdentity(a.handleInboundIdentity)

	// Start hy2 server if configured — check port first
	if cfg.Server != nil {
		port := 5565
		if cfg.Server.Listen != "" {
			primary := extractPrimaryAddr(cfg.Server.Listen)
			if _, p, err := net.SplitHostPort(primary); err == nil {
				if pn, err := strconv.Atoi(p); err == nil {
					port = pn
				}
			}
		}
		conflicts := CheckPorts([]PortConflict{{Port: port, Proto: "udp", Desc: "hy2 server"}})
		if len(conflicts) > 0 {
			return fmt.Errorf("FATAL: hy2 server port %d/udp is already in use — cannot start", port)
		}
		if err := a.restartServer(); err != nil {
			return err
		}
	}

	// Start proxies (SOCKS5 etc) — check port before each
	for _, pc := range cfg.Proxies {
		if pc.Enabled {
			if _, p, err := net.SplitHostPort(pc.Listen); err == nil {
				if pn, _ := strconv.Atoi(p); pn > 0 {
					if c := CheckPorts([]PortConflict{{Port: pn, Proto: "tcp", Desc: pc.Protocol}}); len(c) > 0 {
						log.Printf("[%s] port %d/tcp in use — proxy disabled", pc.Protocol, pn)
						continue
					}
				}
			}
		}
		a.StartProxy(pc)
	}

	// Start SS server if configured
	if cfg.SS != nil && cfg.SS.Enabled {
		if _, p, err := net.SplitHostPort(cfg.SS.Listen); err == nil {
			if pn, _ := strconv.Atoi(p); pn > 0 {
				if c := CheckPorts([]PortConflict{{Port: pn, Proto: "tcp", Desc: "shadowsocks"}}); len(c) > 0 {
					log.Printf("[ss] port %d/tcp in use — shadowsocks disabled", pn)
					cfg.SS = nil
				}
			}
		}
		if cfg.SS != nil {
			a.StartSS(*cfg.SS)
		}
	}

	// Start the Trojan server if configured
	if cfg.Trojan != nil && cfg.Trojan.Enabled {
		start := true
		if _, p, err := net.SplitHostPort(cfg.Trojan.Listen); err == nil {
			if pn, _ := strconv.Atoi(p); pn > 0 {
				if c := CheckPorts([]PortConflict{{Port: pn, Proto: "tcp", Desc: "trojan"}}); len(c) > 0 {
					log.Printf("[trojan] port %d/tcp in use — trojan disabled", pn)
					start = false
				}
			}
		}
		if start {
			a.StartTrojan(*cfg.Trojan)
		}
	}

	// Userspace VPN servers (IKEv2 and L2TP share udp/500+4500)
	if cfg.L2TP != nil && cfg.L2TP.Enabled {
		if err := a.StartL2TP(*cfg.L2TP); err != nil {
			log.Printf("[l2tp] start error: %v", err)
		}
	}
	if cfg.IKEv2 != nil && cfg.IKEv2.Enabled {
		if err := a.StartIKEv2(*cfg.IKEv2); err != nil {
			log.Printf("[ikev2] start error: %v", err)
		}
	}
	if cfg.OpenVPN != nil && cfg.OpenVPN.Enabled {
		if err := a.StartOpenVPN(*cfg.OpenVPN); err != nil {
			log.Printf("[openvpn] start error: %v", err)
		}
	}

	// Start WireGuard
	if cfg.WireGuard != nil && cfg.WireGuard.Enabled {
		wgPort := cfg.WireGuard.ListenPort
		if wgPort == 0 {
			wgPort = 51820
		}
		if c := CheckPorts([]PortConflict{{Port: wgPort, Proto: "udp", Desc: "wireguard"}}); len(c) > 0 {
			log.Printf("[wireguard] port %d/udp in use — wireguard disabled", wgPort)
		} else if err := a.StartWireGuard(*cfg.WireGuard); err != nil {
			log.Printf("[wireguard] start error: %v", err)
		}
	}

	// System traffic capture (policy_capture.go)
	a.startPolicyCapture()
	defer a.policyCaptureRecover()

	// Start clients (block disabled ones from inbound too)
	for _, cl := range cfg.Clients {
		if cl.Disabled {
			a.node.BlockPeer(cl.Name)
		}
		a.StartClient(cl)
	}

	<-ctx.Done()
	a.stopPolicyCapture()
	a.ifaces.Close() // DHCP release
	return ctx.Err()
}

// --- Dynamic client management ---

func (a *App) StartClient(cl ClientEntry) {
	if cl.Disabled {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	// Keyed by Addr, the entry's stable key, as StopClient looks it up. Its
	// first address (PrimaryAddr) changes when the address is edited, and a
	// loop keyed by it was never stopped: two loops then kept replacing each
	// other's connection to the peer.
	if _, ok := a.clientCancel[cl.Addr]; ok {
		return // already running for this entry
	}
	ctx, cancel := context.WithCancel(a.appCtx)
	a.clientCancel[cl.Addr] = cancel
	// The primary loop dials the first address first; say so before the
	// extra loops start, so none of them dials it too (they would read the
	// previous entry's value).
	if addrs := cl.AllAddrs(); len(addrs) > 0 {
		a.primaryDial.Store(cl.Addr, addrs[0])
	}

	// Primary connection (first addr) — registers with relay, handles peer identity
	go a.connectLoop(ctx, cl)

	// Secondary connections — attach as extra QUIC connections. One loop per
	// address: the primary may dial any of them (connectLoop rotates on
	// failure), and each loop stands down while its address is the primary's.
	addrs := cl.AllAddrs()
	if len(addrs) > 1 {
		for i := 0; i < len(addrs); i++ {
			extraAddr := addrs[i]
			go a.connectExtraLoop(ctx, cl, extraAddr, i)
		}
	}
}

// ReconnectAll stops and restarts all connections (outbound + hy2 server) after ID change.
// Restarting the server disconnects all inbound peers, forcing them to reconnect and see the new ID.
func (a *App) ReconnectAll() {
	cfg := a.store.Get()
	// Stop all outbound
	a.mu.Lock()
	for name, cancel := range a.clientCancel {
		cancel()
		delete(a.clientCancel, name)
	}
	a.mu.Unlock()

	// Restart hy2 server to disconnect inbound peers
	if err := a.restartServer(); err != nil {
		log.Printf("[%s] server restart after ID change: %v", a.node.Name(), err)
	}

	time.Sleep(time.Second)
	// Restart outbound
	for _, cl := range cfg.Clients {
		if !cl.Disabled {
			a.StartClient(cl)
		}
	}
	log.Printf("[%s] reconnected all peers after ID change", a.node.Name())
}

func (a *App) StopClient(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	// Try by addr first, then by name (addr is the stable key)
	if cancel, ok := a.clientCancel[name]; ok {
		cancel()
		delete(a.clientCancel, name)
		return
	}
	// Look up addr from config by name
	cfg := a.store.Get()
	for _, cl := range cfg.Clients {
		if cl.Name == name {
			if cancel, ok := a.clientCancel[cl.Addr]; ok {
				cancel()
				delete(a.clientCancel, cl.Addr)
			}
			return
		}
	}
}

func (a *App) AddClient(cl ClientEntry) error {
	a.StartClient(cl)
	return a.store.Update(func(c *Config) {
		// Deduplicate by Addr (stable key)
		for _, existing := range c.Clients {
			if existing.Addr == cl.Addr {
				return
			}
		}
		c.Clients = append(c.Clients, cl)
	})
}

func (a *App) UpdateClient(cl ClientEntry) error {
	a.StopClient(cl.Name)
	a.StartClient(cl)
	return a.store.Update(func(c *Config) {
		for i, existing := range c.Clients {
			if existing.Name == cl.Name || existing.Addr == cl.Addr {
				c.Clients[i] = cl
				return
			}
		}
		c.Clients = append(c.Clients, cl)
	})
}

// UpdateClientByAddr finds a client by oldName (name or addr) and replaces it.
func (a *App) UpdateClientByAddr(oldName string, cl ClientEntry) error {
	a.StopClient(oldName)
	return a.store.Update(func(c *Config) {
		for i, existing := range c.Clients {
			if existing.Name == oldName || existing.Addr == oldName {
				cl.Addr = existing.Addr // preserve addr
				c.Clients[i] = cl
				a.StartClient(cl)
				return
			}
		}
	})
}

func (a *App) RemoveClient(name string) error {
	a.StopClient(name)
	// Clear any block placed by a prior SetClientDisabled. Without this,
	// a disable-then-delete sequence leaves the peer name in n.blocked
	// forever (RemoveClient was asymmetric to SetClientDisabled), so a
	// later inbound connection from that same name gets rejected with
	// "register: <name> is blocked, rejecting". Look up by name OR addr
	// to mirror the deletion loop below; UnblockPeer is idempotent so
	// passing a never-blocked name is safe.
	cfg := a.store.Get()
	for _, cl := range cfg.Clients {
		if cl.Name == name || cl.Addr == name {
			a.node.UnblockPeer(cl.Name)
			break
		}
	}
	return a.store.Update(func(c *Config) {
		for i, cl := range c.Clients {
			if cl.Name == name || cl.Addr == name {
				c.Clients = append(c.Clients[:i], c.Clients[i+1:]...)
				return
			}
		}
	})
}

func (a *App) SetClientDisabled(name string, disabled bool) error {
	if disabled {
		a.StopClient(name)
		// Block and disconnect inbound peer with this name
		a.node.BlockPeer(name)
		a.node.DisconnectPeer(name)
	} else {
		a.node.UnblockPeer(name)
		cfg := a.store.Get()
		for _, cl := range cfg.Clients {
			if cl.Name == name || cl.Addr == name {
				cl.Disabled = false
				a.StartClient(cl)
				break
			}
		}
	}
	return a.store.Update(func(c *Config) {
		for i, cl := range c.Clients {
			if cl.Name == name || cl.Addr == name {
				c.Clients[i].Disabled = disabled
				return
			}
		}
	})
}

// --- Dynamic proxy management ---

func (a *App) StartProxy(pc ProxyConfig) {
	if !pc.Enabled {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.proxyHandles[pc.ID]; ok {
		return
	}
	ctx, cancel := context.WithCancel(a.appCtx)
	ln, err := net.Listen("tcp", pc.Listen)
	if err != nil {
		log.Printf("[%s] proxy %s: %v", a.node.Name(), pc.ID, err)
		cancel()
		return
	}
	// TLS wrapping if cert specified
	if pc.TLSCert != "" {
		tlsCfg, tlsErr := a.tls.TLSConfig(pc.TLSCert)
		if tlsErr != nil {
			log.Printf("[%s] proxy %s: TLS cert %q: %v", a.node.Name(), pc.ID, pc.TLSCert, tlsErr)
			ln.Close()
			cancel()
			return
		}
		ln = tls.NewListener(ln, tlsCfg)
	}
	a.proxyHandles[pc.ID] = &proxyHandle{listener: ln, cancel: cancel}
	tlsLabel := ""
	if pc.TLSCert != "" {
		tlsLabel = "+tls"
	}
	log.Printf("[%s] %s%s proxy %s on %s (exit_via=%q)", a.node.Name(), pc.Protocol, tlsLabel, pc.ID, pc.Listen, pc.ExitVia)
	go a.serveProxy(ctx, ln, pc)
}

func (a *App) StopProxy(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if h, ok := a.proxyHandles[id]; ok {
		h.cancel()
		h.listener.Close()
		delete(a.proxyHandles, id)
	}
}

func (a *App) AddProxy(pc ProxyConfig) error {
	a.StartProxy(pc)
	return a.store.Update(func(c *Config) {
		for _, existing := range c.Proxies {
			if existing.ID == pc.ID {
				return
			}
		}
		c.Proxies = append(c.Proxies, pc)
	})
}

func (a *App) RemoveProxy(id string) error {
	a.StopProxy(id)
	return a.store.Update(func(c *Config) {
		for i, p := range c.Proxies {
			if p.ID == id {
				c.Proxies = append(c.Proxies[:i], c.Proxies[i+1:]...)
				return
			}
		}
	})
}

func (a *App) UpdateProxy(pc ProxyConfig) error {
	a.StopProxy(pc.ID)
	a.StartProxy(pc)
	return a.store.Update(func(c *Config) {
		for i, p := range c.Proxies {
			if p.ID == pc.ID {
				c.Proxies[i] = pc
				return
			}
		}
		c.Proxies = append(c.Proxies, pc)
	})
}

// --- User management ---

func (a *App) rebuildUserIndex() {
	cfg := a.store.Get()
	index := make(map[string]*UserConfig, len(cfg.Users))
	for i := range cfg.Users {
		index[cfg.Users[i].Username] = &cfg.Users[i]
	}

	// Build password conflict maps for password-only proxies.
	// To add a new proxy type: add its key here AND in the frontend proxyRegistry.ts
	proxyPwUsers := make(map[string]map[string][]string)
	for _, proxy := range PasswordOnlyProxies {
		proxyPwUsers[proxy] = make(map[string][]string)
	}
	for _, u := range cfg.Users {
		if !u.Enabled {
			continue
		}
		for _, proxy := range PasswordOnlyProxies {
			pw := u.EffectivePassword(proxy)
			proxyPwUsers[proxy][pw] = append(proxyPwUsers[proxy][pw], u.Username)
		}
	}
	// Also check system password against hy2 users (relay/user ambiguity)
	// Server is nil when the hy2 server is disabled (Edit Local Node with no
	// listen/password) or absent from the config.
	if cfg.Server != nil && cfg.Server.Password != "" {
		if users, ok := proxyPwUsers["hy2"][cfg.Server.Password]; ok {
			_ = users // system password collides; mark those users as conflicted
		}
	}

	conflicted := make(map[string]map[string]bool)
	for proxy, pwMap := range proxyPwUsers {
		for _, usernames := range pwMap {
			if len(usernames) > 1 {
				for _, un := range usernames {
					if conflicted[un] == nil {
						conflicted[un] = make(map[string]bool)
					}
					conflicted[un][proxy] = true
				}
			}
		}
	}

	a.usersMu.Lock()
	a.userIndex = index
	a.pwConflicted = conflicted
	a.pwConflictAll = proxyPwUsers
	a.usersMu.Unlock()
	a.syncUserRates(cfg.Users)
}

// IsPasswordConflicted returns true if the user has a password collision on the given proxy.
func (a *App) IsPasswordConflicted(username, proxy string) bool {
	a.usersMu.RLock()
	defer a.usersMu.RUnlock()
	if m, ok := a.pwConflicted[username]; ok {
		return m[proxy]
	}
	return false
}

// GetPasswordConflicts returns a map of username → proxy → []conflicting usernames.
// Only users with actual conflicts are included.
func (a *App) GetPasswordConflicts() map[string]map[string][]string {
	a.usersMu.RLock()
	defer a.usersMu.RUnlock()
	result := make(map[string]map[string][]string)
	for proxy, pwMap := range a.pwConflictAll {
		for _, usernames := range pwMap {
			if len(usernames) <= 1 {
				continue
			}
			for _, un := range usernames {
				if result[un] == nil {
					result[un] = make(map[string][]string)
				}
				// List the OTHER users this one conflicts with
				others := make([]string, 0, len(usernames)-1)
				for _, o := range usernames {
					if o != un {
						others = append(others, o)
					}
				}
				result[un][proxy] = others
			}
		}
	}
	return result
}

// LookupUser authenticates a username/password pair for a specific proxy
// (socks5/http/l2tp/ikev2/...). The `proxy` argument is consulted against
// the per-user ProxyDisabled list so an admin can revoke access to a single
// protocol without deleting the user. Pass an empty proxy string to skip
// the per-proxy check (for legacy/internal callers that don't represent a
// specific listening protocol). VPN callers (l2tp/ikev2) commonly pass an
// empty password — they validated the secret elsewhere — but they still
// pay the ProxyDisabled check.
func (a *App) LookupUser(username, password, proxy string) (*UserConfig, error) {
	a.usersMu.RLock()
	u, ok := a.userIndex[username]
	a.usersMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("user not found")
	}
	if u.Password != password {
		return nil, fmt.Errorf("invalid password")
	}
	if !u.Enabled {
		return nil, fmt.Errorf("user disabled")
	}
	if proxy != "" && !u.IsProxyEnabled(proxy) {
		return nil, fmt.Errorf("user disabled for proxy %s", proxy)
	}
	if u.ExpiryDate != "" {
		if t, err := time.Parse("2006-01-02", u.ExpiryDate); err == nil && time.Now().After(t) {
			return nil, fmt.Errorf("user expired")
		}
	}
	if u.TrafficLimit > 0 && u.TrafficUsed >= u.TrafficLimit {
		return nil, fmt.Errorf("traffic limit exceeded")
	}
	return u, nil
}

func (a *App) AddUser(u UserConfig) error {
	if u.TrafficReset != "" && u.TrafficResetFrom == "" {
		u.TrafficResetFrom = TrafficNow().Format(time.RFC3339)
	}
	err := a.store.Update(func(c *Config) {
		for _, existing := range c.Users {
			if existing.Username == u.Username {
				return
			}
		}
		c.Users = append(c.Users, u)
	})
	if err == nil {
		a.rebuildUserIndex()
	}
	return err
}

func (a *App) UpdateUser(id string, u UserConfig) error {
	var oldPassword string
	var oldProxyPw map[string]string
	var oldProxyDisabled []string
	var oldUsername string
	err := a.store.Update(func(c *Config) {
		for i, existing := range c.Users {
			if existing.ID == id {
				oldPassword = existing.Password
				oldProxyPw = existing.ProxyPasswords
				oldProxyDisabled = existing.ProxyDisabled
				oldUsername = existing.Username
				u.TrafficUsed = existing.TrafficUsed
				// server-managed fields
				u.TrafficResetAt = existing.TrafficResetAt
				u.TrafficResetFrom = existing.TrafficResetFrom
				if u.TrafficReset != existing.TrafficReset || u.TrafficResetDay != existing.TrafficResetDay {
					// a new period counts from now: no reset for boundaries already passed
					u.TrafficResetFrom = ""
					if u.TrafficReset != "" {
						u.TrafficResetFrom = TrafficNow().Format(time.RFC3339)
					}
				}
				u.ShareTargets = existing.ShareTargets
				u.ShareRev = existing.ShareRev
				u.ShareResetRev = existing.ShareResetRev
				u.SharedFrom = existing.SharedFrom
				c.Users[i] = u
				return
			}
		}
	})
	if err == nil {
		a.rebuildUserIndex()
		// If credentials OR per-proxy access changed, disconnect all active
		// sessions for this user. ProxyDisabled changing means a previously
		// permitted protocol is now revoked — keeping the in-flight tunnel
		// alive would silently keep the user connected after the toggle.
		credsChanged := oldPassword != u.Password ||
			fmt.Sprint(oldProxyPw) != fmt.Sprint(u.ProxyPasswords) ||
			fmt.Sprint(oldProxyDisabled) != fmt.Sprint(u.ProxyDisabled) ||
			(oldUsername != u.Username && oldUsername != "")
		if credsChanged {
			if n := a.Sessions.KickUser(oldUsername); n > 0 {
				log.Printf("[users] credentials changed for %s, kicked %d sessions", u.Username, n)
			}
		}
	}
	return err
}

func (a *App) RemoveUser(id string) error {
	err := a.store.Update(func(c *Config) {
		for i, u := range c.Users {
			if u.ID == id {
				// a user shared out is deleted on its targets too
				for _, t := range u.ShareTargets {
					addSharedDelete(c, SharedDelete{Target: t, UserID: u.ID})
				}
				c.Users = append(c.Users[:i], c.Users[i+1:]...)
				return
			}
		}
	})
	if err == nil {
		a.rebuildUserIndex()
	}
	return err
}

func (a *App) ToggleUser(id string, enabled bool) error {
	err := a.store.Update(func(c *Config) {
		for i, u := range c.Users {
			if u.ID == id {
				c.Users[i].Enabled = enabled
				return
			}
		}
	})
	if err == nil {
		a.rebuildUserIndex()
	}
	return err
}

func (a *App) ResetUserTraffic(id string) error {
	a.usersMu.RLock()
	err := a.store.Update(func(c *Config) {
		for i, u := range c.Users {
			if u.ID == id {
				if live, ok := a.userIndex[u.Username]; ok {
					atomic.StoreInt64(&live.TrafficUsed, 0)
				}
				atomic.StoreInt64(&c.Users[i].TrafficUsed, 0)
				c.Users[i].TrafficResetAt = TrafficNow().Format(time.RFC3339)
				return
			}
		}
	})
	a.usersMu.RUnlock()
	if err == nil {
		a.rebuildUserIndex()
	}
	return err
}

func (a *App) RecordTraffic(username string, bytes int64) {
	a.usersMu.RLock()
	u, ok := a.userIndex[username]
	a.usersMu.RUnlock()
	if !ok {
		return
	}
	atomic.AddInt64(&u.TrafficUsed, bytes)
	a.trafficDirty.Store(username, true)
}

// FlushTraffic persists dirty traffic counters to config.
func (a *App) FlushTraffic() {
	dirty := false
	a.trafficDirty.Range(func(key, _ any) bool {
		dirty = true
		a.trafficDirty.Delete(key)
		return true
	})
	if !dirty {
		return
	}
	a.usersMu.RLock()
	a.store.Update(func(c *Config) {
		for i := range c.Users {
			if u, ok := a.userIndex[c.Users[i].Username]; ok {
				c.Users[i].TrafficUsed = atomic.LoadInt64(&u.TrafficUsed)
			}
		}
	})
	a.usersMu.RUnlock()
}

// StartTrafficFlusher runs a background goroutine to periodically flush traffic.
func (a *App) StartTrafficFlusher(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			a.FlushTraffic()
			return
		case <-t.C:
			a.FlushTraffic()
		}
	}
}

// --- Nested-identity sync (inbound peer NodeID change detection) ---
//
// Outbound NodeID renames are handled inside dialClient's AttachTo callback
// using cfg.Clients[].Addr as the stable handle. Inbound peers have no
// equivalent stable handle in operator-provided config (operators never
// pre-register inbound peers — they appear at runtime when they dial in).
// We therefore use the source IP as the stable handle: persist
// cfg.KnownInboundIDs[remoteIP] = NodeID-last-seen-from-that-IP. On the
// next inbound register, if the same IP reports a different NodeID and the
// previously-seen NodeID is no longer an active peer (so this is NOT a
// NAT-shared-IP scenario where two different peers happen to share an
// egress IP), we treat it as a rename and rewrite cfg.Peers keys,
// cfg.Proxies.ExitVia/ExitPaths, and cfg.Users.ExitVia/ExitPaths — exactly
// what the outbound AttachTo callback does.
//
// See docs/peer-identity-sync-bugs.md Bug B.
func (a *App) handleInboundIdentity(declaredName, nodeID, remoteIP string) {
	if nodeID == "" || remoteIP == "" {
		return
	}
	cfg := a.store.Get()
	prevID := ""
	if cfg.KnownInboundIDs != nil {
		prevID = cfg.KnownInboundIDs[remoteIP]
	}
	// First time we see this IP, or NodeID unchanged — record and return.
	if prevID == "" || prevID == nodeID {
		a.store.Update(func(c *Config) {
			if c.KnownInboundIDs == nil {
				c.KnownInboundIDs = make(map[string]string)
			}
			c.KnownInboundIDs[remoteIP] = nodeID
		})
		return
	}
	// Different NodeID at same source IP — propagate the rename. Same
	// rewrite the outbound dialClient AttachTo callback uses, plus update
	// the persistent index.
	//
	// NAT-shared-IP caveat: if two genuinely different peers happen to share
	// an egress IP (egress NAT) AND both are actively connected, this code
	// will incorrectly rewrite the first peer's nested authorization to the
	// second peer's NodeID. We accept this trade-off because (a) NAT-shared
	// peers are rare in production hy2scale meshes, where each node is
	// typically deployed on a distinct host with its own egress, and (b) the
	// alternative — adding HasPeer / GetLatency timing guards — produces
	// false negatives during the legitimate rename window where the old
	// peer's map entry is still stale, defeating the whole point of the
	// fix. Operators on a NAT-shared topology can disable this behavior by
	// not regenerating NodeIDs (the trigger is operator-controlled).
	oldName := prevID
	newName := nodeID
	log.Printf("[%s] inbound-identity: peer at %s renamed %s → %s — propagating to cfg.Peers / cfg.Proxies / cfg.Users",
		a.node.Name(), remoteIP, oldName, newName)
	a.store.Update(func(c *Config) {
		renamePeerRef := func(s string) string {
			return strings.ReplaceAll(s, oldName, newName)
		}
		newPeers := make(map[string]PeerConfig)
		for k, v := range c.Peers {
			newPeers[renamePeerRef(k)] = v
		}
		c.Peers = newPeers
		for i := range c.Proxies {
			c.Proxies[i].ExitVia = renamePeerRef(c.Proxies[i].ExitVia)
			for j := range c.Proxies[i].ExitPaths {
				c.Proxies[i].ExitPaths[j] = renamePeerRef(c.Proxies[i].ExitPaths[j])
			}
		}
		for i := range c.Users {
			c.Users[i].ExitVia = renamePeerRef(c.Users[i].ExitVia)
			for j := range c.Users[i].ExitPaths {
				c.Users[i].ExitPaths[j] = renamePeerRef(c.Users[i].ExitPaths[j])
			}
		}
		if c.KnownInboundIDs == nil {
			c.KnownInboundIDs = make(map[string]string)
		}
		c.KnownInboundIDs[remoteIP] = newName
	})
	// Mirror relay-side nested map and clear stale latency, same as the
	// outbound rename path.
	a.node.SetNestedDiscovery(newName, a.node.IsNestedEnabled(oldName))
	a.node.SetNestedDiscovery(oldName, false)
	a.node.SetLatency(oldName, 0)
	// Also force-disconnect the stale peer entry. Without this, upstream
	// nodes polling our PeersOf list still see the old NodeID listed as a
	// child until the prober eventually marks it dead — and during that
	// window, the upstream's subPeersCache continues to report old → new is
	// invisible to them. DisconnectPeer flushes the slot immediately so the
	// next /api/internal/peers response carries only the new identity.
	a.node.DisconnectPeer(oldName)
}

// --- Nested discovery ---

func (a *App) SetNested(peer string, enabled bool) error {
	// Strip self prefix — inbound peers under self use selfID/name in UI paths
	// but are direct peers in config (same as dialExit stripping). The UI
	// may send either "<node_id>/peer" or "<name>/peer" depending on which
	// the TreeTable happened to use as rootPath, so accept both.
	cfg := a.store.Get()
	if parts := strings.SplitN(peer, "/", 2); len(parts) == 2 && (parts[0] == a.node.Name() || parts[0] == cfg.NodeID) {
		peer = parts[1]
	}
	a.node.SetNestedDiscovery(peer, enabled)
	return a.store.Update(func(c *Config) {
		if c.Peers == nil {
			c.Peers = make(map[string]PeerConfig)
		}
		pc := c.Peers[peer]
		pc.Nested = enabled
		c.Peers[peer] = pc
		// Cascade: disabling nested also clears all descendant nested configs
		if !enabled {
			prefix := peer + "/"
			for key := range c.Peers {
				if len(key) > len(prefix) && key[:len(prefix)] == prefix {
					delete(c.Peers, key)
				}
			}
		}
	})
}

// SetPeerDisabled marks a peer (or qualified peer path) as disabled,
// preventing it from being used as an exit hop without dropping the connection.
func (a *App) SetPeerDisabled(peer string, disabled bool) error {
	// Strip self prefix to match how SetNested handles it — accept both
	// the display name and the node_id since the UI uses whichever it
	// has cached first.
	cfg := a.store.Get()
	if parts := strings.SplitN(peer, "/", 2); len(parts) == 2 && (parts[0] == a.node.Name() || parts[0] == cfg.NodeID) {
		peer = parts[1]
	}
	return a.store.Update(func(c *Config) {
		if c.Peers == nil {
			c.Peers = make(map[string]PeerConfig)
		}
		pc := c.Peers[peer]
		pc.Disabled = disabled
		c.Peers[peer] = pc
	})
}

// --- Internal ---

// RestartServer is the public wrapper for restartServer.
func (a *App) RestartServer() {
	if err := a.restartServer(); err != nil {
		log.Printf("[server] restart error: %v", err)
	}
}

// restartServer stops the current hy2 server (if running) and starts a new one.
// This disconnects all inbound peers, forcing them to reconnect and discover the new ID.
func (a *App) restartServer() error {
	if a.srvCancel != nil {
		a.srvCancel()
		time.Sleep(500 * time.Millisecond)
	}
	cfg := a.store.Get()
	if cfg.Server == nil || cfg.Server.Listen == "" {
		setPortHopRedirect("") // server off: drop its port redirect too
		return nil
	}
	srvCtx, cancel := context.WithCancel(a.appCtx)
	a.srvCancel = cancel
	return a.startServer(srvCtx, cfg.Server)
}

// extractPrimaryAddr takes an address like "0.0.0.0:5565,44000-45000"
// and returns "0.0.0.0:5565" (first port only) for net.ListenPacket.
func extractPrimaryAddr(addr string) string {
	addr = stripAddrBind(addr)
	idx := strings.LastIndex(addr, ":")
	if idx < 0 {
		return addr
	}
	host := addr[:idx]
	portSpec := addr[idx+1:]
	// Take first port from comma-separated list
	first := strings.SplitN(portSpec, ",", 2)[0]
	// Take start of range if it's a range
	first = strings.SplitN(first, "-", 2)[0]
	return host + ":" + strings.TrimSpace(first)
}

// hy2Dial resolves a peer address spec. "host:port" is one UDP address. A
// spec with several ports ("host:5565,44000-45000", "host:20000-30000") is
// hysteria port hopping: one connection to host that moves to another of
// those ports every 30 s.
func hy2Dial(spec string) (net.Addr, hyclient.ConnFactory, net.IP, error) {
	spec, bind, err := splitAddrBind(spec)
	if err != nil {
		return nil, nil, nil, err
	}
	_, ports, err := net.SplitHostPort(spec)
	if err != nil {
		return nil, nil, nil, err
	}
	if !strings.ContainsAny(ports, ",-") {
		ua, err := net.ResolveUDPAddr("udp", spec)
		if err != nil {
			return nil, nil, nil, err
		}
		if bind.none() {
			return ua, nil, ua.IP, nil
		}
		return ua, boundConnFactory{bind}, ua.IP, nil
	}
	ha, err := udphop.ResolveUDPHopAddr(spec)
	if err != nil {
		return nil, nil, nil, err
	}
	return ha, hopConnFactory{bind}, ha.IP, nil
}

// hopConnFactory opens a port-hopping connection; every hop's socket gets
// the address's binding.
type hopConnFactory struct{ bind addrBind }

func (f hopConnFactory) New(addr net.Addr) (net.PacketConn, error) {
	ha := addr.(*udphop.UDPHopAddr)
	return udphop.NewUDPHopPacketConn(ha, udphop.HopIntervalConfig{},
		func() (net.PacketConn, error) { return f.bind.listen(ha.IP) })
}

// Port hopping, server side: hysteria listens on one port; the other ports
// of the listen spec are redirected to it in a nat chain of our own. Every
// server start rewrites the chain (an empty spec removes it). Only packets
// addressed to this host enter the chain: on a node that also routes (a
// router, a Docker host), UDP that other hosts send out to the same port
// numbers is forwarded untouched.
const hopChain = "HY2SCALE-HOP"

var (
	hopJump      = []string{"-p", "udp", "-m", "addrtype", "--dst-type", "LOCAL", "-j", hopChain}
	hopJumpPlain = []string{"-p", "udp", "-j", hopChain} // before 1.4.0, and where addrtype is missing
)

func setPortHopRedirect(listen string) {
	host, ports, err := net.SplitHostPort(stripAddrBind(listen))
	extra := []udphop.PortRange(nil)
	primary := ""
	if err == nil && strings.ContainsAny(ports, ",-") {
		primary = hopPrimaryPort(ports)
		for _, r := range udphop.ParsePortUnion(ports) {
			if p := strconv.Itoa(int(r.Start)); r.Start == r.End && p == primary {
				continue
			}
			extra = append(extra, r)
		}
	}
	// The families the server socket serves: a wildcard (0.0.0.0, ::, "")
	// or a host name is a dual-stack socket, so both get the redirect.
	ip := net.ParseIP(host)
	fams := map[bool]bool{false: true, true: true} // v6 → wanted
	if ip != nil && !ip.IsUnspecified() {
		v6 := ip.To4() == nil
		fams = map[bool]bool{v6: true, !v6: false}
	}
	if len(extra) == 0 || primary == "" {
		hopRedirectClear(false)
		hopRedirectClear(true)
		return
	}
	if !netfilter.Available() {
		log.Printf("[server] listen %s: no iptables here, so only port %s is reachable (redirect the other ports to it on the host)", listen, primary)
		return
	}
	for _, v6 := range []bool{false, true} {
		if !fams[v6] {
			hopRedirectClear(v6)
			continue
		}
		target := []string{"-j", "REDIRECT", "--to-ports", primary}
		if ip != nil && !ip.IsUnspecified() {
			target = []string{"-d", ip.String(), "-j", "DNAT", "--to-destination", net.JoinHostPort(host, primary)}
		}
		hopRedirectSet(v6, extra, target)
	}
	log.Printf("[server] port hopping: udp %s redirected to %s", ports, primary)
}

// hopPrimaryPort is the port hysteria listens on for a port spec: the first
// port of the first element ("5565,44000-45000" → "5565").
func hopPrimaryPort(ports string) string {
	first := strings.SplitN(ports, ",", 2)[0]
	first = strings.TrimSpace(strings.SplitN(first, "-", 2)[0])
	if n, err := strconv.Atoi(first); err != nil || n < 1 || n > 65535 {
		return ""
	}
	return first
}

// hopRedirectSet rewrites the hop chain of one family (iptables / ip6tables).
func hopRedirectSet(v6 bool, extra []udphop.PortRange, target []string) {
	ipt := netfilter.Prog(v6)
	netfilter.Exec(ipt, "-t", "nat", "-N", hopChain).Run() // exists already: fine
	if out, err := netfilter.Exec(ipt, "-t", "nat", "-F", hopChain).CombinedOutput(); err != nil {
		log.Printf("[server] port hopping: %s nat table unavailable: %v %s", ipt, err, out)
		return
	}
	hopUnjump(ipt)
	if out, err := netfilter.Exec(ipt, append([]string{"-t", "nat", "-I", "PREROUTING"}, hopJump...)...).CombinedOutput(); err != nil {
		log.Printf("[server] port hopping: %s has no addrtype match (%v %s); redirecting the hop ports of every destination", ipt, err, out)
		netfilter.Run(ipt, append([]string{"-t", "nat", "-I", "PREROUTING"}, hopJumpPlain...)...)
	}
	for _, r := range extra {
		dport := fmt.Sprintf("%d:%d", r.Start, r.End)
		args := append([]string{"-t", "nat", "-A", hopChain, "-p", "udp", "--dport", dport}, target...)
		if out, err := netfilter.Exec(ipt, args...).CombinedOutput(); err != nil {
			log.Printf("[server] port hopping redirect %s (%s): %v %s", dport, ipt, err, out)
		}
	}
}

// hopUnjump removes every PREROUTING jump into the hop chain (either form,
// however many times it was added).
func hopUnjump(ipt string) {
	for _, j := range [][]string{hopJump, hopJumpPlain} {
		for i := 0; i < 8 && netfilter.Exec(ipt, append([]string{"-t", "nat", "-D", "PREROUTING"}, j...)...).Run() == nil; i++ {
		}
	}
}

// hopRedirectClear removes the hop chain of one family, if present.
func hopRedirectClear(v6 bool) {
	ipt := netfilter.Prog(v6)
	if netfilter.Exec(ipt, "-t", "nat", "-L", hopChain, "-n").Run() == nil {
		hopUnjump(ipt)
		netfilter.Exec(ipt, "-t", "nat", "-F", hopChain).Run()
		netfilter.Exec(ipt, "-t", "nat", "-X", hopChain).Run()
	}
}

func (a *App) startServer(ctx context.Context, sc *ServerConfig) error {
	cert, err := loadCert(sc.TLSCert, sc.TLSKey)
	if err != nil {
		return err
	}
	listenAddr := extractPrimaryAddr(sc.Listen)
	conn, err := sockmark.ListenPacket("udp", listenAddr)
	if err != nil {
		return err
	}
	setPortHopRedirect(sc.Listen)
	authImpl := &hy2Auth{app: a, sysPassword: sc.Password}
	a.serverCert.Store(&cert)
	hyServer, err := a.newHy2Server(ctx, conn, cert, authImpl, authImpl)
	if err != nil {
		conn.Close()
		return err
	}
	go func() { <-ctx.Done(); hyServer.Close(); conn.Close() }()
	go hyServer.Serve()
	log.Printf("[%s] hy2 server on %s", a.node.Name(), sc.Listen)
	return nil
}

// newHy2Server builds the node's hy2 server over conn (core/server): the
// main listener, or a punched socket serving one peer. Users' streams are
// served by nodeOutbound.
func (a *App) newHy2Server(ctx context.Context, conn net.PacketConn, cert tls.Certificate,
	auth hyserver.Authenticator, events server.ConnEvents) (hyserver.Server, error) {
	return server.New(ctx, a.node, a.exit, server.Config{
		Conn: conn, Cert: cert, Auth: auth, Events: events,
		Users:    &nodeOutbound{app: a, node: a.node, ctx: ctx},
		LocalTCP: a.relayLocalTCP,
		Access:   accessGrants{a},
		MaxChain: func() int { return a.tree.Limits().MaxDepth },
		WrapAPI: func(c net.Conn, authID string) net.Conn {
			return &RelayAuthConn{Conn: c, AuthID: authID}
		},
	})
}

func (a *App) connectLoop(ctx context.Context, cl ClientEntry) {
	addr := cl.Addr // stable key
	next := 0       // index into AllAddrs() for the next dial
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		// Re-read latest config for this addr (Name may have changed)
		cfg := a.store.Get()
		for _, entry := range cfg.Clients {
			if entry.Addr == addr {
				cl = entry
				break
			}
		}
		// Addr is the entry's stable key and may still be the address it was
		// created with; the addresses to dial are AllAddrs.
		dialAddr := cl.Addr
		if addrs := cl.AllAddrs(); len(addrs) > 0 {
			dialAddr = addrs[next%len(addrs)]
		}
		a.primaryDial.Store(addr, dialAddr)
		log.Printf("[%s] connecting to %s (%s)", a.node.Name(), cl.Name, dialAddr)
		if err := a.connectAt(ctx, cl, dialAddr); err != nil {
			log.Printf("[%s] %s: %v", a.node.Name(), cl.Name, err)
		}
		// Multi-address peers: whenever a connection ends or a dial fails,
		// move on to the next configured address — one dead IP must not keep
		// the peer offline (its extra connections hang off this one). Walk
		// the alternatives quickly, then back off.
		next++
		wait := 5 * time.Second
		if n := len(cl.AllAddrs()); n > 1 && next%n != 0 {
			wait = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// connectExtraLoop manages a secondary QUIC connection to an additional IP of the same peer.
// It doesn't register with relay — just attaches the QUIC client to the existing peer's extra conns.
// isPrimaryDial: is addr the one cl's primary connection currently dials?
func (a *App) isPrimaryDial(cl ClientEntry, addr string) bool {
	cur := cl.Addr
	if v, ok := a.primaryDial.Load(cl.Addr); ok {
		cur = v.(string)
	}
	return cur == addr
}

func (a *App) connectExtraLoop(ctx context.Context, cl ClientEntry, extraAddr string, index int) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if a.isPrimaryDial(cl, extraAddr) {
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}

		// Wait for primary to connect first (peer must exist)
		peerName := ""
		for i := 0; i < 30; i++ {
			cfg := a.store.Get()
			for _, entry := range cfg.Clients {
				if entry.PrimaryAddr() == cl.PrimaryAddr() {
					peerName = entry.Name
					break
				}
			}
			if peerName != "" && a.node.HasPeer(peerName) {
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(1 * time.Second):
			}
		}
		if peerName == "" || !a.node.HasPeer(peerName) {
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}

		if a.isPrimaryDial(cl, extraAddr) {
			continue // the primary moved to this address while we waited
		}
		log.Printf("[%s] connecting extra IP %s for %s (#%d)", a.node.Name(), extraAddr, peerName, index)

		addr, connFactory, password, dialDone, err := a.dialTarget(ctx, cl, extraAddr)
		if err != nil {
			log.Printf("[%s] extra IP %s: %v", a.node.Name(), extraAddr, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}

		tlsCfg := hyclient.TLSConfig{
			InsecureSkipVerify: cl.Insecure,
			ServerName:         cl.SNI,
		}
		if tlsCfg.ServerName == "" {
			tlsCfg.ServerName = "hy2scale"
		}
		if cl.CA != "" {
			pool := x509.NewCertPool()
			pool.AppendCertsFromPEM([]byte(cl.CA))
			tlsCfg.RootCAs = pool
		}

		c, _, err := hyclient.NewClient(&hyclient.Config{
			ConnFactory: connFactory,
			ServerAddr:  addr,
			Auth:        password,
			TLSConfig:   tlsCfg,
			QUICConfig: hyclient.QUICConfig{
				// See server-side comment at NewServer for why these are 8/16 MB
				// rather than 64/128 MB. The client side must match: a small
				// receive window on the server is only useful if the *peer's*
				// (i.e. our outbound) receive window is also small, otherwise
				// the chain still over-buffers in the other direction.
				InitialStreamReceiveWindow:     8388608,
				MaxStreamReceiveWindow:         8388608,
				InitialConnectionReceiveWindow: 16777216,
				MaxConnectionReceiveWindow:     16777216,
			},
			CongestionConfig: hyclient.CongestionConfig{
				BBRProfile: cl.BBRProfile,
			},
			BandwidthConfig: hyclient.BandwidthConfig{
				MaxTx: 125000000,
				MaxRx: 125000000,
			},
		})
		dialDone(err == nil)
		if err != nil {
			log.Printf("[%s] extra IP %s: connect failed: %v", a.node.Name(), extraAddr, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}

		// Verify this IP reaches the same node by attempting a lightweight handshake
		var status string
		if a.node.IsInvisible(peerName) {
			status = a.verifyAccessConn(ctx, c, peerName)
		} else {
			status = a.verifyExtraConn(c, peerName)
		}
		a.node.AddPeerConn(peerName, c, extraAddr, status)
		log.Printf("[%s] extra IP %s for %s: %s", a.node.Name(), extraAddr, peerName, status)

		// Monitor connection health — reconnect if it dies
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				c.Close()
				return
			case <-ticker.C:
				// (A connected extra stays even if the primary has moved onto
				// the same address: it may be carrying the live streams.)
				// Probe: try opening a lightweight stream
				// (a gateway reached by node access answers only ping)
				probeAddr := "_relay_latency_:0"
				if a.node.IsInvisible(peerName) {
					probeAddr = proto.StreamPing
				}
				probe, err := c.TCP(probeAddr)
				if err != nil {
					log.Printf("[%s] extra IP %s for %s: connection lost, reconnecting", a.node.Name(), extraAddr, peerName)
					a.node.RemovePeerConn(peerName, extraAddr)
					c.Close()
					if IsPunchAddr(extraAddr) {
						a.punch.lost(extraAddr, cl.Advanced)
					}
					break // break select, continue outer for loop to reconnect
				}
				probe.Close()
				// Re-attach: a reconnect of the primary builds a new peer entry
				// that starts without extras (idempotent when still attached).
				a.node.AddPeerConn(peerName, c, extraAddr, status)
				continue // probe successful, keep monitoring
			}
			break // connection lost, reconnect
		}
	}
}

// verifyExtraConn checks if an extra QUIC connection reaches the expected peer.
// Returns: "online" (same node), "mismatch" (different node), "native" (not hy2scale)
func (a *App) verifyExtraConn(c hyclient.Client, expectedPeerName string) string {
	// Try to open the register stream — if it fails with proxy-like error, it's native
	stream, err := c.TCP("_relay_register_:0")
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "NXDOMAIN") || strings.Contains(errStr, "connection refused") || strings.Contains(errStr, "no such host") {
			return "native"
		}
		return "offline"
	}
	defer stream.Close()

	// Send our info (minimal — just to get the remote ID back)
	stream.Write([]byte{0}) // flags = 0 (no exit, no metadata)
	writeString := func(w io.Writer, s string) {
		b := []byte(s)
		buf := []byte{byte(len(b) >> 8), byte(len(b))}
		w.Write(buf)
		w.Write(b)
	}
	writeString(stream, a.node.Name())

	// Read remote ID
	readString := func(r io.Reader) (string, error) {
		var hdr [2]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return "", err
		}
		n := int(hdr[0])<<8 | int(hdr[1])
		if n == 0 || n > 1024 {
			return "", fmt.Errorf("invalid string length: %d", n)
		}
		data := make([]byte, n)
		if _, err := io.ReadFull(r, data); err != nil {
			return "", err
		}
		return string(data), nil
	}

	remoteID, err := readString(stream)
	if err != nil {
		return "native" // couldn't read ID — likely native hy2
	}

	if remoteID == expectedPeerName {
		return "online"
	}
	log.Printf("[%s] extra IP verification: expected %s, got %s", a.node.Name(), expectedPeerName, remoteID)
	return "mismatch"
}

func (a *App) connect(ctx context.Context, cl ClientEntry) error {
	return a.connectAt(ctx, cl, cl.PrimaryAddr())
}

// connectAt dials dialAddr; cl.Addr stays the peer's identity key.
func (a *App) connectAt(ctx context.Context, cl ClientEntry, dialAddr string) error {
	addr, connFactory, password, dialDone, err := a.dialTarget(ctx, cl, dialAddr)
	if err != nil {
		return fmt.Errorf("address %q: %w", dialAddr, err)
	}

	// TLS
	tlsCfg := hyclient.TLSConfig{
		InsecureSkipVerify: cl.Insecure,
		ServerName:         cl.SNI,
	}
	if tlsCfg.ServerName == "" {
		tlsCfg.ServerName = "hy2scale"
	}
	if cl.CA != "" {
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM([]byte(cl.CA))
		tlsCfg.RootCAs = pool
	}

	// Bandwidth
	maxTx := uint64(125000000) // 1Gbps default
	maxRx := uint64(125000000)
	if cl.MaxTx > 0 {
		maxTx = uint64(cl.MaxTx)
	}
	if cl.MaxRx > 0 {
		maxRx = uint64(cl.MaxRx)
	}

	// QUIC windows. Defaults match the server-side hy2 listener
	// (8 MB stream / 16 MB connection) — see the comment on
	// hyserver.QUICConfig above. Per-peer overrides via cl.InitStreamWindow
	// etc. let an operator restore the old 64 MB behaviour for a
	// specific high-BDP link if needed.
	isw := uint64(8388608)  // 8MB
	msw := uint64(8388608)
	icw := uint64(16777216) // 16MB
	mcw := uint64(16777216)
	if cl.InitStreamWindow > 0 {
		isw = uint64(cl.InitStreamWindow)
	}
	if cl.MaxStreamWindow > 0 {
		msw = uint64(cl.MaxStreamWindow)
	}
	if cl.InitConnWindow > 0 {
		icw = uint64(cl.InitConnWindow)
	}
	if cl.MaxConnWindow > 0 {
		mcw = uint64(cl.MaxConnWindow)
	}

	c, _, err := hyclient.NewClient(&hyclient.Config{
		ConnFactory: connFactory,
		ServerAddr:  addr,
		Auth:        password,
		TLSConfig:   tlsCfg,
		QUICConfig: hyclient.QUICConfig{
			InitialStreamReceiveWindow:     isw,
			MaxStreamReceiveWindow:         msw,
			InitialConnectionReceiveWindow: icw,
			MaxConnectionReceiveWindow:     mcw,
		},
		CongestionConfig: hyclient.CongestionConfig{
			BBRProfile: cl.BBRProfile,
		},
		BandwidthConfig: hyclient.BandwidthConfig{
			MaxTx: maxTx,
			MaxRx: maxRx,
		},
		FastOpen: cl.FastOpen,
	})
	dialDone(err == nil)
	if err != nil {
		return err
	}
	defer c.Close()
	if IsPunchAddr(dialAddr) {
		defer a.punch.lost(dialAddr, cl.Advanced)
	}
	log.Printf("[%s] connected to %s (%s)", a.node.Name(), cl.Name, dialAddr)

	onID := func(remoteID string) {
		// Check for name conflict: another client with different addr has the same ID
		if remoteID != "" {
			isConflict := false
			cfg := a.store.Get()
			for _, other := range cfg.Clients {
				if other.Addr != cl.Addr && other.Name == remoteID {
					log.Printf("[%s] NAME CONFLICT: %s (%s) and %s both claim ID %q",
						a.node.Name(), cl.Addr, cl.Name, other.Addr, remoteID)
					isConflict = true
					break
				}
			}
			if remoteID == a.node.Name() {
				log.Printf("[%s] NAME CONFLICT: peer %s claims our own ID %q", a.node.Name(), cl.Addr, remoteID)
				isConflict = true
			}
			if isConflict {
				a.node.SetPeerConflict(cl.Addr, remoteID)
				return // don't rename
			}
			// No conflict — clear any previous conflict for this addr
			a.node.SetPeerConflict(cl.Addr, "")
		}
		if remoteID != "" && remoteID != cl.Name {
			log.Printf("[%s] peer %s actual ID: %s", a.node.Name(), cl.Addr, remoteID)
			oldName := cl.Name
			a.store.Update(func(cfg *Config) {
				for i, entry := range cfg.Clients {
					if entry.Addr == cl.Addr {
						cfg.Clients[i].Name = remoteID
						break
					}
				}
				// Rename all references to old peer ID throughout config
				renamePeerRef := func(s string) string {
					return strings.ReplaceAll(s, oldName, remoteID)
				}
				// Peers config (nested flags, path-based keys)
				newPeers := make(map[string]PeerConfig)
				for k, v := range cfg.Peers {
					newPeers[renamePeerRef(k)] = v
				}
				cfg.Peers = newPeers
				// Proxy exit_via and exit_paths
				for i := range cfg.Proxies {
					cfg.Proxies[i].ExitVia = renamePeerRef(cfg.Proxies[i].ExitVia)
					for j := range cfg.Proxies[i].ExitPaths {
						cfg.Proxies[i].ExitPaths[j] = renamePeerRef(cfg.Proxies[i].ExitPaths[j])
					}
				}
				// User exit_via and exit_paths
				for i := range cfg.Users {
					cfg.Users[i].ExitVia = renamePeerRef(cfg.Users[i].ExitVia)
					for j := range cfg.Users[i].ExitPaths {
						cfg.Users[i].ExitPaths[j] = renamePeerRef(cfg.Users[i].ExitPaths[j])
					}
				}
			})
			// Clean up old name from relay
			a.node.SetNestedDiscovery(remoteID, a.node.IsNestedEnabled(oldName))
			a.node.SetNestedDiscovery(oldName, false)
			// Clear old latency so topology doesn't show stale entry
			a.node.SetLatency(oldName, 0)
		}
	}
	// Adaptive: a full peer (register), else node access by user (hello:
	// these are a user's credentials there), else a plain hy2 server.
	attachAccess := func() error {
		err := a.node.AttachAccess(ctx, cl.Name, c, func(remoteID string) bool {
			onID(remoteID)
			a.showAccessTree(remoteID)
			return remoteID != a.node.Name() && remoteID != a.store.Get().NodeID
		})
		a.noteAccess(cl.Addr, err)
		return err
	}
	if cl.Access == AccessUser {
		err = attachAccess()
	} else {
		err = a.node.AttachTo(ctx, cl.Name, c, onID)
		if err == relay.ErrNotHy2scale && ctx.Err() == nil {
			err = attachAccess()
		}
	}
	if errors.Is(err, relay.ErrAccessRefused) && ctx.Err() == nil {
		log.Printf("[%s] %s: no node access with these credentials: using it as a plain hy2 server", a.node.Name(), cl.Addr)
		err = relay.ErrNotHy2scale
	}
	if err == relay.ErrNotHy2scale && ctx.Err() == nil {
		// Relay protocol not supported — native hy2 server
		log.Printf("[%s] %s is not hy2scale, attaching as native hy2", a.node.Name(), cl.Addr)
		// Need a fresh client since the old one may be broken
		c2, _, err2 := hyclient.NewClient(&hyclient.Config{
			ConnFactory: connFactory,
			ServerAddr:  addr,
			Auth:        cl.Password,
			TLSConfig:   tlsCfg,
			QUICConfig: hyclient.QUICConfig{
				InitialStreamReceiveWindow:     isw,
				MaxStreamReceiveWindow:         msw,
				InitialConnectionReceiveWindow: icw,
				MaxConnectionReceiveWindow:     mcw,
			},
			CongestionConfig: hyclient.CongestionConfig{
				BBRProfile: cl.BBRProfile,
			},
			BandwidthConfig: hyclient.BandwidthConfig{MaxTx: maxTx, MaxRx: maxRx},
			FastOpen:        cl.FastOpen,
		})
		if err2 != nil {
			return err // return original error
		}
		defer c2.Close()
		return a.node.AttachNative(ctx, cl.Name, c2)
	}
	return err
}

func (a *App) serveProxy(ctx context.Context, ln net.Listener, pc ProxyConfig) {
	go func() { <-ctx.Done(); ln.Close() }()
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		switch pc.Protocol {
		case "http":
			go a.handleHTTP(c, &pc)
		case "shadowsocks", "ss":
			// proxies[shadowsocks] previously fell through to handleSOCKS5,
			// silently serving SOCKS5 on a port the UI advertised as SS —
			// so any SS client (sing-box, mihomo, ss-libev) got protocol
			// mismatches and timed out. The cipher comes from a per-proxy
			// override in pc.Method; default aes-256-gcm if unset.
			method := pc.Method
			if method == "" {
				method = "aes-256-gcm"
			}
			go a.handleSS(c, method)
		default:
			go a.handleSOCKS5(c, &pc)
		}
	}
}

func (a *App) handleSOCKS5(conn net.Conn, pc *ProxyConfig) {
	defer conn.Close()
	buf := make([]byte, 512)

	// SOCKS5 greeting
	n, _ := conn.Read(buf)
	if n < 2 || buf[0] != 0x05 {
		return
	}

	// Check if users are configured
	cfg := a.store.Get()
	hasUsers := len(cfg.Users) > 0

	var user *UserConfig
	if hasUsers {
		// Require username/password auth (RFC 1929, method 0x02)
		conn.Write([]byte{0x05, 0x02})

		// Read auth: {ver=0x01, ulen, username, plen, password}
		n, _ = conn.Read(buf)
		if n < 3 || buf[0] != 0x01 {
			conn.Write([]byte{0x01, 0x01}) // auth failed
			return
		}
		ulen := int(buf[1])
		if n < 2+ulen+1 {
			conn.Write([]byte{0x01, 0x01})
			return
		}
		username := string(buf[2 : 2+ulen])
		plen := int(buf[2+ulen])
		if n < 3+ulen+plen {
			conn.Write([]byte{0x01, 0x01})
			return
		}
		password := string(buf[3+ulen : 3+ulen+plen])

		var err error
		user, err = a.LookupUser(username, password, "socks5")
		if err != nil {
			auditRefused(audit.InSOCKS5, username, conn, err)
			conn.Write([]byte{0x01, 0x01}) // auth failed
			return
		}
		conn.Write([]byte{0x01, 0x00}) // auth success
	} else {
		// No users configured — no auth required (backward compat)
		conn.Write([]byte{0x05, 0x00})
	}

	// SOCKS5 request
	n, _ = conn.Read(buf)
	if n < 7 || buf[1] != 0x01 {
		conn.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	var addr string
	switch buf[3] {
	case 0x01:
		addr = fmt.Sprintf("%d.%d.%d.%d:%d", buf[4], buf[5], buf[6], buf[7], int(buf[8])<<8|int(buf[9]))
	case 0x03:
		dl := int(buf[4])
		addr = fmt.Sprintf("%s:%d", buf[5:5+dl], int(buf[5+dl])<<8|int(buf[5+dl+1]))
	case 0x04:
		addr = fmt.Sprintf("[%s]:%d", net.IP(buf[4:20]), int(buf[20])<<8|int(buf[21]))
	default:
		conn.Write([]byte{0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	// Route: a listener selection overrides the user's
	username := ""
	sel := userSel(user)
	if user != nil {
		username = user.Username
	}
	if ps := proxySel(pc); ps.set() {
		sel = ps
	}
	rt := a.route(sel, a.userSrc(policy.KindSOCKS5, user, conn.RemoteAddr()), "tcp", addr)

	af := beginUserFlow(audit.InSOCKS5, "tcp", username, conn.RemoteAddr(), addr)
	dctx := audit.NewContext(withRestrict(context.Background(), a.userRestrict(username)), af)

	// IP destination: reply first so the client sends its hello, recover the
	// host name from it (sniff_override.go), then dial. A failed dial can no
	// longer be reported and just closes the connection.
	var remote net.Conn
	var err error
	if wantSniff(addr) {
		conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		peeked, waited := peekHello(conn, nil)
		remote, err = a.dialSniffedTCP(dctx, rt, addr, peeked, waited, isLoopbackAddr(conn.RemoteAddr()))
		if err != nil {
			af.End(err)
			denyClose(conn, err)
			return
		}
		defer remote.Close()
		defer af.End(nil) // early returns; relayUser ends it otherwise
		if len(peeked) > 0 {
			if _, werr := remote.Write(peeked); werr != nil {
				return
			}
			af.Add(int64(len(peeked)), 0)
		}
	} else {
		remote, err = rt.dial(dctx)
		if err != nil {
			af.End(err)
			if d, deny := policy.AsDeny(err); deny {
				if d.Reject {
					// 0x02: connection not allowed by ruleset
					conn.Write([]byte{0x05, 0x02, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
				}
				return
			}
			conn.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
			return
		}
		defer remote.Close()
		defer af.End(nil)
		conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	}

	// Device row (username+IP+protocol) with live accounting
	remoteIP := ""
	if ta, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		remoteIP = ta.IP.String()
	}
	a.relayUser(conn, remote, username, remoteIP, "socks5", true, af)
}

// bufferedBytes drains what br has read past the request header.
func bufferedBytes(br *bufio.Reader) []byte {
	if br.Buffered() == 0 {
		return nil
	}
	b := make([]byte, br.Buffered())
	n, _ := br.Read(b)
	return b[:n]
}

// halfCloseWriteOrClose forwards an end-of-stream signal on `c`'s write
// side only, so the peer can read the natural FIN while we keep reading
// the response on the read side. Used by every proxy handler's
// bidirectional copy plumbing to avoid aborting the opposite direction's
// in-flight buffered data when one side hangs up.
//
// For TCP / QUIC streams CloseWrite is the correct primitive. For
// `net.Pipe()` ends (e.g. bond's user-side conn) there's no half-close,
// so we fall back to full Close — which IS the right signal for the
// bond writer's EOF detection.
func halfCloseWriteOrClose(c net.Conn) {
	type writeCloser interface{ CloseWrite() error }
	if cw, ok := c.(writeCloser); ok {
		_ = cw.CloseWrite()
		return
	}
	// Relay streams can't carry a half-close: closing one here aborted the
	// response still in flight (the tail of every reply was lost whenever
	// the client half-closed first). Leave it open; halfCloseAndBound's
	// read deadline reaps it.
	if nh, ok := c.(interface{ NoHalfClose() bool }); ok && nh.NoHalfClose() {
		return
	}
	_ = c.Close()
}

// halfCloseGracePeriod bounds how long the surviving copy direction is
// allowed to stay alive after the OTHER direction has ended. Set per the
// proxy-half-close audit: long enough for normal HTTP request/response
// and bond drain-then-half-close to complete (both well under 10 s in
// observed traffic), short enough to prevent a malicious or hung peer
// from leaking goroutines + relay streams (which historically caused
// host-memory exhaustion before the legacy cancel-on-FIN was added).
const halfCloseGracePeriod = 30 * time.Second

// halfCloseAndBound performs a graceful end-of-stream + bounded-drain
// handoff on `c`. Two effects, both essential:
//
//  1. CloseWrite (or full Close fallback) so the peer reads a clean FIN.
//     This is the data-integrity half: in-flight buffered bytes that have
//     already been written into c's send queue are NOT aborted, unlike
//     the legacy `cancel()`-on-first-direction-end pattern.
//
//  2. SetReadDeadline(now + halfCloseGracePeriod). The OTHER copy goroutine
//     (in this handler's bidirectional copy pair) is reading from `c`; if
//     its peer never reciprocates the FIN, that Read would block forever.
//     The deadline forces the Read to error within `halfCloseGracePeriod`
//     so the handler returns and the stream / goroutine are reclaimed.
//
// This combined shape replaces both the destructive `cancel()` (which
// preserved liveness but lost in-flight data) and the naive bare
// CloseWrite (which preserved data but leaked sessions).
func halfCloseAndBound(c net.Conn) {
	if c == nil {
		return
	}
	halfCloseWriteOrClose(c)
	_ = c.SetReadDeadline(time.Now().Add(halfCloseGracePeriod))
}

// handleHTTP implements an HTTP CONNECT proxy (RFC 7231 §4.3.6).
// Also supports plain HTTP forwarding for non-CONNECT requests.
func (a *App) handleHTTP(conn net.Conn, pc *ProxyConfig) {
	defer conn.Close()
	br := bufio.NewReader(conn)

	// Read the HTTP request line
	reqLine, err := br.ReadString('\n')
	if err != nil {
		return
	}
	parts := strings.SplitN(strings.TrimSpace(reqLine), " ", 3)
	if len(parts) < 3 {
		conn.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
		return
	}
	method, target := parts[0], parts[1]

	// Read headers (we need Host and Proxy-Authorization)
	headers := make(map[string]string)
	for {
		line, err := br.ReadString('\n')
		if err != nil || strings.TrimSpace(line) == "" {
			break
		}
		if idx := strings.IndexByte(line, ':'); idx > 0 {
			headers[strings.ToLower(strings.TrimSpace(line[:idx]))] = strings.TrimSpace(line[idx+1:])
		}
	}

	// Auth: check Proxy-Authorization if users configured
	cfg := a.store.Get()
	hasUsers := len(cfg.Users) > 0
	var user *UserConfig
	username := ""
	if hasUsers {
		authHeader := headers["proxy-authorization"]
		if authHeader == "" {
			conn.Write([]byte("HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic realm=\"proxy\"\r\n\r\n"))
			return
		}
		// Parse Basic auth: "Basic base64(user:pass)"
		if !strings.HasPrefix(authHeader, "Basic ") {
			conn.Write([]byte("HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic realm=\"proxy\"\r\n\r\n"))
			return
		}
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(authHeader, "Basic "))
		if err != nil {
			conn.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
			return
		}
		creds := strings.SplitN(string(decoded), ":", 2)
		if len(creds) != 2 {
			conn.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
			return
		}
		u, err := a.LookupUser(creds[0], creds[1], "http")
		if err != nil {
			auditRefused(audit.InHTTP, creds[0], conn, err)
			conn.Write([]byte("HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic realm=\"proxy\"\r\n\r\n"))
			return
		}
		user = u
		username = u.Username
	}

	// Resolve exit routing: a listener selection overrides the user's
	sel := userSel(user)
	if ps := proxySel(pc); ps.set() {
		sel = ps
	}

	var addr string
	var connectPeeked []byte
	connectSniff, connectWaited := false, false
	if method == "CONNECT" {
		// CONNECT method: tunnel mode
		addr = target
		if !strings.Contains(addr, ":") {
			addr += ":443"
		}
		// IP destination: answer first so the client sends its hello and
		// the host name can be recovered (sniff_override.go).
		if wantSniff(addr) {
			conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
			connectSniff = true
			connectPeeked, connectWaited = peekHello(conn, bufferedBytes(br))
		}
	} else {
		// Plain HTTP: extract host from URL or Host header
		host := headers["host"]
		if host == "" {
			// Try extracting from absolute URL
			if strings.HasPrefix(target, "http://") {
				u := strings.TrimPrefix(target, "http://")
				if idx := strings.IndexByte(u, '/'); idx > 0 {
					host = u[:idx]
				} else {
					host = u
				}
			}
		}
		if host == "" {
			conn.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
			return
		}
		if _, _, err := net.SplitHostPort(host); err != nil {
			host = net.JoinHostPort(strings.Trim(host, "[]"), "80")
		}
		addr = host
	}

	// Dial remote
	af := beginUserFlow(audit.InHTTP, "tcp", username, conn.RemoteAddr(), addr)
	dctx := audit.NewContext(withRestrict(context.Background(), a.userRestrict(username)), af)
	rt := a.route(sel, a.userSrc(policy.KindHTTP, user, conn.RemoteAddr()), "tcp", addr)
	var remote net.Conn
	if connectSniff {
		remote, err = a.dialSniffedTCP(dctx, rt, addr, connectPeeked, connectWaited, isLoopbackAddr(conn.RemoteAddr()))
	} else {
		remote, err = rt.dial(dctx)
	}
	if err != nil {
		af.End(err)
		// After the early 200 a 502 can no longer be sent; just close.
		if connectSniff {
			denyClose(conn, err)
		} else if d, deny := policy.AsDeny(err); deny {
			if d.Reject {
				conn.Write([]byte("HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n"))
			}
		} else {
			conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		}
		return
	}
	defer remote.Close()
	defer af.End(nil) // early returns; relayUser ends it otherwise

	if method == "CONNECT" {
		if !connectSniff {
			conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		} else if len(connectPeeked) > 0 {
			if _, werr := remote.Write(connectPeeked); werr != nil {
				return
			}
			af.Add(int64(len(connectPeeked)), 0)
		}
	} else {
		// Forward the original request to the remote
		var req strings.Builder
		fmt.Fprintf(&req, "%s %s %s\r\n", method, target, parts[2])
		for k, v := range headers {
			if k != "proxy-authorization" && k != "proxy-connection" {
				fmt.Fprintf(&req, "%s: %s\r\n", k, v)
			}
		}
		req.WriteString("\r\n")
		// Forward any remaining buffered data
		req.Write(bufferedBytes(br))
		n, _ := io.WriteString(remote, req.String())
		af.Add(int64(n), 0)
	}

	// Device row with live accounting
	remoteIP := ""
	if ta, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		remoteIP = ta.IP.String()
	}
	a.relayUser(conn, remote, username, remoteIP, "http", true, af)
}

// copyCtx copies data respecting context cancellation.
func copyCtx(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	done := make(chan struct{})
	var n int64
	var err error
	go func() {
		n, err = io.Copy(dst, src)
		close(done)
	}()
	select {
	case <-done:
		return n, err
	case <-ctx.Done():
		// Force close to unblock io.Copy
		if c, ok := src.(net.Conn); ok {
			c.Close()
		}
		if c, ok := dst.(net.Conn); ok {
			c.Close()
		}
		<-done
		return n, ctx.Err()
	}
}


// --- helpers ---

// nodeOutbound serves the streams of hy2 users (core/server.Users): their
// exit route, or node access by user.
type nodeOutbound struct {
	app  *App
	node *relay.Node
	ctx  context.Context
}

// (Mesh-internal streams of users are the server's: core/server/access.go.)
func (o *nodeOutbound) TCP(_ context.Context, authID, peerAddr, actualAddr string, isBridged bool) (net.Conn, error) {
	// hy2 users follow their exit route like every other protocol, and get
	// a device row with live accounting.
	af := beginUserFlowStr(audit.InHy2, "tcp", strings.TrimPrefix(authID, "user:"), peerAddr, actualAddr)
	if err := o.app.hy2UserUsable(authID); err != nil {
		af.Fail(audit.ResultRefused, err)
		af.End(err)
		return nil, err
	}
	c, err := o.hy2UserTCP(audit.NewContext(o.ctx, af), authID, peerAddr, actualAddr, isBridged)
	if err != nil {
		af.End(err)
		return nil, err
	}
	return o.app.meterHy2(authID, peerAddr, c, af), nil
}

// hy2UserTCP dials an hy2 user's stream along its selection (route.go).
func (o *nodeOutbound) hy2UserTCP(ctx context.Context, authID, peerAddr, actualAddr string, isBridged bool) (net.Conn, error) {
	ctx = withRestrict(ctx, o.app.userRestrict(strings.TrimPrefix(authID, "user:")))
	rt := o.app.hy2Route(authID, peerAddr, "tcp", actualAddr)
	if !isBridged && wantSniff(actualAddr) {
		// dialled once the client's first bytes are in, with the host name
		// they carry (the client is on another host); sniff_override.go
		return newDeferredDialConn(func(peeked []byte, waited bool) (net.Conn, error) {
			return o.app.dialSniffedTCP(ctx, rt, actualAddr, peeked, waited, false)
		}), nil
	}
	return rt.dial(ctx)
}

func (o *nodeOutbound) UDP(_ context.Context, authID, peerAddr, reqAddr string) (hyserver.UDPConn, error) {
	af := beginUserFlowStr(audit.InHy2, "udp", strings.TrimPrefix(authID, "user:"), peerAddr, reqAddr)
	if err := o.app.hy2UserUsable(authID); err != nil {
		af.Fail(audit.ResultRefused, err)
		af.End(err)
		return nil, err
	}
	var c net.Conn
	var err error
	ctx := audit.NewContext(withRestrict(o.ctx, o.app.userRestrict(strings.TrimPrefix(authID, "user:"))), af)
	c, err = o.app.hy2Route(authID, peerAddr, "udp", reqAddr).dial(ctx)
	if err != nil {
		af.End(err)
		return nil, err
	}
	return &server.UDPAdapter{Conn: o.app.meterHy2(authID, peerAddr, c, af), Addr: reqAddr}, nil
}

// hy2UserUsable refuses a new stream of an hy2 user that expired or used up
// its traffic limit while its QUIC connection stayed open.
func (a *App) hy2UserUsable(authID string) error {
	name := strings.TrimPrefix(authID, "user:")
	a.usersMu.RLock()
	u, ok := a.userIndex[name]
	a.usersMu.RUnlock()
	if !ok || !u.Enabled {
		return fmt.Errorf("user %s not active", name)
	}
	if userExpired(u) || a.overTrafficLimit(name) {
		return fmt.Errorf("user %s expired or over its traffic limit", name)
	}
	return nil
}

// hy2Route is the routing of an hy2-authenticated user's ("user:<name>")
// flow to addr.
func (a *App) hy2Route(authID, peerAddr, network, addr string) *routed {
	name := strings.TrimPrefix(authID, "user:")
	a.usersMu.RLock()
	u := a.userIndex[name]
	var sel exitSel
	src := flowSrc{kind: policy.KindHy2, addr: peerAddr}
	if u != nil {
		sel = userSel(u)
		src.user = u.ID
	}
	a.usersMu.RUnlock()
	r := a.route(sel, src, network, addr)
	r.rewrite = true
	return r
}

// hy2Auth handles authentication for the hy2 server.
// - User password → accept as user proxy client (identified by username)
// - System password → must be hy2scale relay (verified after connect via relay protocol)
type hy2Auth struct {
	app          *App
	sysPassword  string
}

func (a *hy2Auth) Authenticate(addr net.Addr, auth string, tx uint64) (bool, string) {
	// A hole-punch probe: record where it came from, never log it in.
	if a.app.punch != nil && a.app.punch.observeProbe(auth, addr) {
		return false, ""
	}
	// Check user passwords first
	cfg := a.app.store.Get()
	for _, u := range cfg.Users {
		if u.EffectivePassword("hy2") == auth && u.Enabled {
			// Per-proxy disable: admin UI can revoke a user from hy2 only.
			if !u.IsProxyEnabled("hy2") {
				continue
			}
			// Skip if this user's hy2 password conflicts with another user
			if a.app.IsPasswordConflicted(u.Username, "hy2") {
				continue
			}
			// Check expiry
			if u.ExpiryDate != "" {
				if t, err := time.Parse("2006-01-02", u.ExpiryDate); err == nil && time.Now().After(t) {
					return false, ""
				}
			}
			// Check traffic limit
			if u.TrafficLimit > 0 && u.TrafficUsed >= u.TrafficLimit {
				return false, ""
			}
			return true, "user:" + u.Username
		}
	}
	// Check system password (for hy2scale relay nodes)
	if auth == a.sysPassword {
		return true, "system"
	}
	return false, ""
}

func loadCert(certFile, keyFile string) (tls.Certificate, error) {
	if certFile != "" && keyFile != "" {
		return tls.LoadX509KeyPair(certFile, keyFile)
	}
	log.Printf("generating self-signed cert")
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), NotBefore: time.Now(),
		NotAfter: time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}
