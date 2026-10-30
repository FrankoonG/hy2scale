//go:build linux

package vnic

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/network/arp"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/icmp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

const nicID tcpip.NICID = 1

// slaacGrace is how long IPv6 SLAAC may stay without an address before the
// family is reported as failed (it keeps listening for advertisements).
const slaacGrace = 30 * time.Second

// Iface is a running built-in child interface.
type Iface struct {
	cfg   Config
	mac   [6]byte
	mode4 string
	mode6 string
	s     *stack.Stack
	link  *linkEP
	ndp   *ndpState
	dhcp  *dhcpClient
	ctl   int // AF_UNIX socket for interface ioctls

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu        sync.Mutex
	up        bool
	perr      string
	upSince   time.Time
	v4        FamilyStatus
	v4routes  []dhcpRoute
	v4any     bool // 0.0.0.0 assigned for DHCP
	v6err     string
	groups    map[[6]byte]bool
	announced map[netip.Addr]bool
	lastSig   string
	closed    bool
	started   time.Time

	kickCh    chan struct{}
	notifyCh  chan struct{}
	closeOnce sync.Once
	reopens   atomic.Int32
}

func validate(cfg *Config) error {
	if cfg.Name == "" {
		return errors.New("vnic: name required")
	}
	if cfg.Parent == "" {
		return errors.New("vnic: parent required")
	}
	if len(cfg.MAC) != 6 || cfg.MAC[0]&1 != 0 || string(cfg.MAC) == "\x00\x00\x00\x00\x00\x00" {
		return fmt.Errorf("vnic: invalid MAC %q (need a unicast 48-bit address)", cfg.MAC)
	}
	if cfg.VLAN < 0 || cfg.VLAN > 4094 {
		return fmt.Errorf("vnic: invalid VLAN %d", cfg.VLAN)
	}
	if cfg.MTU != 0 && (cfg.MTU < 576 || cfg.MTU > 65535) {
		return fmt.Errorf("vnic: invalid MTU %d", cfg.MTU)
	}
	switch modeOf(cfg.IPv4.Mode) {
	case "off", "dhcp", "static":
	default:
		return fmt.Errorf("vnic: invalid IPv4 mode %q", cfg.IPv4.Mode)
	}
	switch modeOf(cfg.IPv6.Mode) {
	case "off", "slaac", "static":
	default:
		return fmt.Errorf("vnic: invalid IPv6 mode %q", cfg.IPv6.Mode)
	}
	for _, p := range cfg.IPv4.Addrs {
		if !p.IsValid() || !p.Addr().Is4() {
			return fmt.Errorf("vnic: invalid IPv4 address %v", p)
		}
	}
	for _, p := range cfg.IPv6.Addrs {
		if !p.IsValid() || !p.Addr().Is6() || p.Addr().Is4In6() {
			return fmt.Errorf("vnic: invalid IPv6 address %v", p)
		}
	}
	return nil
}

