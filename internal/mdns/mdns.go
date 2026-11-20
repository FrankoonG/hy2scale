// Package mdns is a minimal mDNS responder that answers A/AAAA queries for a
// single "<label>.local" name with the addresses of the interface each query
// arrived on. It shares port 5353 with the host's own responder and never
// answers for any other name.
package mdns

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"net"
	"net/netip"
	"sync"
	"time"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

const (
	rescanEvery = 30 * time.Second
	maxNames    = 20 // label, label-2 ... label-20
	maxLosses   = 10 // lost probe tiebreaks before moving to the next name
	ownMemory   = 3 * time.Second
)

var (
	group4 = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: port}
	group6 = net.ParseIP("ff02::fb")
)

// Config configures a Responder.
type Config struct {
	Label string                           // host label without ".local", already sanitised (a-z0-9-)
	Skip  func(ifname string) bool         // optional: true = ignore this interface
	Logf  func(format string, args ...any) // optional
}

// Responder answers mDNS queries for one claimed name.
type Responder struct {
	cfg   Config
	label string
	u4    net.PacketConn
	u6    net.PacketConn
	c4    *ipv4.PacketConn
	c6    *ipv6.PacketConn

	mu     sync.Mutex
	ifaces map[int]*ifaceInfo
	name   string // claimed, e.g. "sg-home.local"
	cand   string // being probed
	limit  map[limitKey]time.Time
	sent   map[uint64]time.Time

	events chan event
	done   chan struct{}
	once   sync.Once
	wg     sync.WaitGroup
}

type event struct {
	name string
	lost bool // lost a probe tiebreak (else: conflict)
}

type limitKey struct {
	ifindex int
	v6      bool
	types   uint8
}

// ListenPacket opens a UDP socket ("udp4" or "udp6") on the wildcard address
// and port 5353, shared with other responders on the host where the OS
// allows it.
func ListenPacket(network string) (net.PacketConn, error) {
	addr := "0.0.0.0:5353"
	if network == "udp6" {
		addr = "[::]:5353"
	}
	lc := net.ListenConfig{Control: reuseControl}
	return lc.ListenPacket(context.Background(), network, addr)
}

// Start opens the sockets and starts probing for cfg.Label. It fails only if
// the label is empty or no socket at all could be opened.
func Start(cfg Config) (*Responder, error) {
	label := SanitizeLabel(cfg.Label)
	if label == "" {
		return nil, errors.New("mdns: empty label")
	}
	r := &Responder{
		cfg:    cfg,
		label:  label,
		ifaces: map[int]*ifaceInfo{},
		limit:  map[limitKey]time.Time{},
		sent:   map[uint64]time.Time{},
		events: make(chan event, 16),
		done:   make(chan struct{}),
	}
	u4, err4 := ListenPacket("udp4")
	if err4 == nil {
		r.u4, r.c4 = u4, ipv4.NewPacketConn(u4)
		if err := r.c4.SetControlMessage(ipv4.FlagInterface, true); err != nil {
			r.logf("ipv4 arrival interface unavailable: %v", err)
		}
		_ = r.c4.SetMulticastTTL(255)
		_ = r.c4.SetTTL(255)
		_ = r.c4.SetMulticastLoopback(true)
	}
	u6, err6 := ListenPacket("udp6")
	if err6 == nil {
		r.u6, r.c6 = u6, ipv6.NewPacketConn(u6)
		if err := r.c6.SetControlMessage(ipv6.FlagInterface, true); err != nil {
			r.logf("ipv6 arrival interface unavailable: %v", err)
		}
		_ = r.c6.SetMulticastHopLimit(255)
		_ = r.c6.SetHopLimit(255)
		_ = r.c6.SetMulticastLoopback(true)
	}
	if r.c4 == nil && r.c6 == nil {
		return nil, fmt.Errorf("mdns: listen: %v; %v", err4, err6)
	}
	r.rescan()
	if r.c4 != nil {
		r.wg.Add(1)
		go r.read4()
	}
	if r.c6 != nil {
		r.wg.Add(1)
		go r.read6()
	}
	r.wg.Add(1)
	go r.run()
	return r, nil
}

