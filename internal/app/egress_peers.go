package app

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/FrankoonG/hy2scale/internal/pubip"
)

// egressPeersLoop fetches every capable direct peer's egress so the node
// list can show where each node's direct traffic leaves from; the peer
// lists we serve carry it on to nodes further away.
func (a *App) egressPeersLoop(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		live := map[string]bool{}
		for _, p := range append(a.node.Peers(), a.node.PeersAlt()...) {
			live[p.Name] = true
			if !p.EgressCapable {
				continue
			}
			go func(name string) {
				b, err := a.node.EgressOf(name)
				if err != nil || !json.Valid(b) {
					return
				}
				a.node.SetPeerEgress(name, json.RawMessage(b))
			}(p.Name)
		}
		for _, cl := range a.store.Get().Clients {
			if !live[cl.Name] {
				a.node.SetPeerEgress(cl.Name, nil)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// peerReportedAddr asks a peer this node dialled through iface which
// address it sees us at ("peer+public" detection). Only outbound peers
// whose primary connection leaves through iface in family fam qualify.
func (a *App) peerReportedAddr(ctx context.Context, iface string, fam int) pubip.Result {
	res := pubip.Result{Checked: time.Now()}
	capable := map[string]bool{}
	for _, p := range a.node.Peers() {
		if p.EgressCapable && p.Direction == "outbound" {
			capable[p.Name] = true
		}
	}
	defIface := defaultRouteIface(fam)
	for _, cl := range a.store.Get().Clients {
		if cl.Disabled || !capable[cl.Name] {
			continue
		}
		addr := cl.Addr
		if v, ok := a.primaryDial.Load(cl.Addr); ok {
			addr = v.(string)
		}
		host, b, err := splitAddrBind(addr)
		if err != nil {
			continue
		}
		via := b.iface
		if via == "" {
			via = defIface
		}
		if via != iface {
			continue
		}
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil && ip.Is4() != (fam == 4) {
			continue
		}
		s, err := a.node.WhoAmI(cl.Name)
		if err != nil {
			res.Err = err
			continue
		}
		ap, err := netip.ParseAddrPort(strings.TrimSpace(s))
		if err != nil {
			res.Err = err
			continue
		}
		ip := ap.Addr().Unmap()
		if ip.Is4() != (fam == 4) {
			continue
		}
		res.IP, res.Server, res.Err = ip, "peer:"+cl.Name, nil
		return res
	}
	if res.Err == nil {
		res.Err = errors.New("no peer connection through this interface")
	}
	_ = ctx
	return res
}
