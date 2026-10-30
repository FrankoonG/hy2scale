// Core: this node, its peers, the topology and node entries.

package api

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"

	relay "github.com/FrankoonG/hy2scale/core/node"
	"github.com/FrankoonG/hy2scale/core/proto"
	"github.com/FrankoonG/hy2scale/internal/app"
)

// coreRoutes: the routes of the core: node, peers and topology, node entries, interfaces, rules, certificates, settings, diagnostics.
func (s *Server) coreRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/node", s.getNode)
	m.HandleFunc("GET /api/stats", s.getStats)
	m.HandleFunc("PUT /api/node", s.updateNode)
	m.HandleFunc("GET /api/peers", s.getPeers)
	m.HandleFunc("GET /api/topology", s.getTopology)
	m.HandleFunc("GET /api/paths/status", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.app.PathStatuses()) })
	m.HandleFunc("GET /api/graph-canvases", s.getGraphCanvases)
	m.HandleFunc("GET /api/graph-canvases/stream", s.streamGraphCanvases)
	m.HandleFunc("PUT /api/graph-canvases/{id}", s.putGraphCanvas)
	m.HandleFunc("DELETE /api/graph-canvases/{id}", s.deleteGraphCanvas)
	m.HandleFunc("GET /api/peers/{name}/peers", s.getNestedPeers)
	m.HandleFunc("PUT /api/peers/{name}/nested", s.setNested)
	m.HandleFunc("PUT /api/peers/{name}/disable", s.setPeerDisabled)
	m.HandleFunc("GET /api/clients", s.getClients)
	m.HandleFunc("GET /api/clients/{name}", s.getClient)
	m.HandleFunc("POST /api/clients", s.addClient)
	m.HandleFunc("PUT /api/clients/{name}", s.updateClient)
	m.HandleFunc("PUT /api/clients/{name}/disable", s.disableClient)
	m.HandleFunc("DELETE /api/clients/{name}", s.removeClient)
	// WireGuard
	m.HandleFunc("GET /api/punch/status", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.app.PunchStatuses()) })
	m.HandleFunc("GET /api/system/interfaces", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.app.BindableInterfaces()) })
	s.registerInterfaceRoutes(m)
	// Rules
	m.HandleFunc("GET /api/rules", s.getRules)
	m.HandleFunc("POST /api/rules", s.addRule)
	m.HandleFunc("PUT /api/rules/{id}", s.updateRule)
	m.HandleFunc("DELETE /api/rules/{id}", s.deleteRule)
	m.HandleFunc("PUT /api/rules/{id}/toggle", s.toggleRule)
	// Port check
	m.HandleFunc("POST /api/check-ports", s.checkPorts)
	// DNS resolver diagnostic — sanity-check that ResolveViaExit returns
	// the expected IP through the configured upstream/exit. Used by ops
	// to verify the relay-routed DNS is healthy without needing to add
	// a domain rule and watch logs.
	//   GET /api/diag/resolve?name=example.com&exit_via=us
	m.HandleFunc("GET /api/diag/resolve", s.diagResolve)
	// Per-peer relay diagnostics — three axes of state we wish we'd had
	// during the cn-xinchang +82ms drift investigation. None of these
	// touch peer state; all are read-only counters / rings on the live
	// peer struct.
	//   GET /api/diag/peer-health   QUIC-tunnel proxy: ping ok/fail,
	//                                consecutive failCount, tx/rx bytes
	//   GET /api/diag/peer-rebinds  per-peer count of [via] rebind-probe
	//                                arrivals — chronic per-stream
	//                                disruption signal
	//   GET /api/diag/peer-latency  rolling time series of recent
	//                                latency_ms samples (~10 min ring),
	//                                so an operator can pinpoint when
	//                                a peer's RTT started creeping up
	m.HandleFunc("GET /api/diag/peer-health", s.diagPeerHealth)
	m.HandleFunc("GET /api/diag/peer-rebinds", s.diagPeerRebinds)
	m.HandleFunc("GET /api/diag/peer-latency", s.diagPeerLatency)
	m.HandleFunc("GET /api/diag/peer-history", s.diagPeerHistory)
	//   GET /api/diag/runtime       goroutine / heap / relay-object counts,
	//                                the first thing to read when a node's
	//                                memory keeps growing
	m.HandleFunc("GET /api/diag/runtime", s.diagRuntime)
	m.HandleFunc("GET /api/logs", s.handleLogs)
	m.HandleFunc("GET /api/settings/ui", s.getUISettings)
	m.HandleFunc("PUT /api/settings/ui", s.updateUISettings)
	m.HandleFunc("GET /api/settings/node", s.getNodeSettings)
	m.HandleFunc("PUT /api/settings/node", s.updateNodeSettings)
	// TLS
	m.HandleFunc("GET /api/tls", s.listCerts)
	m.HandleFunc("POST /api/tls/import", s.importCert)
	m.HandleFunc("POST /api/tls/import-path", s.importCertFromPath)
	m.HandleFunc("POST /api/tls/generate", s.generateCert)
	m.HandleFunc("POST /api/tls/sign", s.signCertWithCA)
	m.HandleFunc("GET /api/tls/{id}/pem", s.getCertPEM)
	m.HandleFunc("DELETE /api/tls/{id}", s.deleteCert)
}

