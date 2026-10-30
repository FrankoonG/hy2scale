//go:build linux

package vnic

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

// leaseState is the persisted DHCP lease (<StateDir>/<Name>.json).
type leaseState struct {
	MAC      string       `json:"mac"`
	Addr     netip.Prefix `json:"addr"`
	Router   netip.Addr   `json:"router,omitzero"`
	Routes   []dhcpRoute  `json:"routes,omitempty"`
	DNS      []netip.Addr `json:"dns,omitempty"`
	Server   netip.Addr   `json:"server,omitzero"`
	Acquired time.Time    `json:"acquired"`
	LeaseSec int64        `json:"lease_secs"` // -1 = infinite
	T1Sec    int64        `json:"t1_secs"`
	T2Sec    int64        `json:"t2_secs"`
	Released bool         `json:"released,omitempty"`
}

func (l *leaseState) at(secs int64) time.Time {
	if l.LeaseSec < 0 {
		return time.Unix(1<<62, 0)
	}
	return l.Acquired.Add(time.Duration(secs) * time.Second)
}
func (l *leaseState) expiry() time.Time { return l.at(l.LeaseSec) }
func (l *leaseState) t1() time.Time     { return l.at(l.T1Sec) }
func (l *leaseState) t2() time.Time     { return l.at(l.T2Sec) }

type dhcpClient struct {
	i       *Iface
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	renewCh chan struct{}
	lease   *leaseState // bound lease; owned by run(), then by release()
	prev    netip.Addr  // address to ask for again
	path    string
	cid     []byte
}

func newDHCPClient(i *Iface) *dhcpClient {
	c := &dhcpClient{i: i, done: make(chan struct{}), renewCh: make(chan struct{}, 1), cid: clientIDBytes(i.cfg.IPv4.ClientID)}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	if i.cfg.StateDir != "" {
		c.path = filepath.Join(i.cfg.StateDir, i.cfg.Name+".json")
	}
	return c
}

func (c *dhcpClient) stop() {
	c.cancel()
	<-c.done
}

func (c *dhcpClient) renewNow() {
	select {
	case c.renewCh <- struct{}{}:
	default:
	}
}

func (c *dhcpClient) load() *leaseState {
	if c.path == "" {
		return nil
	}
	b, err := os.ReadFile(c.path)
	if err != nil {
		return nil
	}
	var l leaseState
	if json.Unmarshal(b, &l) != nil || !l.Addr.IsValid() || l.MAC != net.HardwareAddr(c.i.mac[:]).String() {
		return nil
	}
	return &l
}

func (c *dhcpClient) save(l *leaseState) {
	if c.path == "" || l == nil {
		return
	}
	b, _ := json.MarshalIndent(l, "", "  ")
	tmp := c.path + ".tmp"
	if os.WriteFile(tmp, b, 0600) == nil {
		os.Rename(tmp, c.path)
	}
}

// wait sleeps d unless a renewal is requested (true) or the client stops.
func (c *dhcpClient) wait(d time.Duration) (renew bool, ok bool) {
	if d < 0 {
		d = 0
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return false, true
	case <-c.renewCh:
		return true, true
	case <-c.ctx.Done():
		return false, false
	}
}

func (c *dhcpClient) run() {
	defer close(c.done)
	// the first message would be lost while the packet socket is still opening
	for k := 0; k < 150 && !c.i.isUp() && c.ctx.Err() == nil; k++ {
		time.Sleep(20 * time.Millisecond)
	}
	if prev := c.load(); prev != nil {
		c.prev = prev.Addr.Addr()
		if !prev.Released && time.Now().Before(prev.expiry()) {
			// INIT-REBOOT: confirm the remembered lease
			c.i.ensureUnspecified()
			m := c.newMsg(dhcpRequest)
			m.flags = 0x8000
			m.set(optRequestedIP, addr4(prev.Addr.Addr()))
			r, _ := c.xchg(m, netip.AddrFrom4([4]byte{255, 255, 255, 255}), isAckOrNak, 6*time.Second)
			switch {
			case c.ctx.Err() != nil:
				return
			case r != nil && r.msgType() == dhcpAck:
				c.bind(r)
			case r != nil:
				c.prev = netip.Addr{}
			case time.Now().Before(prev.expiry()):
				// no server answered: keep using the unexpired lease (RFC 2131 3.2)
				if c.i.setV4Lease(netip.Prefix{}, prev) == nil {
					c.lease = prev
				}
			}
		}
	}
	forced := false
	for c.ctx.Err() == nil {
		if c.lease == nil {
			if !c.discover() {
				return
			}
			forced = false
			continue
		}
		l := c.lease
		now := time.Now()
		if !now.Before(l.expiry()) {
			c.i.clearV4Lease(l.Addr, StateAcquiring, "lease expired")
			c.lease = nil
			continue
		}
		if !forced && now.Before(l.t1()) {
			r, ok := c.wait(l.t1().Sub(now))
			if !ok {
				return
			}
			forced = r
			continue
		}
		rebinding := !now.Before(l.t2())
		m := c.newMsg(dhcpRequest)
		m.ciaddr = l.Addr.Addr()
		dst := l.Server
		if rebinding || !dst.IsValid() {
			dst = netip.AddrFrom4([4]byte{255, 255, 255, 255})
		}
		r, _ := c.xchg(m, dst, isAckOrNak, 4*time.Second)
		if c.ctx.Err() != nil {
			return
		}
		forced = false
		if r != nil && r.msgType() == dhcpAck && r.yiaddr.IsValid() && !r.yiaddr.IsUnspecified() {
			c.bind(r)
			continue
		}
		if r != nil && r.msgType() == dhcpNak {
			c.i.clearV4Lease(l.Addr, StateAcquiring, "lease refused (DHCPNAK)")
			c.lease = nil
			continue
		}
		// no answer: retry at half the time left until the next stage
		next := l.t2()
		if rebinding {
			next = l.expiry()
		}
		d := time.Until(next) / 2
		d = max(min(d, 60*time.Second), 5*time.Second)
		if until := time.Until(next); until < d {
			d = until
		}
		r2, ok := c.wait(d)
		if !ok {
			return
		}
		forced = r2
	}
}