// Start creates the interface and brings it up in the background: a missing
// or down parent is not an error (Status reports it and the interface keeps
// retrying). Cancelling ctx closes the interface like Close.
func Start(ctx context.Context, cfg Config) (*Iface, error) {
	if err := validate(&cfg); err != nil {
		return nil, err
	}
	i := &Iface{
		cfg:      cfg,
		mode4:    modeOf(cfg.IPv4.Mode),
		mode6:    modeOf(cfg.IPv6.Mode),
		kickCh:   make(chan struct{}, 1),
		notifyCh: make(chan struct{}, 1),
		groups:   map[[6]byte]bool{},
		started:  time.Now(),
		ctl:      -1,
	}
	copy(i.mac[:], cfg.MAC)
	var err error
	if i.ctl, err = unix.Socket(unix.AF_UNIX, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0); err != nil {
		return nil, fmt.Errorf("vnic: control socket: %w", err)
	}
	i.link = newLinkEP(i.mac, cfg.VLAN, vnetSupported())
	if i.mode6 != "off" {
		i.ndp = &ndpState{kick: i.kick, clock: nil}
	}
	i.s = i.newStack()
	if i.ndp != nil {
		i.ndp.clock = i.s.Clock()
	}
	if terr := i.s.CreateNICWithOptions(nicID, i.link, stack.NICOptions{Name: cfg.Name}); terr != nil {
		unix.Close(i.ctl)
		return nil, fmt.Errorf("vnic: create NIC: %s", terr)
	}
	// no listeners: unsolicited TCP / UDP is dropped without RST / ICMP
	drop := func(stack.TransportEndpointID, *stack.PacketBuffer) bool { return true }
	i.s.SetTransportProtocolHandler(tcp.ProtocolNumber, drop)
	i.s.SetTransportProtocolHandler(udp.ProtocolNumber, drop)

	i.v4 = FamilyStatus{State: StateOff}
	switch i.mode4 {
	case "static":
		i.v4.State = StateOK
		if len(cfg.IPv4.Addrs) == 0 {
			i.v4.State, i.v4.Error = StateFailed, "no address configured"
		}
		for _, p := range cfg.IPv4.Addrs {
			if err := i.addAddr(p); err != nil {
				i.v4.State, i.v4.Error = StateFailed, err.Error()
			}
			i.v4.Addrs = append(i.v4.Addrs, AddrInfo{Prefix: p, Source: "static"})
		}
		i.v4.Gateway = cfg.IPv4.Gateway
		i.v4.DNS = slices.Clone(cfg.IPv4.DNS)
	case "dhcp":
		i.v4.State = StateAcquiring
	}
	if i.mode6 == "static" {
		for _, p := range cfg.IPv6.Addrs {
			if err := i.addAddr(p); err != nil {
				i.v6err = err.Error()
			}
		}
	}

	i.ctx, i.cancel = context.WithCancel(context.Background())
	i.wg.Add(3)
	go i.linkLoop()
	go i.controlLoop()
	go i.notifier()
	if i.mode4 == "dhcp" {
		i.dhcp = newDHCPClient(i)
		go i.dhcp.run()
	}
	go func() {
		select {
		case <-ctx.Done():
			i.Close()
		case <-i.ctx.Done():
		}
	}()
	i.kick()
	return i, nil
}

func (i *Iface) newStack() *stack.Stack {
	opts := stack.Options{}
	if i.mode4 != "off" {
		opts.NetworkProtocols = append(opts.NetworkProtocols, ipv4.NewProtocol, arp.NewProtocol)
		opts.TransportProtocols = append(opts.TransportProtocols, icmp.NewProtocol4)
	}
	if i.mode6 != "off" {
		ndpc := ipv6.DefaultNDPConfigurations()
		ndpc.HandleRAs = ipv6.HandlingRAsDisabled
		ndpc.DiscoverDefaultRouters = false
		ndpc.DiscoverOnLinkPrefixes = false
		ndpc.AutoGenGlobalAddresses = false
		if i.mode6 == "slaac" {
			ndpc.HandleRAs = ipv6.HandlingRAsAlwaysEnabled
			ndpc.DiscoverDefaultRouters = true
			ndpc.DiscoverOnLinkPrefixes = true
			ndpc.AutoGenGlobalAddresses = true
			ndpc.AutoGenTempGlobalAddresses = false // one stable (EUI-64) address per prefix
			ndpc.AutoGenAddressConflictRetries = 3
		}
		opts.NetworkProtocols = append(opts.NetworkProtocols, ipv6.NewProtocolWithOptions(ipv6.Options{
			NDPConfigs:       ndpc,
			AutoGenLinkLocal: true,
			NDPDisp:          i.ndp,
			MLD:              ipv6.MLDOptions{Enabled: true},
			DADConfigs:       stack.DADConfigurations{DupAddrDetectTransmits: 1, RetransmitTimer: time.Second},
		}))
		opts.TransportProtocols = append(opts.TransportProtocols, icmp.NewProtocol6)
	}
	opts.TransportProtocols = append(opts.TransportProtocols, tcp.NewProtocol, udp.NewProtocol)
	s := stack.New(opts)
	// the VPN netstack's tuning (internal/app/vpn_stack.go) + receive auto-tuning
	sack := tcpip.TCPSACKEnabled(true)
	s.SetTransportProtocolOption(tcp.ProtocolNumber, &sack)
	rcv := tcpip.TCPReceiveBufferSizeRangeOption{Min: 4 << 10, Default: 1 << 20, Max: 8 << 20}
	s.SetTransportProtocolOption(tcp.ProtocolNumber, &rcv)
	snd := tcpip.TCPSendBufferSizeRangeOption{Min: 4 << 10, Default: 1 << 20, Max: 8 << 20}
	s.SetTransportProtocolOption(tcp.ProtocolNumber, &snd)
	mod := tcpip.TCPModerateReceiveBufferOption(true)
	s.SetTransportProtocolOption(tcp.ProtocolNumber, &mod)
	return s
}