// topoSubPeer: a node of the nested peer tree (core/node).
type topoSubPeer = relay.TreePeer

func (s *Server) getStats(w http.ResponseWriter, r *http.Request) {
	st := s.app.Node().GetStats()
	writeJSON(w, map[string]any{
		"tx_bytes":     st.TxBytes,
		"rx_bytes":     st.RxBytes,
		"tx_rate":      st.TxRate,
		"rx_rate":      st.RxRate,
		"conns":        st.Conns,
		"exit_clients": st.ExitClients,
	})
}

func (s *Server) getNode(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Store().Get()
	capOK, _ := app.CheckCapability()
	limited := !capOK
	writeJSON(w, map[string]any{
		"node_id": cfg.NodeID,
		// `name` was the legacy display label kept divergent from
		// node_id. The schema collapsed those — front-end consumers
		// that still read `name` keep working, just always equal to
		// node_id now.
		"name":          cfg.NodeID,
		"exit_node":     cfg.ExitNode,
		"server":        cfg.Server,
		"version":       Version,
		"limited":       limited,
		"compat":        app.IsCompatMode(),
		"hy2_user_auth": cfg.Hy2UserAuth,
		"active_paths":  s.app.AllActivePaths(),
		// peers below this lack lossless quality/aggregate (UI marks them)
		"lossless_min_version": app.MsessMinVersion,
	})
}