func isAckOrNak(m *dhcpMsg) bool { t := m.msgType(); return t == dhcpAck || t == dhcpNak }

// discover runs DISCOVER / OFFER / REQUEST / ACK until bound (true) or
// stopped (false).
func (c *dhcpClient) discover() bool {
	backoff := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 60 * time.Second}
	for attempt := 0; c.ctx.Err() == nil; attempt++ {
		c.i.ensureUnspecified()
		d := c.newMsg(dhcpDiscover)
		d.flags = 0x8000
		if c.prev.IsValid() {
			d.set(optRequestedIP, addr4(c.prev))
		}
		offer, _ := c.xchg(d, netip.AddrFrom4([4]byte{255, 255, 255, 255}), func(m *dhcpMsg) bool {
			return m.msgType() == dhcpOffer && m.yiaddr.IsValid() && !m.yiaddr.IsUnspecified()
		}, 10*time.Second)
		if c.ctx.Err() != nil {
			return false
		}
		if offer != nil {
			req := c.newMsg(dhcpRequest)
			req.xid = d.xid
			req.flags = 0x8000
			req.set(optRequestedIP, addr4(offer.yiaddr))
			if sid := offer.addr(optServerID); sid.IsValid() {
				req.set(optServerID, addr4(sid))
			}
			r, _ := c.xchg(req, netip.AddrFrom4([4]byte{255, 255, 255, 255}), isAckOrNak, 8*time.Second)
			if c.ctx.Err() != nil {
				return false
			}
			if r != nil && r.msgType() == dhcpAck && r.yiaddr.IsValid() && !r.yiaddr.IsUnspecified() {
				if c.bind(r) {
					return true
				}
			}
			if r != nil && r.msgType() == dhcpNak {
				c.prev = netip.Addr{}
			}
		}
		if attempt >= 1 {
			msg := "no DHCP offer"
			if offer != nil {
				msg = "no DHCP acknowledgement"
			}
			c.i.setV4State(StateFailed, msg)
		}
		if _, ok := c.wait(backoff[min(attempt, len(backoff)-1)]); !ok {
			return false
		}
	}
	return false
}

// bind turns an ACK into the current lease.
func (c *dhcpClient) bind(ack *dhcpMsg) bool {
	lease, ok := ack.seconds(optLeaseTime)
	if !ok || lease <= 0 {
		lease = time.Hour
	}
	l := &leaseState{
		MAC:      net.HardwareAddr(c.i.mac[:]).String(),
		Addr:     netip.PrefixFrom(ack.yiaddr, ack.prefixLen()),
		DNS:      ack.addrs(optDNS),
		Server:   ack.addr(optServerID),
		Acquired: time.Now(),
	}
	if lease == infiniteLease {
		l.LeaseSec, l.T1Sec, l.T2Sec = -1, -1, -1
	} else {
		l.LeaseSec = int64(lease / time.Second)
		l.T1Sec, l.T2Sec = l.LeaseSec/2, l.LeaseSec*7/8
		if t1, ok := ack.seconds(optT1); ok && t1 != infiniteLease && int64(t1/time.Second) < l.LeaseSec {
			l.T1Sec = int64(t1 / time.Second)
		}
		if t2, ok := ack.seconds(optT2); ok && t2 != infiniteLease && int64(t2/time.Second) < l.LeaseSec && int64(t2/time.Second) >= l.T1Sec {
			l.T2Sec = int64(t2 / time.Second)
		}
	}
	// RFC 3442: classless routes replace the router option
	if rs := ack.classless(); len(rs) > 0 {
		for _, r := range rs {
			if r.Dst.Bits() == 0 && !l.Router.IsValid() {
				l.Router = r.Via
			} else if r.Dst.Bits() > 0 {
				l.Routes = append(l.Routes, r)
			}
		}
	} else if rs := ack.addrs(optRouter); len(rs) > 0 {
		l.Router = rs[0]
	}
	var old netip.Prefix
	if c.lease != nil {
		old = c.lease.Addr
	}
	if err := c.i.setV4Lease(old, l); err != nil {
		c.i.setV4State(StateFailed, err.Error())
		return false
	}
	c.lease = l
	c.prev = l.Addr.Addr()
	c.save(l)
	return true
}