func protoOf(a netip.Addr) tcpip.NetworkProtocolNumber {
	if a.Is4() {
		return ipv4.ProtocolNumber
	}
	return ipv6.ProtocolNumber
}

func tcpipAddr(a netip.Addr) tcpip.Address {
	return tcpip.AddrFromSlice(a.Unmap().AsSlice())
}

func (i *Iface) addAddr(p netip.Prefix) error {
	pa := tcpip.ProtocolAddress{Protocol: protoOf(p.Addr()), AddressWithPrefix: tcpip.AddressWithPrefix{Address: tcpipAddr(p.Addr()), PrefixLen: p.Bits()}}
	if terr := i.s.AddProtocolAddress(nicID, pa, stack.AddressProperties{PEB: stack.CanBePrimaryEndpoint}); terr != nil {
		if _, dup := terr.(*tcpip.ErrDuplicateAddress); !dup {
			return fmt.Errorf("add %v: %s", p, terr)
		}
	}
	return nil
}

func (i *Iface) removeAddr(a netip.Addr) {
	i.s.RemoveAddress(nicID, tcpipAddr(a))
}

func (i *Iface) kick() {
	select {
	case i.kickCh <- struct{}{}:
	default:
	}
}

func (i *Iface) sleep(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-i.ctx.Done():
		return false
	}
}

// ---------------------------------------------------------------- link supervision

// setLinkErr records a down link without waking the control loop when
// nothing changed (the wait loop calls it every 25 ms).
func (i *Iface) setLinkErr(perr string) {
	i.mu.Lock()
	same := !i.up && i.perr == perr
	i.mu.Unlock()
	if !same {
		i.setLink(false, perr)
	}
}

func (i *Iface) setLink(up bool, perr string) {
	i.mu.Lock()
	if up && !i.up {
		i.upSince = time.Now()
	}
	i.up, i.perr = up, perr
	i.mu.Unlock()
	i.kick()
}