// Name returns the claimed full name (e.g. "sg-home.local"), or "" while
// probing.
func (r *Responder) Name() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.name
}

// Close sends goodbye announcements (TTL 0) for the claimed name, closes the
// sockets and waits up to a second for the goroutines. It is idempotent.
func (r *Responder) Close() error {
	r.once.Do(func() {
		r.mu.Lock()
		close(r.done)
		name := r.name
		r.name, r.cand = "", ""
		ifs := r.snapshot()
		r.mu.Unlock()
		if name != "" {
			for _, ifc := range ifs {
				if msg, err := buildAnnounce(name, ifc.addrs, 0); err == nil {
					r.sendMulticast(ifc, msg)
				}
			}
		}
		if r.u4 != nil {
			r.u4.Close()
		}
		if r.u6 != nil {
			r.u6.Close()
		}
		waited := make(chan struct{})
		go func() { r.wg.Wait(); close(waited) }()
		select {
		case <-waited:
		case <-time.After(time.Second):
		}
	})
	return nil
}

func (r *Responder) logf(format string, args ...any) {
	if r.cfg.Logf != nil {
		r.cfg.Logf(format, args...)
	}
}

func (r *Responder) closed() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

// sleep waits d; false if the responder closed meanwhile.
func (r *Responder) sleep(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-r.done:
		return false
	case <-t.C:
		return true
	}
}

// snapshot returns the selected interfaces; r.mu must be held.
func (r *Responder) snapshot() []*ifaceInfo {
	out := make([]*ifaceInfo, 0, len(r.ifaces))
	for _, ifc := range r.ifaces {
		out = append(out, ifc)
	}
	return out
}

// rescan refreshes the interface set, joining and leaving the groups, and
// returns interfaces that are new or whose addresses changed. Only Start and
// the run goroutine call it.
func (r *Responder) rescan() []*ifaceInfo {
	cur := scanInterfaces(r.cfg.Skip)
	r.mu.Lock()
	old := r.ifaces
	r.mu.Unlock()
	var changed []*ifaceInfo
	for idx, n := range cur {
		o := old[idx]
		if o != nil && o.ifi.Name == n.ifi.Name {
			n.j4, n.j6 = o.j4, o.j6
			if !sameAddrs(o.addrs, n.addrs) {
				changed = append(changed, n)
			}
			continue
		}
		if o != nil {
			r.leave(o)
		}
		r.join(n)
		r.logf("serving on %s %v", n.ifi.Name, n.addrs)
		changed = append(changed, n)
	}
	for idx, o := range old {
		if cur[idx] == nil {
			r.leave(o)
			r.logf("dropped %s", o.ifi.Name)
		}
	}
	r.mu.Lock()
	r.ifaces = cur
	r.mu.Unlock()
	return changed
}

func (r *Responder) join(ifc *ifaceInfo) {
	if r.c4 != nil {
		if err := r.c4.JoinGroup(&ifc.ifi, group4); err != nil {
			r.logf("join 224.0.0.251 on %s: %v", ifc.ifi.Name, err)
		} else {
			ifc.j4 = true
		}
	}
	if r.c6 != nil {
		if err := r.c6.JoinGroup(&ifc.ifi, &net.UDPAddr{IP: group6}); err == nil {
			ifc.j6 = true
		}
	}
}

func (r *Responder) leave(ifc *ifaceInfo) {
	if ifc.j4 && r.c4 != nil {
		_ = r.c4.LeaveGroup(&ifc.ifi, group4)
	}
	if ifc.j6 && r.c6 != nil {
		_ = r.c6.LeaveGroup(&ifc.ifi, &net.UDPAddr{IP: group6})
	}
}

