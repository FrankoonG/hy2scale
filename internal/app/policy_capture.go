package app

import (
	"context"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/FrankoonG/hy2scale/core/dnsproxy"
	"github.com/FrankoonG/hy2scale/core/policy"
	"github.com/FrankoonG/hy2scale/core/policy/capture"
	"github.com/FrankoonG/hy2scale/core/sockmark"
)

// System traffic capture (core/policy/capture, spec §3) and the DNS proxy
// (core/dnsproxy): started with the node, re-applied on every policy
// change, removed on exit (block-on-stop layers stay).

func init() {
	// hy2scale's own sockets carry the socket mark: the default resolver
	// and HTTP transport too (geoip downloads, upgrade checks, tools).
	sockmark.Install()
	OnPolicyChange(func() {
		if a := captureApp.Load(); a != nil {
			a.reloadPolicyCapture()
		}
	})
}

var captureApp atomic.Pointer[App]

var (
	captureOnce sync.Once
	captureMgr  *capture.Manager
	dnsProxy    *dnsproxy.Proxy
)

func (a *App) captureManager() *capture.Manager {
	captureOnce.Do(func() {
		captureMgr = capture.New()
		dnsProxy = dnsproxy.New(dnsproxy.Config{
			Upstreams: a.dnsProxyUpstreams,
			Dial:      a.dnsProxyDial,
		})
		SetPolicyDomainLookup(dnsProxy.DomainOf)
	})
	return captureMgr
}

// startPolicyCapture installs the capture (and first removes what a
// previous process, 1.3.x included, left behind).
func (a *App) startPolicyCapture() {
	cleanupV13Rules()
	a.captureManager()
	captureApp.Store(a)
	a.reloadPolicyCapture()
}

// stopPolicyCapture removes the capture at exit.
func (a *App) stopPolicyCapture() {
	captureApp.Store(nil)
	a.captureManager().Stop()
}

// policyCaptureRecover is deferred in Run: a panic there removes the
// capture before it propagates (panics elsewhere: the heartbeat expires).
func (a *App) policyCaptureRecover() {
	if r := recover(); r != nil {
		a.captureManager().Stop()
		panic(r)
	}
}

var captureApplyMu sync.Mutex

// reloadPolicyCapture re-applies the system settings.
func (a *App) reloadPolicyCapture() {
	captureApplyMu.Lock()
	defer captureApplyMu.Unlock()
	cfg := a.store.Get()
	sys := policy.DefaultSystem()
	if cfg.Policy != nil {
		sys = cfg.Policy.System
	}
	err := a.captureManager().Apply(capture.Options{
		System:    sys,
		Runtime:   a.PolicyRuntime(),
		DNS:       dnsProxy,
		MeshAddrs: a.meshAddrs,
	})
	if err != nil {
		log.Printf("[capture] %v", err)
	}
}

// meshAddrs: the node's server listen address and every outbound peer
// address — never captured.
func (a *App) meshAddrs() (string, []string) {
	cfg := a.store.Get()
	var listen string
	if cfg.Server != nil {
		listen = stripAddrBind(cfg.Server.Listen)
	}
	var peers []string
	for _, cl := range cfg.Clients {
		for _, ad := range cl.AllAddrs() {
			if !IsPunchAddr(ad) {
				peers = append(peers, stripAddrBind(ad))
			}
		}
	}
	return listen, peers
}

// dnsProxyUpstreams: the node's DNS servers (the resolver setting); with
// the resolver off, the system's (loopback ones skipped: a local stub
// resolver's own queries would come back to the proxy).
func (a *App) dnsProxyUpstreams() []string {
	cfg := a.store.Get()
	if cfg.DNSResolver.IsEnabled() {
		if u := dnsproxy.ParseUpstreams(cfg.DNS); len(u) > 0 {
			return u
		}
		return dnsproxy.ParseUpstreams(dnsproxy.DefaultUpstreams)
	}
	var out []string
	b, _ := os.ReadFile("/etc/resolv.conf")
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "nameserver" {
			if ip := net.ParseIP(f[1]); ip != nil && !ip.IsLoopback() {
				out = append(out, net.JoinHostPort(ip.String(), "53"))
			}
		}
	}
	if len(out) == 0 {
		out = dnsproxy.ParseUpstreams(dnsproxy.DefaultUpstreams)
	}
	return out
}

// dnsProxyDial sends a query upstream as the client would go: through the
// policy from the direct entry.
func (a *App) dnsProxyDial(ctx context.Context, c *policy.Conn, network, upstream string) (net.Conn, error) {
	u := dnsproxy.UpstreamConn(c, network, upstream)
	rt := a.PolicyRuntime()
	var conn net.Conn
	var err error
	if network == "tcp" {
		conn, _, err = rt.DialTCP(ctx, u, policy.Direct)
	} else {
		conn, _, err = rt.DialUDP(ctx, u, policy.Direct)
	}
	return conn, err
}

// PolicyCapabilities: what the system capture can do here (geoip and the
// iface terminal are filled in by the API).
func (a *App) PolicyCapabilities() capture.Capabilities { return a.captureManager().Capabilities() }

// PolicyCaptureStatus: the capture's current state.
func (a *App) PolicyCaptureStatus() capture.Status { return a.captureManager().Status() }

// PolicyInterfaces: the interfaces layer 2 can take traffic from.
func (a *App) PolicyInterfaces() []capture.Interface {
	return a.captureManager().Interfaces(a.isChildIface)
}