// linkLoop keeps the packet sockets open on the parent; when the parent goes
// away (deleted, down, ifindex changed) or the socket fails it reopens them
// with the NIC, addresses and leases untouched, and flushes the neighbour
// cache so a changed upstream MAC is learned again at once.
func (i *Iface) linkLoop() {
	defer i.wg.Done()
	p := openParams{parent: i.cfg.Parent, mac: i.mac, vlan: i.cfg.VLAN, v6: i.mode6 != "off", gso: i.link.gso, mtu: i.cfg.MTU}
	opened := false
	wait := 100 * time.Millisecond
	for i.ctx.Err() == nil {
		// cheap wait for the parent to exist and be up before opening sockets
		if _, ifUp, err := ifindexOf(i.ctl, p.parent); err != nil || !ifUp {
			if err != nil {
				i.setLinkErr(fmt.Sprintf("parent interface %q not found", p.parent))
			} else {
				i.setLinkErr(fmt.Sprintf("parent interface %q is down", p.parent))
			}
			if !i.sleep(25 * time.Millisecond) {
				return
			}
			continue
		}
		sk, mtu, err := openSock(p)
		if err != nil {
			i.setLinkErr(err.Error())
			if errors.Is(err, errNoPermission) {
				wait = min(wait*2, 10*time.Second)
			} else {
				wait = 100 * time.Millisecond
			}
			if !i.sleep(wait) {
				return
			}
			continue
		}
		wait = 100 * time.Millisecond
		i.link.setSock(sk, mtu)
		if opened {
			i.reopens.Add(1)
			if i.mode4 != "off" {
				i.s.ClearNeighbors(nicID, ipv4.ProtocolNumber)
			}
			if i.mode6 != "off" {
				i.s.ClearNeighbors(nicID, ipv6.ProtocolNumber)
			}
		}
		opened = true
		i.mu.Lock()
		groups := i.groups
		i.mu.Unlock()
		i.link.setGroups(groups)
		i.mu.Lock()
		i.announced = nil
		i.mu.Unlock()
		i.setLink(true, "")

		openedAt := time.Now()
		rxDone := make(chan error, 1)
		go func() { rxDone <- sk.rxLoop(i.link) }()
		reason, got := i.watchLink(sk, rxDone)
		sk.stop()
		if !got {
			<-rxDone
		}
		i.link.setSock(nil, 0) // waits for in-flight writes
		sk.closeFDs()
		if i.ctx.Err() != nil {
			return
		}
		i.setLink(false, reason)
		if time.Since(openedAt) < time.Second && !i.sleep(250*time.Millisecond) {
			return // a socket that keeps failing at once: do not spin
		}
	}
}

// watchLink waits until the open sockets have to be replaced. got reports
// whether rxDone was consumed.
func (i *Iface) watchLink(sk *sock, rxDone chan error) (reason string, got bool) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	check := func() string {
		idx, up, err := ifindexOf(i.ctl, i.cfg.Parent)
		switch {
		case err != nil:
			return fmt.Sprintf("parent interface %q not found", i.cfg.Parent)
		case idx != sk.ifindex:
			return fmt.Sprintf("parent interface %q was recreated", i.cfg.Parent)
		case !up:
			return fmt.Sprintf("parent interface %q is down", i.cfg.Parent)
		}
		return ""
	}
	for {
		select {
		case <-i.ctx.Done():
			return "", false
		case err := <-rxDone:
			if r := check(); r != "" {
				return r, true
			}
			if err == nil {
				err = errors.New("receive loop stopped")
			}
			return err.Error(), true
		case <-i.link.txErrCh:
			if r := check(); r != "" {
				return r, false
			}
		case <-tick.C:
			if r := check(); r != "" {
				return r, false
			}
		}
	}
}

// ---------------------------------------------------------------- control

// controlLoop applies routes / multicast groups and computes the IPv6 state
// whenever something changed (and periodically, for lifetimes).
func (i *Iface) controlLoop() {
	defer i.wg.Done()
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-i.ctx.Done():
			return
		case <-i.kickCh:
		case <-t.C:
		}
		i.refresh()
	}
}