func (s *Server) updateNode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NodeID      *string           `json:"node_id"`
		Name        *string           `json:"name"`
		ExitNode    *bool             `json:"exit_node"`
		Server      *app.ServerConfig `json:"server"`
		Hy2UserAuth *bool             `json:"hy2_user_auth"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	// Validate server password
	if body.Server != nil && body.Server.Listen != "" && len(body.Server.Password) < 6 {
		http.Error(w, "server password must be at least 6 characters", 400)
		return
	}
	// The hy2 relay password is broadcast to every other peer via the
	// clients list — sharing it with a SOCKS5/HTTP user account would
	// give that user full peer-level relay access. Reject the collision
	// at save time on both directions.
	if body.Server != nil && body.Server.Password != "" {
		curUsers := s.app.Store().Get().Users
		for _, u := range curUsers {
			if u.Password == body.Server.Password {
				http.Error(w, fmt.Sprintf("server password collides with user %q — pick a different password", u.Username), 400)
				return
			}
		}
	}
	oldID := s.app.Store().Get().NodeID

	s.app.Store().Update(func(c *app.Config) {
		// node_id is the single identity. Accept either body field for
		// backward compat with old clients that still send `name`.
		if body.NodeID != nil && *body.NodeID != "" {
			c.NodeID = *body.NodeID
		} else if body.Name != nil && *body.Name != "" {
			c.NodeID = *body.Name
		}
		if body.ExitNode != nil {
			c.ExitNode = *body.ExitNode
		}
		if body.Server != nil {
			if body.Server.Listen == "" && body.Server.Password == "" {
				c.Server = nil
			} else {
				c.Server = body.Server
			}
		}
		if body.Hy2UserAuth != nil {
			c.Hy2UserAuth = *body.Hy2UserAuth
		}
	})

	cfg := s.app.Store().Get()
	oldWireName := s.app.Node().Name()
	s.app.Node().SetName(cfg.NodeID)
	s.app.Node().SetNodeID(cfg.NodeID)
	s.app.Node().SetExit(cfg.ExitNode)

	needReconnect := false
	if oldID != cfg.NodeID {
		s.app.PersistNodeID(cfg.NodeID)
		s.mu.Lock()
		s.oldNodeIDs[oldID] = cfg.NodeID
		s.mu.Unlock()
		needReconnect = true
	}
	// Reconnect if the wire-name (== NodeID after the merge) changed,
	// so peers see the new identity.
	if cfg.NodeID != oldWireName {
		needReconnect = true
	}
	// Hot-restart server if server config or identity changed
	if needReconnect || body.Server != nil {
		go func() {
			if needReconnect {
				s.app.ReconnectAll()
			}
			s.app.RestartServer()
		}()
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) getPeers(w http.ResponseWriter, r *http.Request) {
	peers := s.app.Node().Peers()
	cfg := s.app.Store().Get()
	type peerResp struct {
		Name      string `json:"name"`
		ExitNode  bool   `json:"exit_node"`
		Direction string `json:"direction"`
		Nested    bool   `json:"nested"`
	}
	result := make([]peerResp, 0, len(peers))
	for _, p := range peers {
		nested := false
		if pc, ok := cfg.Peers[p.Name]; ok {
			nested = pc.Nested
		}
		result = append(result, peerResp{
			Name:      p.Name,
			ExitNode:  p.ExitNode,
			Direction: p.Direction,
			Nested:    nested,
		})
	}
	writeJSON(w, result)
}

// internalPeers returns relay.PeerInfo list (no auth, for reverse nested discovery).
// internalPeers returns peers with cached sub-peer children (no auth, for reverse nested discovery).
func (s *Server) internalPeers(w http.ResponseWriter, r *http.Request) {
	peers := s.app.Node().Peers()
	type peerWithChildren struct {
		relay.PeerInfo
		Children []topoSubPeer `json:"children,omitempty"`
	}
	result := make([]peerWithChildren, 0, len(peers))
	for _, p := range peers {
		pc := peerWithChildren{PeerInfo: p}
		pc.Children = s.app.Tree().Cached(p.Name)
		result = append(result, pc)
	}
	writeJSON(w, result)
}

func (s *Server) getTopology(w http.ResponseWriter, r *http.Request) {
	peers := s.app.Node().Peers()
	// Alt-slot peers carry the OTHER direction in a mutually-dialed
	// (bidirectional) deployment — see relay.Node.peersAlt for the slot
	// contract. We merge them into the same maps below so both directions
	// surface in the topology JSON: top-level for outbound side, inbound
	// child of self for inbound side. The frontend then renders parallel
	// rails for the mutual link (NodesGraphView's `hasOpposite` branch).
	peersAlt := s.app.Node().PeersAlt()
	allPeers := make([]relay.PeerInfo, 0, len(peers)+len(peersAlt))
	allPeers = append(allPeers, peers...)
	allPeers = append(allPeers, peersAlt...)
	cfg := s.app.Store().Get()

	// Build client lookup for addr info
	clientMap := make(map[string]app.ClientEntry)
	for _, cl := range cfg.Clients {
		clientMap[cl.Name] = cl
	}
	connected := make(map[string]bool)
	nativeMap := make(map[string]bool)
	versionMap := make(map[string]string)
	incompatMap := make(map[string]bool)
	conflictMap := make(map[string]bool)
	unsupportedMap := make(map[string]bool)
	for _, p := range allPeers {
		connected[p.Name] = true
		if p.Native {
			nativeMap[p.Name] = true
		}
		if p.Version != "" {
			versionMap[p.Name] = p.Version
		}
		if p.Incompatible {
			incompatMap[p.Name] = true
		}
		if p.Conflict {
			conflictMap[p.Name] = true
		}
		if p.Unsupported {
			unsupportedMap[p.Name] = true
		}
	}

	type treeNode struct {
		Name         string           `json:"name"`
		Addr         string           `json:"addr,omitempty"`
		Addrs        []string         `json:"addrs,omitempty"`
		IPStatuses   []relay.IPStatus `json:"ip_statuses,omitempty"`
		ExitNode     bool             `json:"exit_node"`
		Direction    string           `json:"direction"`
		Connected    bool             `json:"connected"`
		Disabled     bool             `json:"disabled"`
		Nested       bool             `json:"nested"`
		Native       bool             `json:"native,omitempty"`
		Version      string           `json:"version,omitempty"`
		Incompatible bool             `json:"incompatible,omitempty"`
		Conflict     bool             `json:"conflict,omitempty"`
		Unsupported  bool             `json:"unsupported,omitempty"`
		TunCapable   bool             `json:"tun_capable,omitempty"`
		Conns        int              `json:"conns,omitempty"`
		LatencyMs    int              `json:"latency_ms"`
		TxRate       uint64           `json:"tx_rate"`
		RxRate       uint64           `json:"rx_rate"`
		// MaxRate (bytes/sec) — same role as topoSubPeer.MaxRate,
		// see comment there. Populated from the local cfg.Clients
		// entry's max(MaxTx, MaxRx).
		MaxRate    uint64          `json:"max_rate,omitempty"`
		IsSelf     bool            `json:"is_self,omitempty"`
		Restricted bool            `json:"restricted,omitempty"`
		Invisible  bool            `json:"invisible,omitempty"`
		Attrs      proto.Attrs     `json:"attrs,omitempty"`
		Egress     json.RawMessage `json:"egress"`
		// Punched: who set up the hole-punched connection(s) to this peer
		// (this node's name and/or the peer's).
		Punched  []string      `json:"punched,omitempty"`
		Children []topoSubPeer `json:"children,omitempty"`
	}

	disabledMap := make(map[string]bool)
	// maxRateByName: configured bandwidth per direct outbound peer.
	// max(MaxTx, MaxRx) so a duplex-capped link is represented by the
	// higher-of-two rates (the one that's actually a saturating cap
	// for the dominant traffic direction in most deployments).
	maxRateByName := make(map[string]uint64)
	for _, cl := range cfg.Clients {
		if cl.Disabled {
			disabledMap[cl.Name] = true
		}
		var m int
		if cl.MaxTx > m {
			m = cl.MaxTx
		}
		if cl.MaxRx > m {
			m = cl.MaxRx
		}
		if m > 0 {
			maxRateByName[cl.Name] = uint64(m)
		}
	}

	nameSet := make(map[string]bool)
	for _, p := range allPeers {
		nameSet[p.Name] = true
	}
	for _, cl := range cfg.Clients {
		nameSet[cl.Name] = true
	}
	names := make([]string, 0, len(nameSet))
	for n := range nameSet {
		names = append(names, n)
	}
	sort.Strings(names)

	// Read latencies from background prober (instant, non-blocking)
	latencyCache := make(map[string]int)
	for _, name := range names {
		latencyCache[name] = s.app.Node().GetLatency(name)
	}

	// Collect inbound peers as self's children
	selfServer := ""
	selfDisabled := cfg.Server == nil || cfg.Server.Listen == ""
	if cfg.Server != nil {
		selfServer = cfg.Server.Listen
	}
	selfChildren := make([]topoSubPeer, 0)
	inboundNames := make(map[string]bool)
	punched := s.app.PunchedPeers()
	for _, p := range allPeers {
		if p.Direction == "inbound" {
			inboundNames[p.Name] = true
			child := topoSubPeer{
				Name:       p.Name,
				ExitNode:   p.ExitNode,
				Direction:  "inbound",
				Via:        cfg.NodeID,
				LatencyMs:  latencyCache[p.Name],
				Version:    p.Version,
				TunCapable: p.TunCapable,
				Egress:     p.Egress,
				// Inbound peers in the live `peers` slice are connected
				// by definition — `p` was added when the inbound stream
				// registered. The new `connected` field on topoSubPeer
				// has no `omitempty`, so we MUST set it to true here or
				// the row defaults to false → list shows "offline" for
				// every active inbound peer.
				Connected: true,
				Punched:   punched[p.Name],
			}
			if pc, ok := cfg.Peers[p.Name]; ok {
				if pc.Nested {
					child.Nested = true // explicitly enabled
				}
				if pc.Disabled {
					child.Disabled = true
				}
			}
			selfChildren = append(selfChildren, child)
		}
	}
	peerRates := s.app.Node().PeerRates()
	pathRates := s.app.Node().PathRates()

	// Add per-peer traffic and sub-peers for inbound children
	for i, c := range selfChildren {
		if pr, ok := peerRates[c.Name]; ok {
			selfChildren[i].TxRate = pr.TxRate
			selfChildren[i].RxRate = pr.RxRate
		}
		if c.Nested {
			ancestors := selfAncestors(cfg, c.Name)
			children := filterAncestorPaths(s.app.Tree().Cached(c.Name), ancestors)
			// parentCumLat=latencyCache[c.Name] is self→c. The cache
			// holds the cumulative self→c→child for direct children, so
			// the depth=0 branch in filterChildrenByNestedConfig leaves
			// them alone; deeper levels (raw one-hop) get +parentCumLat.
			selfChildren[i].Children = s.filterChildrenByNestedConfig(children, c.Name, cfg, ancestors, pathRates, latencyCache[c.Name], 0)
		}
	}
	sort.Slice(selfChildren, func(i, j int) bool { return selfChildren[i].Name < selfChildren[j].Name })

	result := make([]treeNode, 0, len(names)+1)
	result = append(result, treeNode{
		Name:       cfg.NodeID,
		Addr:       selfServer,
		Direction:  "local",
		Connected:  true,
		Disabled:   selfDisabled,
		IsSelf:     true,
		LatencyMs:  0,
		TunCapable: relay.NodeTunCapable,
		Egress:     selfEgressJSON(s.app),
		Children:   selfChildren,
	})

	for _, name := range names {
		_, configuredOutbound := clientMap[name]
		// Skip inbound peers from the top-level list — they're nested
		// under self and shouldn't render twice — UNLESS this peer is
		// ALSO in our cfg.Clients (we configured it as outbound). In
		// that case the relationship is genuinely bidirectional: we
		// dial them AND they dial us. Emit both: this top-level entry
		// (outbound, from our config) plus the inbound child under
		// self (kept in selfChildren above). The frontend's addEdge
		// will produce two opposite-direction edges and the graph
		// renders them as parallel rails.
		if inboundNames[name] && !configuredOutbound {
			continue
		}
		tn := treeNode{Name: name, Connected: connected[name] && !disabledMap[name], Disabled: disabledMap[name]}
		tn.Punched = punched[name]
		if cl, ok := clientMap[name]; ok {
			tn.Addr = cl.PrimaryAddr()
			addrs := cl.AllAddrs()
			if len(addrs) > 1 {
				ipStatuses := s.app.Node().PeerIPStatuses(name)
				addrLats := s.app.Node().GetAddrLatencies(name)
				primaryAddr := cl.PrimaryAddr()
				// Ensure primary addr is included
				hasPrimary := false
				for _, s := range ipStatuses {
					if s.Addr == primaryAddr {
						hasPrimary = true
						break
					}
				}
				if !hasPrimary {
					primaryLat := latencyCache[name]
					ipStatuses = append([]relay.IPStatus{{Addr: primaryAddr, Status: "online", LatencyMs: primaryLat}}, ipStatuses...)
				}
				// Enrich with per-addr latency
				for i := range ipStatuses {
					if ms, ok := addrLats[ipStatuses[i].Addr]; ok {
						ipStatuses[i].LatencyMs = ms
					}
				}
				tn.IPStatuses = ipStatuses
				tn.Addrs = addrs
			}
			tn.Direction = "outbound"
		}
		// Pull the OUTBOUND-direction peer info for this name, since this
		// is the top-level outbound entry. In the bidirectional case the
		// outbound side may live in either primary or alt — search both.
		// If only an inbound entry is available, use it as the source of
		// metadata (ExitNode, TunCapable, etc.) but keep direction logic
		// gated by configuredOutbound below.
		for _, p := range allPeers {
			if p.Name == name {
				tn.ExitNode = p.ExitNode
				if !configuredOutbound {
					tn.Direction = p.Direction
				}
				tn.TunCapable = p.TunCapable
				tn.Conns = p.Conns
				tn.Egress = p.Egress
				tn.Restricted = p.Restricted
				tn.Invisible = p.Invisible
				tn.Attrs = p.Attrs
				if p.Direction == "outbound" {
					// Found the matching outbound entry — prefer this and
					// stop. (Continuing would overwrite with inbound
					// metadata in mutual-dial topologies.)
					break
				}
			}
		}
		tn.Native = nativeMap[name]
		tn.Version = versionMap[name]
		tn.Incompatible = incompatMap[name]
		tn.Conflict = conflictMap[name]
		tn.Unsupported = unsupportedMap[name]
		// Nested flag is always whatever the config says — config is the
		// only authority. Transient compatibility / native-binding /
		// conflict states are surfaced in their own fields (Incompatible,
		// Native, Conflict) so the UI can present a "nested but currently
		// blocked" visual rather than silently flipping the user's
		// configured nested off during a network blip.
		if pc, ok := cfg.Peers[name]; ok && pc.Nested {
			tn.Nested = true
		}
		if tn.Connected && tn.Direction == "outbound" {
			tn.LatencyMs = latencyCache[name]
		} else if !tn.Connected {
			tn.LatencyMs = -1
		}
		if pr, ok := peerRates[name]; ok {
			tn.TxRate = pr.TxRate
			tn.RxRate = pr.RxRate
		}
		if mr, ok := maxRateByName[name]; ok {
			tn.MaxRate = mr
		}

		// Load sub-peers from background cache only (never blocks).
		// Hide children when the peer's wire-protocol generation is
		// newer than ours — we may not be able to parse the frames it
		// would use to expose those sub-peers, and we don't want to
		// surface them in the UI as if they were reachable.
		if tn.Nested && tn.Connected && !tn.Native && !tn.Incompatible && !tn.Conflict && !tn.Unsupported {
			ancestors := selfAncestors(cfg, name)
			children := filterAncestorPaths(s.app.Tree().Cached(name), ancestors)
			tn.Children = s.filterChildrenByNestedConfig(children, name, cfg, ancestors, pathRates, latencyCache[name], 0)
			if tn.Restricted {
				relay.MarkRestricted(tn.Children)
			}
		}
		// Also load children for inbound peers nested under self
		// (they can have outbound connections visible if nested is enabled)
		result = append(result, tn)
	}
	writeJSON(w, result)
}

// filterChildrenByNestedConfig strips deeper children unless local nested config allows them.
// Uses path-qualified keys (parent/child) so same-name peers have independent nested state.
// When c.Nested is enabled but c.Children is empty, pulls the flat child list
// cached for the qualified path by the node's nested-discovery tree.
//
// Rule 1 (including self identity): a descendant whose name matches any
// ancestor on the path — OR matches our local node_id / name — is dropped.
// `ancestors` is mutated on descent and restored on backtrack so identical
// names on parallel sibling branches remain legal.
//
// Path-qualified traffic rates are attached per node so the Nodes UI can
// attribute relayed bytes to the specific nested descendant they're destined
// for, not only to the first-hop direct peer that physically carries them.
func (s *Server) filterChildrenByNestedConfig(children []topoSubPeer, parentName string, cfg app.Config, ancestors map[string]bool, pathRates map[string]relay.PeerTraffic, parentCumLat int, depth int) []topoSubPeer {
	if len(children) == 0 {
		return children
	}
	// Depth cap of the tree cache; binds only if the cap was lowered after
	// the cache was filled.
	if depth >= s.app.Tree().Limits().MaxDepth {
		stripped := make([]topoSubPeer, len(children))
		for i, c := range children {
			c.Children = nil
			stripped[i] = c
		}
		return stripped
	}
	result := make([]topoSubPeer, 0, len(children))
	for _, c := range children {
		if ancestors[c.Name] {
			continue
		}
		qualifiedKey := parentName + "/" + c.Name
		pc, hasPC := cfg.Peers[qualifiedKey]
		c.Nested = hasPC && pc.Nested
		c.Disabled = hasPC && pc.Disabled
		if pr, ok := pathRates[qualifiedKey]; ok {
			c.TxRate = pr.TxRate
			c.RxRate = pr.RxRate
		}
		// Latencies cumulative from self: the tree caches direct children
		// cumulative already, deeper levels one-hop.
		if depth >= 1 && c.LatencyMs > 0 && parentCumLat > 0 {
			c.LatencyMs += parentCumLat
		}
		childCumLat := c.LatencyMs
		if c.Nested {
			if len(c.Children) == 0 {
				c.Children = s.app.Tree().Cached(qualifiedKey)
			}
			if len(c.Children) > 0 {
				ancestors[c.Name] = true
				c.Children = s.filterChildrenByNestedConfig(c.Children, qualifiedKey, cfg, ancestors, pathRates, childCumLat, depth+1)
				delete(ancestors, c.Name)
			}
		} else {
			c.Children = nil
		}
		result = append(result, c)
	}
	return result
}

// selfAncestors returns the baseline ancestor set used for Rule 1 filtering.
// Always contains the local node's ID and name so neither can ever appear as
// a sub-peer at any depth.
func selfAncestors(cfg app.Config, extra ...string) map[string]bool {
	a := map[string]bool{cfg.NodeID: true}
	for _, e := range extra {
		if e != "" {
			a[e] = true
		}
	}
	return a
}

// filterAncestorPaths recursively removes any descendant whose name already
// appears in the ancestor path (including self). This prevents loops like
// "AUB/tz-cm-temp/2400/tz-cm-temp" where a descendant's peer list legitimately
// includes the upstream node, since routing through a node already in the path
// would just bounce the traffic back.
// `ancestors` is mutated during recursion (add on descent, remove on backtrack)
// so the same name IS allowed in parallel sibling branches.
func filterAncestorPaths(children []topoSubPeer, ancestors map[string]bool) []topoSubPeer {
	if len(children) == 0 {
		return children
	}
	filtered := make([]topoSubPeer, 0, len(children))
	for _, c := range children {
		if ancestors[c.Name] {
			continue
		}
		ancestors[c.Name] = true
		c.Children = filterAncestorPaths(c.Children, ancestors)
		delete(ancestors, c.Name)
		filtered = append(filtered, c)
	}
	return filtered
}

func (s *Server) getNestedPeers(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	peers, err := s.app.Node().PeersOf(name)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, peers)
}

func (s *Server) setPeerDisabled(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		Disabled bool `json:"disabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := s.app.SetPeerDisabled(name, body.Disabled); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) setNested(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := s.app.SetNested(name, body.Enabled); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.app.Tree().Kick()
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) getClients(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Store().Get()
	connected := make(map[string]bool)
	for _, p := range s.app.Node().Peers() {
		connected[p.Name] = true
	}
	type clientResp struct {
		app.ClientEntry
		Connected bool `json:"connected"`
	}
	result := make([]clientResp, 0, len(cfg.Clients))
	for _, cl := range cfg.Clients {
		result = append(result, clientResp{
			ClientEntry: cl,
			Connected:   connected[cl.Name] && !cl.Disabled,
		})
	}
	writeJSON(w, result)
}