// run is the state machine: probe a candidate, claim it, serve until a
// conflict, then move to the next candidate.
func (r *Responder) run() {
	defer r.wg.Done()
	for {
		r.mu.Lock()
		n := len(r.ifaces)
		r.mu.Unlock()
		if n > 0 {
			break
		}
		if !r.sleep(5 * time.Second) {
			return
		}
		r.rescan()
	}
	losses := 0
	for n := 1; n <= maxNames; {
		full := candidate(r.label, n) + ".local"
		switch r.probe(full) {
		case probeClosed:
			return
		case probeConflict:
			r.logf("%s is taken", full)
			n, losses = n+1, 0
			continue
		case probeLost:
			if losses++; losses >= maxLosses {
				n, losses = n+1, 0
			}
			if !r.sleep(time.Second) {
				return
			}
			continue
		}
		r.mu.Lock()
		if r.closed() {
			r.mu.Unlock()
			return
		}
		r.name = full
		ifs := r.snapshot()
		r.mu.Unlock()
		r.logf("claimed %s", full)
		if !r.announceTwice(full, ifs) || !r.serve(full) {
			return
		}
		r.mu.Lock()
		r.name = ""
		r.mu.Unlock()
		n, losses = n+1, 0
	}
	r.logf("no free name after %d attempts, giving up", maxNames)
}

type probeResult int

const (
	probeOK probeResult = iota
	probeConflict
	probeLost
	probeClosed
)

// probe sends three probes 250 ms apart and listens one more second.
func (r *Responder) probe(full string) probeResult {
	for len(r.events) > 0 {
		<-r.events
	}
	r.mu.Lock()
	r.cand = full
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.cand = ""
		r.mu.Unlock()
	}()
	if res := r.wait(full, time.Duration(rand.IntN(250))*time.Millisecond); res != probeOK {
		return res
	}
	for i := 0; i < 3; i++ {
		r.mu.Lock()
		ifs := r.snapshot()
		r.mu.Unlock()
		for _, ifc := range ifs {
			if msg, err := buildProbe(full, ifc.addrs); err == nil {
				r.sendMulticast(ifc, msg)
			}
		}
		d := 250 * time.Millisecond
		if i == 2 {
			d = time.Second
		}
		if res := r.wait(full, d); res != probeOK {
			return res
		}
	}
	return probeOK
}

// wait sleeps d unless an event for full arrives first.
func (r *Responder) wait(full string, d time.Duration) probeResult {
	t := time.NewTimer(d)
	defer t.Stop()
	for {
		select {
		case <-r.done:
			return probeClosed
		case <-t.C:
			return probeOK
		case ev := <-r.events:
			if ev.name != full {
				continue
			}
			if ev.lost {
				return probeLost
			}
			return probeConflict
		}
	}
}

// serve runs while full is claimed; it returns true on a conflict, false on
// Close.
func (r *Responder) serve(full string) bool {
	t := time.NewTicker(rescanEvery)
	defer t.Stop()
	for {
		select {
		case <-r.done:
			return false
		case ev := <-r.events:
			if ev.name == full && !ev.lost {
				r.logf("conflict on %s, renaming", full)
				return true
			}
		case <-t.C:
			if ch := r.rescan(); len(ch) > 0 && !r.announceTwice(full, ch) {
				return false
			}
		}
	}
}

func (r *Responder) announceTwice(full string, ifs []*ifaceInfo) bool {
	for i := 0; i < 2; i++ {
		if i > 0 && !r.sleep(time.Second) {
			return false
		}
		for _, ifc := range ifs {
			if r.closed() {
				return false
			}
			if msg, err := buildAnnounce(full, ifc.addrs, ttlNormal); err == nil {
				r.sendMulticast(ifc, msg)
			}
		}
	}
	return true
}

func (r *Responder) signal(ev event) {
	select {
	case r.events <- ev:
	default:
	}
}

func packetHash(b []byte) uint64 {
	h := fnv.New64a()
	h.Write(b)
	return h.Sum64()
}

// remember records a packet we sent, so its multicast loopback copy is not
// mistaken for someone else's.
func (r *Responder) remember(msg []byte) {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.sent) > 256 {
		for k, t := range r.sent {
			if now.Sub(t) > ownMemory {
				delete(r.sent, k)
			}
		}
	}
	r.sent[packetHash(msg)] = now
}