func (i *Iface) refresh() {
	var routes []tcpip.Route
	groups := map[[6]byte]bool{}
	if i.mode6 != "off" {
		groups[[6]byte{0x33, 0x33, 0, 0, 0, 1}] = true
		for _, pa := range i.s.AllAddresses()[nicID] {
			if pa.Protocol != ipv6.ProtocolNumber {
				continue
			}
			a := pa.AddressWithPrefix.Address.As16()
			groups[[6]byte{0x33, 0x33, 0xff, a[13], a[14], a[15]}] = true
		}
	}
	i.mu.Lock()
	routes = i.routesLocked()
	changedGroups := !mapsEqual(groups, i.groups)
	if changedGroups {
		i.groups = groups
	}
	st := i.statusLocked(false)
	sig := fmt.Sprint(st)
	changed := sig != i.lastSig
	i.lastSig = sig
	var fresh []netip.Addr
	if i.up {
		if i.announced == nil {
			i.announced = map[netip.Addr]bool{}
		}
		cur := map[netip.Addr]bool{}
		for _, a := range st.IPv4.Addrs {
			cur[a.Prefix.Addr()] = true
		}
		if st.IPv6.State == StateOK {
			for _, a := range st.IPv6.Addrs {
				cur[a.Prefix.Addr()] = true
			}
		}
		for a := range cur {
			if !i.announced[a] {
				fresh = append(fresh, a)
			}
		}
		i.announced = cur
	}
	i.mu.Unlock()

	if len(fresh) > 0 {
		send := func() {
			for _, a := range fresh {
				i.link.announce(a.As16(), a.Is4())
			}
		}
		send()
		time.AfterFunc(time.Second, send) // one more, in case the first is lost
	}

	i.s.SetRouteTable(routes)
	if changedGroups {
		i.link.setGroups(groups)
	}
	if changed {
		select {
		case i.notifyCh <- struct{}{}:
		default:
		}
	}
}

func mapsEqual(a, b map[[6]byte]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func (i *Iface) notifier() {
	defer i.wg.Done()
	for {
		select {
		case <-i.ctx.Done():
			return
		case <-i.notifyCh:
		}
		if f := i.cfg.OnChange; f != nil {
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("[vnic] %s: OnChange panic: %v", i.cfg.Name, r)
					}
				}()
				f()
			}()
		}
	}
}

func (i *Iface) routesLocked() []tcpip.Route {
	var rs []tcpip.Route
	onlink := func(p netip.Prefix) {
		p = p.Masked()
		sn, err := tcpip.NewSubnet(tcpipAddr(p.Addr()), tcpip.MaskFromBytes(net.CIDRMask(p.Bits(), p.Addr().BitLen())))
		if err == nil {
			rs = append(rs, tcpip.Route{Destination: sn, NIC: nicID})
		}
	}
	via := func(p netip.Prefix, gw netip.Addr) {
		p = p.Masked()
		sn, err := tcpip.NewSubnet(tcpipAddr(p.Addr()), tcpip.MaskFromBytes(net.CIDRMask(p.Bits(), p.Addr().BitLen())))
		if err == nil {
			rs = append(rs, tcpip.Route{Destination: sn, Gateway: tcpipAddr(gw), NIC: nicID})
		}
	}
	covered := func(gw netip.Addr, addrs []AddrInfo) bool {
		for _, a := range addrs {
			if a.Prefix.Masked().Contains(gw) {
				return true
			}
		}
		return false
	}
	if i.mode4 != "off" {
		for _, a := range i.v4.Addrs {
			onlink(a.Prefix)
		}
		for _, r := range i.v4routes {
			if r.Via.IsUnspecified() {
				onlink(r.Dst)
			} else if r.Dst.Bits() > 0 {
				via(r.Dst, r.Via)
			}
		}
		if gw := i.v4.Gateway; gw.IsValid() && len(i.v4.Addrs) > 0 {
			if !covered(gw, i.v4.Addrs) {
				onlink(netip.PrefixFrom(gw, 32))
			}
			via(netip.PrefixFrom(netip.IPv4Unspecified(), 0), gw)
		}
	}
	if i.mode6 != "off" {
		onlink(netip.MustParsePrefix("fe80::/64"))
		if i.ndp != nil {
			for _, p := range i.ndp.onlinkPrefixes() {
				onlink(p)
			}
		}
		for _, a := range i.cfg.IPv6.Addrs {
			if i.mode6 == "static" {
				onlink(a)
			}
		}
		if gw := i.v6Gateway(); gw.IsValid() {
			if !gw.IsLinkLocalUnicast() && !covered(gw, i.cfgAddrs6()) {
				onlink(netip.PrefixFrom(gw, 128))
			}
			via(netip.PrefixFrom(netip.IPv6Unspecified(), 0), gw)
		}
	}
	// the netstack takes the first matching route: most specific first
	sort.SliceStable(rs, func(a, b int) bool { return rs[a].Destination.Prefix() > rs[b].Destination.Prefix() })
	return rs
}