func (s *Server) getClient(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	cfg := s.app.Store().Get()
	for _, cl := range cfg.Clients {
		if cl.Name == name || cl.Addr == name {
			writeJSON(w, cl)
			return
		}
	}
	http.Error(w, "not found", 404)
}

func (s *Server) updateClient(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var cl app.ClientEntry
	if err := json.NewDecoder(r.Body).Decode(&cl); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	// Sync Addrs ↔ Addr
	if len(cl.Addrs) > 0 {
		cl.Addr = cl.Addrs[0]
	} else if cl.Addr != "" && !app.IsPunchAddr(cl.Addr) {
		cl.Addrs = []string{cl.Addr}
	}
	if cl.Name == "" {
		cl.Name = name
	}
	if err := normalizeClient(&cl); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	// Look up by URL path name, update with body (may include rename)
	oldName := name
	if cl.Name == "" {
		cl.Name = name
	}
	if err := s.app.UpdateClientByAddr(oldName, cl); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) addClient(w http.ResponseWriter, r *http.Request) {
	var cl app.ClientEntry
	if err := json.NewDecoder(r.Body).Decode(&cl); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	// Sync Addrs ↔ Addr for backward compat
	if len(cl.Addrs) > 0 {
		cl.Addr = cl.Addrs[0]
	} else if cl.Addr != "" && !app.IsPunchAddr(cl.Addr) {
		cl.Addrs = []string{cl.Addr}
	}
	if cl.Name == "" {
		http.Error(w, "name required", 400)
		return
	}
	if err := normalizeClient(&cl); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := s.app.AddClient(cl); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) disableClient(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		Disabled bool `json:"disabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := s.app.SetClientDisabled(name, body.Disabled); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) removeClient(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.app.RemoveClient(name); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// normalizeClient validates a node entry: manual addresses and/or punch
// routes. An entry with punch routes only is keyed by its first punch route.
func normalizeClient(cl *app.ClientEntry) error {
	if p := cl.Punch; p != nil {
		if len(p.Signal) == 0 && len(p.Reflectors) == 0 && len(p.Binds) == 0 {
			cl.Punch = nil
		} else {
			if len(p.Signal) == 0 {
				return fmt.Errorf("punch needs at least one signalling path")
			}
			for _, path := range p.Signal {
				hops := strings.Split(path, "/")
				if hops[len(hops)-1] != cl.Name {
					return fmt.Errorf("signalling path %q must end at %q", path, cl.Name)
				}
				for _, h := range hops {
					if h == "" {
						return fmt.Errorf("invalid signalling path %q", path)
					}
				}
			}
			if len(p.Reflectors) == 0 {
				return fmt.Errorf("punch needs at least one reflector")
			}
			for _, r := range p.Reflectors {
				if r == "" || strings.Contains(r, "/") || r == cl.Name {
					return fmt.Errorf("invalid reflector %q", r)
				}
			}
			if !app.ValidPunchFamily(p.Family) {
				return fmt.Errorf("invalid punch family %q", p.Family)
			}
			for _, b := range p.Binds {
				if b == "" {
					continue
				}
				_, _, src, err := app.SplitAddrBind("x:1#" + b)
				if err != nil {
					return err
				}
				if src != "" && (strings.Contains(src, ":") != (p.Family == app.PunchFamilyIPv6)) {
					return fmt.Errorf("punch source %s does not match family %s", src, punchFamilyName(p.Family))
				}
			}
		}
	}
	manual := cl.Addrs
	if len(manual) == 0 && cl.Addr != "" && !app.IsPunchAddr(cl.Addr) {
		manual = []string{cl.Addr}
	}
	for _, a := range manual {
		if !validAddrSpec(a) || app.IsPunchAddr(a) {
			return fmt.Errorf("invalid address format %q", a)
		}
	}
	switch {
	case len(manual) > 0:
		cl.Addr, cl.Addrs = manual[0], manual
		if cl.Password == "" {
			return fmt.Errorf("password required")
		}
	case cl.Punch != nil:
		// punch only: the token is the password; the key is the first route
		cl.Addrs = nil
		if !app.IsPunchAddr(cl.Addr) {
			cl.Addr = cl.AllAddrs()[0]
		}
	default:
		return fmt.Errorf("an address or a punch route is required")
	}
	return nil
}

// validAddrSpec checks "host:portspec" where portspec can be "5565", "1000,2000", "20000-30000".
// An IPv6 host must be bracketed ("[2001:db8::1]:5565"); a bare IPv6
// address is ambiguous with the port and is refused.
func validAddrSpec(addr string) bool {
	if i := strings.LastIndexByte(addr, '#'); i >= 0 {
		if _, _, _, err := app.SplitAddrBind(addr); err != nil {
			return false
		}
		addr = addr[:i]
	}
	// SplitHostPort refuses unbracketed IPv6 ("too many colons") and
	// unbalanced brackets; the port part may be a list / ranges.
	host, portSpec, err := net.SplitHostPort(addr)
	if err != nil || host == "" || portSpec == "" {
		return false
	}
	if strings.ContainsAny(host, "[]/# ") {
		return false
	}
	if strings.Contains(host, ":") || strings.HasPrefix(addr, "[") {
		// bracketed: must be an IPv6 address (with an optional zone)
		h, _, _ := strings.Cut(host, "%")
		if net.ParseIP(h) == nil {
			return false
		}
	}
	for _, part := range strings.Split(portSpec, ",") {
		part = strings.TrimSpace(part)
		if strings.Contains(part, "-") {
			rng := strings.SplitN(part, "-", 2)
			a, err1 := strconv.Atoi(strings.TrimSpace(rng[0]))
			b, err2 := strconv.Atoi(strings.TrimSpace(rng[1]))
			if err1 != nil || err2 != nil || a < 1 || b > 65535 || a > b {
				return false
			}
		} else {
			n, err := strconv.Atoi(part)
			if err != nil || n < 1 || n > 65535 {
				return false
			}
		}
	}
	return true
}

func punchFamilyName(f string) string {
	if f == "" {
		return app.PunchFamilyIPv4
	}
	return f
}