// release sends DHCPRELEASE for the bound lease (best effort, called from
// Close after run has stopped).
func (c *dhcpClient) release() {
	l := c.lease
	if l == nil || l.Released {
		return
	}
	l.Released = true
	c.save(l)
	if !l.Server.IsValid() || !c.i.isUp() {
		return
	}
	s := c.i.s
	local, remote := tcpipAddr(l.Addr.Addr()), tcpipAddr(l.Server)
	// resolve the next hop first so the frame leaves before the socket closes
	if r, terr := s.FindRoute(nicID, local, remote, ipv4.ProtocolNumber, false); terr == nil {
		done := make(chan struct{})
		terr = r.ResolvedFields(func(stack.ResolvedFieldsResult) { close(done) })
		if _, wb := terr.(*tcpip.ErrWouldBlock); wb {
			select {
			case <-done:
			case <-time.After(time.Second):
			}
		}
		r.Release()
	}
	m := c.newMsg(dhcpRelease)
	m.ciaddr = l.Addr.Addr()
	m.set(optServerID, addr4(l.Server))
	conn, err := c.listen(local)
	if err != nil {
		return
	}
	conn.WriteTo(m.marshal(), &net.UDPAddr{IP: l.Server.AsSlice(), Port: 67})
	conn.Close()
}

func (c *dhcpClient) newMsg(t byte) *dhcpMsg {
	var x [4]byte
	rand.Read(x[:])
	m := &dhcpMsg{op: 1, xid: binary.BigEndian.Uint32(x[:]), chaddr: c.i.mac}
	m.set(optMsgType, []byte{t})
	if c.cid != nil {
		m.set(optClientID, c.cid)
	}
	if t == dhcpDiscover || t == dhcpRequest {
		m.set(optMaxMsgSize, []byte{0x05, 0xdc})
		m.set(optParamList, []byte{optSubnetMask, optRouter, optDNS, optDomainName, optBroadcast, optLeaseTime, optServerID, optT1, optT2, optClasslessRte})
		if h := c.i.cfg.IPv4.Hostname; h != "" {
			m.set(optHostname, []byte(h))
		}
	}
	return m
}

func (c *dhcpClient) listen(local tcpip.Address) (*gonet.UDPConn, error) {
	var wq waiter.Queue
	ep, terr := c.i.s.NewEndpoint(udp.ProtocolNumber, ipv4.ProtocolNumber, &wq)
	if terr != nil {
		return nil, errors.New(terr.String())
	}
	ep.SocketOptions().SetBroadcast(true)
	if terr := ep.Bind(tcpip.FullAddress{NIC: nicID, Addr: local, Port: 68}); terr != nil {
		ep.Close()
		return nil, errors.New(terr.String())
	}
	return gonet.NewUDPConn(&wq, ep), nil
}

// xchg sends m to dst:67 (resending with backoff) until a reply with the same
// xid that accept likes arrives, total runs out, or the client stops.
func (c *dhcpClient) xchg(m *dhcpMsg, dst netip.Addr, accept func(*dhcpMsg) bool, total time.Duration) (*dhcpMsg, error) {
	conn, err := c.listen(tcpip.Address{})
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	stopf := context.AfterFunc(c.ctx, func() { conn.Close() })
	defer stopf()
	deadline := time.Now().Add(total)
	interval := 2 * time.Second
	buf := make([]byte, 1500)
	payload := m.marshal()
	to := &net.UDPAddr{IP: dst.AsSlice(), Port: 67}
	for time.Now().Before(deadline) && c.ctx.Err() == nil {
		conn.WriteTo(payload, to)
		until := time.Now().Add(interval)
		if until.After(deadline) {
			until = deadline
		}
		interval *= 2
		conn.SetReadDeadline(until)
		for {
			n, _, err := conn.ReadFrom(buf)
			if err != nil {
				break
			}
			r, err := parseDHCP(buf[:n])
			if err != nil || r.op != 2 || r.xid != m.xid || r.chaddr != m.chaddr || !accept(r) {
				continue
			}
			return r, nil
		}
	}
	return nil, errors.New("timeout")
}