func (i *Iface) cfgAddrs6() []AddrInfo {
	var out []AddrInfo
	for _, p := range i.cfg.IPv6.Addrs {
		out = append(out, AddrInfo{Prefix: p})
	}
	return out
}

func (i *Iface) v6Gateway() netip.Addr {
	if i.mode6 == "static" {
		return i.cfg.IPv6.Gateway
	}
	if i.ndp != nil {
		return i.ndp.router()
	}
	return netip.Addr{}
}

func (i *Iface) v6StatusLocked() FamilyStatus {
	fs := FamilyStatus{State: StateOff}
	switch i.mode6 {
	case "static":
		fs.State = StateOK
		for _, p := range i.cfg.IPv6.Addrs {
			fs.Addrs = append(fs.Addrs, AddrInfo{Prefix: p, Source: "static"})
		}
		fs.Gateway = i.cfg.IPv6.Gateway
		fs.DNS = slices.Clone(i.cfg.IPv6.DNS)
		if len(fs.Addrs) == 0 {
			fs.State, fs.Error = StateFailed, "no address configured"
		} else if !i.ndp.dadDone(tcpipAddr(fs.Addrs[0].Prefix.Addr())) {
			fs.State = StateAcquiring // duplicate address detection still running
		}
		if i.v6err != "" {
			fs.State, fs.Error = StateFailed, i.v6err
		}
		if d := i.ndp.dupes(); d != "" {
			fs.State, fs.Error = StateFailed, "duplicate address "+d
		}
	case "slaac":
		fs.Addrs = i.ndp.addrInfos()
		fs.Gateway = i.ndp.router()
		fs.DNS = i.ndp.dnsServers()
		switch {
		case len(fs.Addrs) > 0:
			fs.State = StateOK
		case i.up && !i.upSince.IsZero() && time.Since(i.upSince) > slaacGrace:
			fs.State = StateFailed
			fs.Error = "no router advertisement"
			if i.ndp.sawRA() {
				fs.Error = "no SLAAC prefix advertised"
			}
		default:
			fs.State = StateAcquiring
		}
	}
	return fs
}

// ---------------------------------------------------------------- public API

func (i *Iface) statusLocked(counters bool) Status {
	st := Status{
		Name:  i.cfg.Name,
		Up:    i.up,
		Error: i.perr,
		MAC:   net.HardwareAddr(i.mac[:]).String(),
		IPv4:  i.v4,
		IPv6:  i.v6StatusLocked(),
	}
	st.IPv4.Addrs = slices.Clone(i.v4.Addrs)
	st.IPv4.DNS = slices.Clone(i.v4.DNS)
	if i.closed {
		st.Up = false
	}
	if counters {
		st.RxBytes = i.link.rxBytes.Load()
		st.TxBytes = i.link.txBytes.Load()
	}
	return st
}

// Status returns a snapshot of the interface.
func (i *Iface) Status() Status {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.statusLocked(true)
}

var errClosed = errors.New("vnic: interface closed")

// source picks the family's primary address as the source for dst.
func (i *Iface) source(is4 bool, dst netip.Addr) (tcpip.Address, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return tcpip.Address{}, errClosed
	}
	if is4 {
		if i.mode4 == "off" {
			return tcpip.Address{}, fmt.Errorf("vnic %s: IPv4 is off", i.cfg.Name)
		}
		if len(i.v4.Addrs) == 0 {
			return tcpip.Address{}, fmt.Errorf("vnic %s: no IPv4 address", i.cfg.Name)
		}
		return tcpipAddr(i.v4.Addrs[0].Prefix.Addr()), nil
	}
	if i.mode6 == "off" {
		return tcpip.Address{}, fmt.Errorf("vnic %s: IPv6 is off", i.cfg.Name)
	}
	if dst.IsValid() && dst.IsLinkLocalUnicast() {
		return tcpip.Address{}, nil // the stack picks the link-local address
	}
	v6 := i.v6StatusLocked()
	if len(v6.Addrs) == 0 || v6.State != StateOK {
		return tcpip.Address{}, fmt.Errorf("vnic %s: no usable IPv6 address", i.cfg.Name)
	}
	return tcpipAddr(v6.Addrs[0].Prefix.Addr()), nil
}