func (r *Responder) isOwn(msg []byte) bool {
	h := packetHash(msg)
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.sent[h]
	return ok && time.Since(t) < ownMemory
}

func (r *Responder) sendMulticast(ifc *ifaceInfo, msg []byte) {
	r.remember(msg)
	if r.c4 != nil && ifc.has4 {
		if _, err := r.c4.WriteTo(msg, &ipv4.ControlMessage{IfIndex: ifc.ifi.Index}, group4); err != nil && !r.closed() {
			r.logf("send on %s: %v", ifc.ifi.Name, err)
		}
	}
	if r.c6 != nil && ifc.j6 {
		dst := &net.UDPAddr{IP: group6, Port: port, Zone: ifc.ifi.Name}
		_, _ = r.c6.WriteTo(msg, &ipv6.ControlMessage{IfIndex: ifc.ifi.Index, HopLimit: 255}, dst)
	}
}

func (r *Responder) read4() {
	defer r.wg.Done()
	buf := make([]byte, 9000)
	for {
		n, cm, src, err := r.c4.ReadFrom(buf)
		if err != nil {
			if r.closed() || errors.Is(err, net.ErrClosed) || !r.sleep(100*time.Millisecond) {
				return
			}
			continue
		}
		if cm != nil {
			r.handle(buf[:n], cm.IfIndex, src, false)
		}
	}
}

func (r *Responder) read6() {
	defer r.wg.Done()
	buf := make([]byte, 9000)
	for {
		n, cm, src, err := r.c6.ReadFrom(buf)
		if err != nil {
			if r.closed() || errors.Is(err, net.ErrClosed) || !r.sleep(100*time.Millisecond) {
				return
			}
			continue
		}
		if cm != nil {
			r.handle(buf[:n], cm.IfIndex, src, true)
		}
	}
}

// handle processes one packet that arrived on ifindex.
func (r *Responder) handle(pkt []byte, ifindex int, from net.Addr, v6 bool) {
	src, ok := from.(*net.UDPAddr)
	if !ok || len(pkt) < 12 || r.isOwn(pkt) {
		return
	}
	isResp := pkt[2]&0x80 != 0
	r.mu.Lock()
	ifc := r.ifaces[ifindex]
	name, cand := r.name, r.cand
	var ours []netip.Addr
	if isResp && name != "" {
		for _, i := range r.ifaces {
			ours = append(ours, i.addrs...)
		}
	}
	r.mu.Unlock()
	if ifc == nil {
		return
	}
	if isResp {
		if src.Port != port {
			return
		}
		if cand != "" && Conflicts(pkt, cand, nil) {
			r.signal(event{name: cand})
		}
		if name != "" && Conflicts(pkt, name, ours) {
			r.signal(event{name: name})
		}
		return
	}
	if cand != "" && LosesTiebreak(pkt, cand, ifc.addrs) {
		r.signal(event{name: cand, lost: true})
	}
	if name == "" {
		return
	}
	rep, ok := answer(pkt, name, ifc.addrs, src.Port)
	if !ok {
		return
	}
	var dst net.Addr = src
	if !rep.unicast {
		key := limitKey{ifindex, v6, rep.types}
		gap := time.Second
		if rep.probe {
			gap = 250 * time.Millisecond
		}
		now := time.Now()
		r.mu.Lock()
		last, seen := r.limit[key]
		if !seen || now.Sub(last) >= gap {
			r.limit[key] = now
		}
		r.mu.Unlock()
		if seen && now.Sub(last) < gap {
			return
		}
		r.remember(rep.msg)
		dst = group4
		if v6 {
			dst = &net.UDPAddr{IP: group6, Port: port, Zone: ifc.ifi.Name}
		}
	}
	if v6 {
		_, _ = r.c6.WriteTo(rep.msg, &ipv6.ControlMessage{IfIndex: ifindex, HopLimit: 255}, dst)
	} else {
		_, _ = r.c4.WriteTo(rep.msg, &ipv4.ControlMessage{IfIndex: ifindex}, dst)
	}
}