// DialContext dials through the interface. network is tcp, tcp4, tcp6, udp,
// udp4 or udp6; address must be a literal IP:port. The family's primary
// address is the source.
func (i *Iface) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	opErr := func(err error) error { return &net.OpError{Op: "dial", Net: network, Err: err} }
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return nil, opErr(fmt.Errorf("vnic: address must be IP:port: %w", err))
	}
	dst := ap.Addr().Unmap().WithZone("")
	is4 := dst.Is4()
	isTCP := false
	switch network {
	case "tcp", "tcp4", "tcp6":
		isTCP = true
	case "udp", "udp4", "udp6":
	default:
		return nil, opErr(net.UnknownNetworkError(network))
	}
	switch network[len(network)-1] {
	case '4':
		if !is4 {
			return nil, opErr(fmt.Errorf("vnic: %s is not an IPv4 address", dst))
		}
	case '6':
		if is4 {
			return nil, opErr(fmt.Errorf("vnic: %s is not an IPv6 address", dst))
		}
	}
	src, err := i.source(is4, dst)
	if err != nil {
		return nil, opErr(err)
	}
	local := tcpip.FullAddress{NIC: nicID, Addr: src}
	remote := tcpip.FullAddress{NIC: nicID, Addr: tcpipAddr(dst), Port: ap.Port()}
	if isTCP {
		return gonet.DialTCPWithBind(ctx, i.s, local, remote, protoOf(dst))
	}
	return i.udpConn(&local, &remote, protoOf(dst))
}

// udpConn opens a UDP socket on the stack with large buffers: QUIC (hy2)
// receives in bursts, and gonet conns cannot be resized by quic-go.
func (i *Iface) udpConn(local, remote *tcpip.FullAddress, proto tcpip.NetworkProtocolNumber) (*gonet.UDPConn, error) {
	var wq waiter.Queue
	ep, terr := i.s.NewEndpoint(udp.ProtocolNumber, proto, &wq)
	if terr != nil {
		return nil, errors.New(terr.String())
	}
	ep.SocketOptions().SetReceiveBufferSize(udpBufSize, true)
	ep.SocketOptions().SetSendBufferSize(udpBufSize, true)
	if terr := ep.Bind(*local); terr != nil {
		ep.Close()
		return nil, &net.OpError{Op: "bind", Net: "udp", Err: errors.New(terr.String())}
	}
	if remote != nil {
		if terr := ep.Connect(*remote); terr != nil {
			ep.Close()
			return nil, &net.OpError{Op: "connect", Net: "udp", Err: errors.New(terr.String())}
		}
	}
	return gonet.NewUDPConn(&wq, ep), nil
}

const udpBufSize = 4 << 20 // the netstack's maximum

// ListenPacket opens an unconnected UDP socket on the interface. An
// unspecified laddr address means the family's primary address.
func (i *Iface) ListenPacket(network string, laddr netip.AddrPort) (net.PacketConn, error) {
	opErr := func(err error) error { return &net.OpError{Op: "listen", Net: network, Err: err} }
	var is4 bool
	switch network {
	case "udp4":
		is4 = true
	case "udp6":
	case "udp":
		is4 = !laddr.Addr().IsValid() || laddr.Addr().Unmap().Is4()
	default:
		return nil, opErr(net.UnknownNetworkError(network))
	}
	a := laddr.Addr().Unmap().WithZone("")
	var src tcpip.Address
	if !a.IsValid() || a.IsUnspecified() {
		var err error
		if src, err = i.source(is4, netip.Addr{}); err != nil {
			return nil, opErr(err)
		}
	} else {
		if a.Is4() != is4 {
			return nil, opErr(fmt.Errorf("vnic: %s does not match %s", a, network))
		}
		src = tcpipAddr(a)
	}
	proto := tcpip.NetworkProtocolNumber(ipv4.ProtocolNumber)
	if !is4 {
		proto = ipv6.ProtocolNumber
	}
	return i.udpConn(&tcpip.FullAddress{NIC: nicID, Addr: src, Port: laddr.Port()}, nil, proto)
}

// Renew starts a DHCP renewal now (or a new DISCOVER when no lease is held).
func (i *Iface) Renew() error {
	i.mu.Lock()
	closed := i.closed
	i.mu.Unlock()
	if closed {
		return errClosed
	}
	if i.dhcp == nil {
		return fmt.Errorf("vnic %s: IPv4 is not DHCP", i.cfg.Name)
	}
	i.dhcp.renewNow()
	return nil
}

// Close releases the DHCP lease, closes the packet sockets and stops every
// goroutine. Idempotent.
func (i *Iface) Close() error {
	i.closeOnce.Do(func() {
		if i.dhcp != nil {
			i.dhcp.stop()
			i.dhcp.release()
		}
		i.mu.Lock()
		i.closed = true
		i.mu.Unlock()
		i.cancel()
		i.wg.Wait()
		i.s.Destroy()
		unix.Close(i.ctl)
		i.mu.Lock()
		i.up = false
		i.mu.Unlock()
	})
	return nil
}

// ---------------------------------------------------------------- IPv4 state (DHCP)

// setV4Lease installs (or replaces) the DHCP address and its parameters.
func (i *Iface) setV4Lease(old netip.Prefix, l *leaseState) error {
	if old.IsValid() && old != l.Addr {
		i.removeAddr(old.Addr())
	}
	if err := i.addAddr(l.Addr); err != nil {
		return err
	}
	i.mu.Lock()
	if i.v4any {
		i.s.RemoveAddress(nicID, header.IPv4Any)
		i.v4any = false
	}
	exp := l.expiry()
	if l.LeaseSec < 0 {
		exp = time.Time{}
	}
	i.v4 = FamilyStatus{State: StateOK, Addrs: []AddrInfo{{Prefix: l.Addr, Source: "dhcp", Expires: exp}}, Gateway: l.Router, DNS: slices.Clone(l.DNS)}
	i.v4routes = slices.Clone(l.Routes)
	i.mu.Unlock()
	i.kick()
	return nil
}

// clearV4Lease removes the DHCP address (expired / NAK).
func (i *Iface) clearV4Lease(old netip.Prefix, state, errMsg string) {
	if old.IsValid() {
		i.removeAddr(old.Addr())
	}
	i.mu.Lock()
	i.v4 = FamilyStatus{State: state, Error: errMsg}
	i.v4routes = nil
	i.mu.Unlock()
	i.kick()
}

func (i *Iface) setV4State(state, errMsg string) {
	i.mu.Lock()
	changed := i.v4.State != state || i.v4.Error != errMsg
	i.v4.State, i.v4.Error = state, errMsg
	i.mu.Unlock()
	if changed {
		i.kick()
	}
}

// ensureUnspecified assigns 0.0.0.0 so DHCP can send from it while no
// address is held.
func (i *Iface) ensureUnspecified() {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.v4any || len(i.v4.Addrs) > 0 {
		return
	}
	pa := tcpip.ProtocolAddress{Protocol: ipv4.ProtocolNumber, AddressWithPrefix: tcpip.AddressWithPrefix{Address: header.IPv4Any}}
	if i.s.AddProtocolAddress(nicID, pa, stack.AddressProperties{}) == nil {
		i.v4any = true
	}
}

func (i *Iface) isUp() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.up
}
