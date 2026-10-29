//go:build linux

package vnic

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/rawfile"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/checksum"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/stack/gro"
)

// Two AF_PACKET sockets per open link:
//
//   - rx: bound to the parent with ETH_P_ALL, a classic BPF filter that only
//     lets through frames for our MAC / broadcast / IPv6 multicast on our VLAN
//     (the kernel moves 802.1Q tags into skb metadata before packet taps, so the
//     filter reads the VID with the VLAN ancillary loads and the frames arrive
//     untagged), PACKET_IGNORE_OUTGOING, a TPACKET_V2 mmap ring. Frames larger
//     than a ring slot (GRO super-packets) are flagged TP_STATUS_COPY and read
//     in full from the socket queue (PACKET_COPY_THRESH).
//   - tx: protocol 0 (receives nothing), PACKET_VNET_HDR so the netstack can
//     hand 64 KiB TCP segments to the kernel, which segments them in software
//     on the way out (host GSO). Each frame is sent with an explicit
//     sockaddr_ll whose protocol is the frame's ethertype (0x8100 when tagged).
//
// The NIC's unicast filter gets our MAC with PACKET_MR_UNICAST (the kernel
// falls back to promiscuous mode itself when the driver has no unicast
// filter); if the membership is refused we fall back to PACKET_MR_PROMISC.
// All memberships go away with the socket.

const (
	ethHdrLen  = header.EthernetMinimumSize
	vlanHdrLen = 4
	etherVLAN  = 0x8100

	vnetHdrLen           = 10
	vnetNeedsCsum        = 1
	vnetDataValid        = 2
	vnetGSONone          = 0
	vnetGSOUDPL4         = 5
	vnetGSOECN           = 0x80
	vnetGSOTCPv4         = 1
	vnetGSOTCPv6         = 4
	gsoMaxSize           = 65536
	packetIgnoreOutgoing = 23 // PACKET_IGNORE_OUTGOING, Linux 4.20+

	tpStatusUser         = 1
	tpStatusCopy         = 2
	tpStatusCsumNotReady = 8
	tpStatusVLANValid    = 16
	tpStatusCsumValid    = 128

	ringBytes  = 2 << 20
	blockBytes = 128 << 10
	copyBufLen = 65536 + 1024
	rxBatch    = 64
)

var (
	errNoPermission = errors.New("no permission to open a packet socket (needs CAP_NET_RAW in the host network namespace)")
)

func htons(v uint16) uint16 { return v<<8 | v>>8 }

// ---------------------------------------------------------------- sockets

type sock struct {
	rxfd, txfd, efd int
	ring            []byte
	frameSize       int
	frames          int
	ifindex         int
	promisc         bool
	allmulti        bool
	groups          map[[6]byte]bool
	vlan            int
	copyBuf         []byte
	rxVnet          bool       // frames carry a virtio_net_hdr
	done            chan error // rx loop result
}

// parentInfo reads ifindex, flags and MTU of a network interface.
func parentInfo(fd int, name string) (ifindex int, flags uint16, mtu int, err error) {
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		return 0, 0, 0, err
	}
	if err = unix.IoctlIfreq(fd, unix.SIOCGIFINDEX, ifr); err != nil {
		if errors.Is(err, unix.ENODEV) {
			err = fmt.Errorf("parent interface %q not found", name)
		}
		return
	}
	ifindex = int(ifr.Uint32())
	if err = unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, ifr); err != nil {
		return
	}
	flags = ifr.Uint16()
	if err = unix.IoctlIfreq(fd, unix.SIOCGIFMTU, ifr); err != nil {
		return
	}
	mtu = int(ifr.Uint32())
	return
}

func buildFilter(mac [6]byte, vlan int, v6 bool) ([]unix.SockFilter, error) {
	const accept, drop = 0x40000, 0
	// head: VLAN check (5 instructions), then the MAC check at m = 5
	const m = 5
	// indices of the two return instructions
	tailLen := 9
	if !v6 {
		tailLen = 8
	}
	dropIdx := m + tailLen
	accIdx := dropIdx + 1
	skip := func(from, to int) uint8 { return uint8(to - from - 1) }

	var prog []bpf.Instruction
	prog = append(prog, bpf.LoadExtension{Num: bpf.ExtVLANTagPresent})
	if vlan == 0 {
		// untagged, or priority-tagged (VID 0)
		prog = append(prog,
			bpf.JumpIf{Cond: bpf.JumpEqual, Val: 0, SkipTrue: skip(1, m)},
			bpf.LoadExtension{Num: bpf.ExtVLANTag},
			bpf.ALUOpConstant{Op: bpf.ALUOpAnd, Val: 0x0fff},
			bpf.JumpIf{Cond: bpf.JumpEqual, Val: 0, SkipFalse: skip(4, dropIdx)},
		)
	} else {
		prog = append(prog,
			bpf.JumpIf{Cond: bpf.JumpEqual, Val: 0, SkipTrue: skip(1, dropIdx)},
			bpf.LoadExtension{Num: bpf.ExtVLANTag},
			bpf.ALUOpConstant{Op: bpf.ALUOpAnd, Val: 0x0fff},
			bpf.JumpIf{Cond: bpf.JumpEqual, Val: uint32(vlan), SkipFalse: skip(4, dropIdx)},
		)
	}
	hi := binary.BigEndian.Uint32(mac[0:4])
	lo := uint32(binary.BigEndian.Uint16(mac[4:6]))
	prog = append(prog,
		bpf.LoadAbsolute{Off: 0, Size: 4},                      // m+0
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: hi, SkipFalse: 2}, // m+1
		bpf.LoadAbsolute{Off: 4, Size: 2},                      // m+2
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: lo, SkipTrue: skip(m+3, accIdx), SkipFalse: skip(m+3, dropIdx)},     // m+3
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: 0xffffffff, SkipFalse: 2},                                           // m+4
		bpf.LoadAbsolute{Off: 4, Size: 2},                                                                        // m+5
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: 0xffff, SkipTrue: skip(m+6, accIdx), SkipFalse: skip(m+6, dropIdx)}, // m+6
	)
	if v6 {
		prog = append(prog,
			bpf.ALUOpConstant{Op: bpf.ALUOpShiftRight, Val: 16},                                                      // m+7
			bpf.JumpIf{Cond: bpf.JumpEqual, Val: 0x3333, SkipTrue: skip(m+8, accIdx), SkipFalse: skip(m+8, dropIdx)}, // m+8
		)
	} else {
		prog = append(prog, bpf.Jump{Skip: 0}) // m+7: falls into drop
	}
	prog = append(prog, bpf.RetConstant{Val: drop}, bpf.RetConstant{Val: accept})
	if len(prog) != accIdx+1 {
		return nil, fmt.Errorf("bpf: program layout %d != %d", len(prog), accIdx+1)
	}
	raw, err := bpf.Assemble(prog)
	if err != nil {
		return nil, err
	}
	out := make([]unix.SockFilter, len(raw))
	for i, r := range raw {
		out[i] = unix.SockFilter{Code: r.Op, Jt: r.Jt, Jf: r.Jf, K: r.K}
	}
	return out, nil
}

type openParams struct {
	parent string
	mac    [6]byte
	vlan   int
	v6     bool
	gso    bool
	mtu    int // configured, 0 = parent's
}

// openSock opens both sockets on the parent. It returns the effective MTU.
func openSock(p openParams) (_ *sock, mtu int, err error) {
	s := &sock{rxfd: -1, txfd: -1, efd: -1, vlan: p.vlan, groups: map[[6]byte]bool{}}
	defer func() {
		if err != nil {
			s.closeFDs()
		}
	}()
	s.rxfd, err = unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
			err = errNoPermission
		}
		return
	}
	ifindex, flags, pmtu, err := parentInfo(s.rxfd, p.parent)
	if err != nil {
		return
	}
	if flags&unix.IFF_UP == 0 {
		return nil, 0, fmt.Errorf("parent interface %q is down", p.parent)
	}
	s.ifindex = ifindex
	mtu = pmtu
	if p.mtu > 0 && p.mtu < pmtu {
		mtu = p.mtu
	}
	if mtu < 576 {
		mtu = 576
	}

	// rx with a vnet header (gso type / size, checksum state of every frame);
	// a kernel that refuses it together with the ring gets a plain ring
	if err = s.setupRx(p, mtu, true); err != nil {
		if s.ring != nil {
			unix.Munmap(s.ring)
			s.ring = nil
		}
		unix.Close(s.rxfd)
		if s.rxfd, err = unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_CLOEXEC, 0); err != nil {
			return
		}
		if err = s.setupRx(p, mtu, false); err != nil {
			return
		}
	}
	if err = unix.Bind(s.rxfd, &unix.SockaddrLinklayer{Protocol: htons(unix.ETH_P_ALL), Ifindex: ifindex}); err != nil {
		err = fmt.Errorf("bind to %s: %w", p.parent, err)
		return
	}
	mreq := unix.PacketMreq{Ifindex: int32(ifindex), Type: unix.PACKET_MR_UNICAST, Alen: 6}
	copy(mreq.Address[:], p.mac[:])
	if unix.SetsockoptPacketMreq(s.rxfd, unix.SOL_PACKET, unix.PACKET_ADD_MEMBERSHIP, &mreq) != nil {
		mreq = unix.PacketMreq{Ifindex: int32(ifindex), Type: unix.PACKET_MR_PROMISC}
		if err = unix.SetsockoptPacketMreq(s.rxfd, unix.SOL_PACKET, unix.PACKET_ADD_MEMBERSHIP, &mreq); err != nil {
			err = fmt.Errorf("add membership: %w", err)
			return
		}
		s.promisc = true
	}
	// a bind on an interface that went down in between leaves ENETDOWN pending
	unix.GetsockoptInt(s.rxfd, unix.SOL_SOCKET, unix.SO_ERROR)
	if s.efd, err = unix.Eventfd(0, unix.EFD_NONBLOCK|unix.EFD_CLOEXEC); err != nil {
		return
	}

	if s.txfd, err = unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_CLOEXEC, 0); err != nil {
		return
	}
	if p.gso {
		if err = unix.SetsockoptInt(s.txfd, unix.SOL_PACKET, unix.PACKET_VNET_HDR, 1); err != nil {
			err = fmt.Errorf("PACKET_VNET_HDR: %w", err)
			return
		}
	}
	if unix.SetsockoptInt(s.txfd, unix.SOL_SOCKET, unix.SO_SNDBUFFORCE, 4<<20) != nil {
		unix.SetsockoptInt(s.txfd, unix.SOL_SOCKET, unix.SO_SNDBUF, 4<<20)
	}
	s.copyBuf = make([]byte, copyBufLen)
	return s, mtu, nil
}

// setupRx configures the rx socket (filter, options, vnet header, ring)
// before it is bound.
func (s *sock) setupRx(p openParams, mtu int, vnet bool) error {
	filter, err := buildFilter(p.mac, p.vlan, p.v6)
	if err != nil {
		return err
	}
	prog := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	if err := unix.SetsockoptSockFprog(s.rxfd, unix.SOL_SOCKET, unix.SO_ATTACH_FILTER, &prog); err != nil {
		return fmt.Errorf("attach filter: %w", err)
	}
	unix.SetsockoptInt(s.rxfd, unix.SOL_PACKET, packetIgnoreOutgoing, 1)
	if unix.SetsockoptInt(s.rxfd, unix.SOL_SOCKET, unix.SO_RCVBUFFORCE, 8<<20) != nil {
		unix.SetsockoptInt(s.rxfd, unix.SOL_SOCKET, unix.SO_RCVBUF, 8<<20)
	}
	s.rxVnet = false
	if vnet {
		// must precede PACKET_RX_RING
		if err := unix.SetsockoptInt(s.rxfd, unix.SOL_PACKET, unix.PACKET_VNET_HDR, 1); err != nil {
			return fmt.Errorf("rx PACKET_VNET_HDR: %w", err)
		}
		s.rxVnet = true
	}
	if err := unix.SetsockoptInt(s.rxfd, unix.SOL_PACKET, unix.PACKET_VERSION, unix.TPACKET_V2); err != nil {
		return fmt.Errorf("TPACKET_V2: %w", err)
	}
	unix.SetsockoptInt(s.rxfd, unix.SOL_PACKET, unix.PACKET_COPY_THRESH, 1)

	// ring: slots fit one MTU-sized frame (plus tpacket header, vnet header
	// and VLAN slack); bigger frames take the copy path
	s.frameSize = 2048
	for s.frameSize < mtu+ethHdrLen+vlanHdrLen+128 {
		s.frameSize *= 2
	}
	bsz := blockBytes
	if bsz < s.frameSize {
		bsz = s.frameSize
	}
	nblk := ringBytes / bsz
	if nblk < 2 {
		nblk = 2
	}
	s.frames = nblk * (bsz / s.frameSize)
	req := unix.TpacketReq{Block_size: uint32(bsz), Block_nr: uint32(nblk), Frame_size: uint32(s.frameSize), Frame_nr: uint32(s.frames)}
	if err := unix.SetsockoptTpacketReq(s.rxfd, unix.SOL_PACKET, unix.PACKET_RX_RING, &req); err != nil {
		return fmt.Errorf("PACKET_RX_RING: %w", err)
	}
	ring, err := unix.Mmap(s.rxfd, 0, bsz*nblk, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return fmt.Errorf("mmap ring: %w", err)
	}
	s.ring = ring
	return nil
}

func (s *sock) closeFDs() {
	if s.ring != nil {
		unix.Munmap(s.ring)
		s.ring = nil
	}
	for _, fd := range []*int{&s.rxfd, &s.txfd, &s.efd} {
		if *fd >= 0 {
			unix.Close(*fd)
			*fd = -1
		}
	}
}

// setGroups makes the parent's multicast filter accept exactly groups (IPv6
// solicited-node / all-nodes MACs). Falls back to ALLMULTI if refused.
func (s *sock) setGroups(groups map[[6]byte]bool) {
	if s.promisc || s.allmulti {
		return
	}
	mr := func(op int, g [6]byte) error {
		m := unix.PacketMreq{Ifindex: int32(s.ifindex), Type: unix.PACKET_MR_MULTICAST, Alen: 6}
		copy(m.Address[:], g[:])
		return unix.SetsockoptPacketMreq(s.rxfd, unix.SOL_PACKET, op, &m)
	}
	for g := range s.groups {
		if !groups[g] {
			mr(unix.PACKET_DROP_MEMBERSHIP, g)
			delete(s.groups, g)
		}
	}
	for g := range groups {
		if s.groups[g] {
			continue
		}
		if err := mr(unix.PACKET_ADD_MEMBERSHIP, g); err != nil {
			m := unix.PacketMreq{Ifindex: int32(s.ifindex), Type: unix.PACKET_MR_ALLMULTI}
			if unix.SetsockoptPacketMreq(s.rxfd, unix.SOL_PACKET, unix.PACKET_ADD_MEMBERSHIP, &m) == nil {
				s.allmulti = true
			}
			return
		}
		s.groups[g] = true
	}
}

func (s *sock) stop() {
	var one = [8]byte{1}
	unix.Write(s.efd, one[:])
}

// readCopy reads a frame that did not fit a ring slot from the socket
// queue. Queue entries that do not match the slot (left behind by frames the
// kernel dropped after queueing) are skipped.
func (s *sock) readCopy(tpLen uint32) (frame, vh []byte) {
	off := 0
	if s.rxVnet {
		off = vnetHdrLen
	}
	for k := 0; k < 8; k++ {
		n, _, err := unix.Recvfrom(s.rxfd, s.copyBuf, unix.MSG_DONTWAIT|unix.MSG_TRUNC)
		if err != nil {
			if errors.Is(err, unix.EAGAIN) {
				return nil, nil
			}
			continue // e.g. EINVAL: a frame the vnet header cannot describe
		}
		if n <= len(s.copyBuf) && n-off == int(tpLen) {
			if off > 0 {
				vh = s.copyBuf[:off]
			}
			return s.copyBuf[off:n], vh
		}
	}
	return nil, nil
}

func (s *sock) status(off int) *uint32 {
	return (*uint32)(unsafe.Pointer(&s.ring[off]))
}

// rxLoop reads the ring until stopped (nil) or the socket fails.
func (s *sock) rxLoop(l *linkEP) error {
	pfd := []unix.PollFd{{Fd: int32(s.rxfd), Events: unix.POLLIN}, {Fd: int32(s.efd), Events: unix.POLLIN}}
	ne := binary.NativeEndian
	idx, pending := 0, 0
	l.beginBatch()
	for {
		off := idx * s.frameSize
		st := atomic.LoadUint32(s.status(off))
		if st&tpStatusUser == 0 {
			if pending > 0 {
				l.endBatch()
				pending = 0
			}
			pfd[0].Revents, pfd[1].Revents = 0, 0
			if _, err := unix.Poll(pfd, -1); err != nil {
				if errors.Is(err, unix.EINTR) {
					continue
				}
				return err
			}
			if pfd[1].Revents != 0 {
				return nil
			}
			if atomic.LoadUint32(s.status(off))&tpStatusUser != 0 {
				l.beginBatch()
				continue
			}
			if pfd[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
				e, _ := unix.GetsockoptInt(s.rxfd, unix.SOL_SOCKET, unix.SO_ERROR)
				if e == 0 {
					return errors.New("packet socket error")
				}
				return fmt.Errorf("packet socket: %w", syscall.Errno(e))
			}
			l.beginBatch()
			continue
		}
		h := s.ring[off : off+s.frameSize]
		tpLen := ne.Uint32(h[4:])
		snap := ne.Uint32(h[8:])
		mac := int(ne.Uint16(h[12:])) // tpacket2_hdr: status, len, snaplen, mac, net, sec, nsec, vlan_tci
		tci := ne.Uint16(h[24:])
		var frame, vh []byte
		if st&tpStatusCopy != 0 {
			frame, vh = s.readCopy(tpLen)
		} else if snap == tpLen && mac+int(snap) <= s.frameSize {
			frame = h[mac : mac+int(snap)]
			if s.rxVnet && mac >= vnetHdrLen {
				vh = h[mac-vnetHdrLen : mac]
			}
		}
		vidOK := true
		if s.vlan != 0 {
			vidOK = st&tpStatusVLANValid != 0 && int(tci&0x0fff) == s.vlan
		} else if st&tpStatusVLANValid != 0 && tci&0x0fff != 0 {
			vidOK = false
		}
		if frame != nil && vidOK {
			l.deliver(frame, st, vh)
			pending++
		}
		atomic.StoreUint32(s.status(off), 0)
		idx++
		if idx == s.frames {
			idx = 0
		}
		if pending >= rxBatch {
			l.endBatch()
			l.beginBatch()
			pending = 0
		}
	}
}

// ---------------------------------------------------------------- link endpoint

// linkEP is the netstack's view of the child interface. The NIC (addresses,
// routes, neighbour cache, TCP endpoints) lives as long as the Iface; the
// sockets underneath are replaced when the parent goes away and comes back.
type linkEP struct {
	mac  tcpip.LinkAddress
	macB [6]byte
	vlan uint16
	gso  bool
	mtu  atomic.Uint32

	mu      sync.RWMutex
	sk      *sock
	disp    stack.NetworkDispatcher
	sa      [4]unix.RawSockaddrLinklayer // per ethertype: ipv4, ipv6, arp, vlan
	txErrCh chan struct{}

	g       gro.GRO // rx goroutine only
	rxBytes atomic.Uint64
	txBytes atomic.Uint64
	rxDrops atomic.Uint64
	segBuf  []byte // rx goroutine only
}

var _ stack.LinkEndpoint = (*linkEP)(nil)
var _ stack.GSOEndpoint = (*linkEP)(nil)

func newLinkEP(mac [6]byte, vlan int, gso bool) *linkEP {
	l := &linkEP{mac: tcpip.LinkAddress(mac[:]), macB: mac, vlan: uint16(vlan), gso: gso, txErrCh: make(chan struct{}, 1)}
	l.mtu.Store(1500)
	l.g.Init(true)
	return l
}

func (l *linkEP) setSock(s *sock, mtu int) {
	l.mu.Lock()
	l.sk = s
	if s != nil {
		l.mtu.Store(uint32(mtu))
		protos := []uint16{uint16(header.IPv4ProtocolNumber), uint16(header.IPv6ProtocolNumber), uint16(header.ARPProtocolNumber), etherVLAN}
		for i, p := range protos {
			l.sa[i] = unix.RawSockaddrLinklayer{Family: unix.AF_PACKET, Protocol: htons(p), Ifindex: int32(s.ifindex)}
		}
	}
	l.mu.Unlock()
}

func (l *linkEP) setGroups(groups map[[6]byte]bool) {
	l.mu.Lock()
	if l.sk != nil {
		l.sk.setGroups(groups)
	}
	l.mu.Unlock()
}

func (l *linkEP) MTU() uint32                             { return l.mtu.Load() }
func (l *linkEP) SetMTU(m uint32)                         { l.mtu.Store(m) }
func (l *linkEP) MaxHeaderLength() uint16                 { return uint16(ethHdrLen + l.vlanLen()) }
func (l *linkEP) LinkAddress() tcpip.LinkAddress          { return l.mac }
func (l *linkEP) SetLinkAddress(tcpip.LinkAddress)        {}
func (l *linkEP) ARPHardwareType() header.ARPHardwareType { return header.ARPHardwareEther }
func (l *linkEP) Close()                                  {}
func (l *linkEP) SetOnCloseAction(func())                 {}
func (l *linkEP) Wait()                                   {}
func (l *linkEP) Capabilities() stack.LinkEndpointCapabilities {
	// checksums of received frames are verified in deliver (or trusted when
	// the kernel says they are valid / locally generated)
	return stack.CapabilityResolutionRequired | stack.CapabilityRXChecksumOffload
}

func (l *linkEP) vlanLen() int {
	if l.vlan != 0 {
		return vlanHdrLen
	}
	return 0
}

func (l *linkEP) GSOMaxSize() uint32 {
	if l.gso {
		return gsoMaxSize
	}
	return 0
}

func (l *linkEP) SupportedGSO() stack.SupportedGSO {
	if l.gso {
		return stack.HostGSOSupported
	}
	return stack.GSONotSupported
}

func (l *linkEP) Attach(d stack.NetworkDispatcher) {
	l.mu.Lock()
	l.disp = d
	l.mu.Unlock()
}

func (l *linkEP) IsAttached() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.disp != nil
}

func (l *linkEP) AddHeader(pkt *stack.PacketBuffer) {
	src := pkt.EgressRoute.LocalLinkAddress
	if src == "" {
		src = l.mac
	}
	dst := pkt.EgressRoute.RemoteLinkAddress
	if l.vlan == 0 {
		eth := header.Ethernet(pkt.LinkHeader().Push(ethHdrLen))
		eth.Encode(&header.EthernetFields{SrcAddr: src, DstAddr: dst, Type: pkt.NetworkProtocolNumber})
		return
	}
	b := pkt.LinkHeader().Push(ethHdrLen + vlanHdrLen)
	copy(b[0:6], dst)
	copy(b[6:12], src)
	binary.BigEndian.PutUint16(b[12:], etherVLAN)
	binary.BigEndian.PutUint16(b[14:], l.vlan)
	binary.BigEndian.PutUint16(b[16:], uint16(pkt.NetworkProtocolNumber))
}

func (l *linkEP) ParseHeader(pkt *stack.PacketBuffer) bool {
	b, ok := pkt.LinkHeader().Consume(ethHdrLen)
	if !ok {
		return false
	}
	pkt.NetworkProtocolNumber = header.Ethernet(b).Type()
	return true
}

func (l *linkEP) saFor(p tcpip.NetworkProtocolNumber) *unix.RawSockaddrLinklayer {
	if l.vlan != 0 {
		return &l.sa[3]
	}
	switch p {
	case header.IPv4ProtocolNumber:
		return &l.sa[0]
	case header.IPv6ProtocolNumber:
		return &l.sa[1]
	default:
		return &l.sa[2]
	}
}

func (l *linkEP) vnetHdr(pkt *stack.PacketBuffer, b []byte) {
	ne := binary.NativeEndian // the kernel reads the header in host order (vio_le on LE hosts)
	for i := range b {
		b[i] = 0
	}
	if pkt.GSOOptions.Type == stack.GSONone {
		return
	}
	ne.PutUint16(b[2:], uint16(pkt.HeaderSize()))
	if pkt.GSOOptions.NeedsCsum {
		b[0] = vnetNeedsCsum
		ne.PutUint16(b[6:], uint16(ethHdrLen+l.vlanLen())+pkt.GSOOptions.L3HdrLen)
		ne.PutUint16(b[8:], pkt.GSOOptions.CsumOffset)
	}
	if uint16(pkt.Data().Size()) > pkt.GSOOptions.MSS {
		switch pkt.GSOOptions.Type {
		case stack.GSOTCPv4:
			b[1] = vnetGSOTCPv4
		case stack.GSOTCPv6:
			b[1] = vnetGSOTCPv6
		}
		ne.PutUint16(b[4:], pkt.GSOOptions.MSS)
	}
}

func (l *linkEP) txFailed() {
	select {
	case l.txErrCh <- struct{}{}:
	default:
	}
}

// WritePackets sends a batch with sendmmsg. A full socket buffer is waited
// out briefly instead of dropping straight away.
func (l *linkEP) WritePackets(pkts stack.PacketBufferList) (int, tcpip.Error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.sk == nil {
		// parent gone: like an unplugged cable, frames are lost (TCP retransmits)
		return pkts.Len(), nil
	}
	fd := l.sk.txfd
	list := pkts.AsSlice()
	hdrs := make([]rawfile.MMsgHdr, len(list))
	sizes := make([]int, len(list))
	var vnet []byte
	if l.gso {
		vnet = make([]byte, vnetHdrLen*len(list))
	}
	for i, pkt := range list {
		views := pkt.AsSlices()
		iovs := make([]unix.Iovec, 0, len(views)+1)
		if l.gso {
			vh := vnet[i*vnetHdrLen : (i+1)*vnetHdrLen]
			l.vnetHdr(pkt, vh)
			iovs = rawfile.AppendIovecFromBytes(iovs, vh, rawfile.MaxIovs)
		}
		for _, v := range views {
			iovs = rawfile.AppendIovecFromBytes(iovs, v, rawfile.MaxIovs)
			sizes[i] += len(v)
		}
		h := &hdrs[i].Msg
		h.Name = (*byte)(unsafe.Pointer(l.saFor(pkt.NetworkProtocolNumber)))
		h.Namelen = unix.SizeofSockaddrLinklayer
		h.Iov = &iovs[0]
		h.SetIovlen(len(iovs))
	}
	sent := 0
	var lastErr tcpip.Error
	waits := 0
	for sent < len(hdrs) {
		n, errno := rawfile.NonBlockingSendMMsg(fd, hdrs[sent:])
		switch {
		case errno == 0:
			for _, sz := range sizes[sent : sent+n] {
				l.txBytes.Add(uint64(sz))
			}
			sent += n
		case errno == unix.EINTR:
		case errno == unix.EAGAIN || errno == unix.ENOBUFS:
			if waits++; waits > 20 {
				return sent, &tcpip.ErrWouldBlock{}
			}
			pfd := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}
			unix.Poll(pfd, 5)
		case errno == unix.ENXIO || errno == unix.ENODEV || errno == unix.ENETDOWN:
			l.txFailed()
			return sent, tcpip.TranslateErrno(errno)
		default:
			// this one frame is unsendable (e.g. EMSGSIZE); skip it
			lastErr = tcpip.TranslateErrno(errno)
			hdrs = append(hdrs[:sent], hdrs[sent+1:]...)
			sizes = append(sizes[:sent], sizes[sent+1:]...)
		}
	}
	return sent, lastErr
}

// sendFrame writes one frame built here (gratuitous ARP / unsolicited NA).
func (l *linkEP) sendFrame(dst [6]byte, et uint16, payload []byte) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.sk == nil {
		return
	}
	var b []byte
	if l.gso {
		b = make([]byte, vnetHdrLen)
	}
	b = append(b, dst[:]...)
	b = append(b, l.macB[:]...)
	proto := et
	if l.vlan != 0 {
		b = binary.BigEndian.AppendUint16(b, etherVLAN)
		b = binary.BigEndian.AppendUint16(b, l.vlan)
		proto = etherVLAN
	}
	b = binary.BigEndian.AppendUint16(b, et)
	b = append(b, payload...)
	if unix.Sendto(l.sk.txfd, b, unix.MSG_DONTWAIT, &unix.SockaddrLinklayer{Protocol: htons(proto), Ifindex: l.sk.ifindex}) == nil {
		l.txBytes.Add(uint64(len(b)))
	}
}

// announce tells the segment that addr now lives at our MAC (gratuitous ARP
// request / unsolicited neighbour advertisement with the override flag), so
// stale entries for a previous owner of the address are replaced at once.
func (l *linkEP) announce(addr [16]byte, is4 bool) {
	if is4 {
		p := make([]byte, 28)
		binary.BigEndian.PutUint16(p[0:], 1)      // ethernet
		binary.BigEndian.PutUint16(p[2:], 0x0800) // ipv4
		p[4], p[5] = 6, 4
		binary.BigEndian.PutUint16(p[6:], 1) // request
		copy(p[8:14], l.macB[:])
		copy(p[14:18], addr[12:])
		copy(p[24:28], addr[12:])
		l.sendFrame([6]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, uint16(header.ARPProtocolNumber), p)
		return
	}
	allNodes := [16]byte{0xff, 0x02, 15: 1}
	icmp := make([]byte, 32)
	icmp[0] = 136                                    // neighbour advertisement
	binary.BigEndian.PutUint32(icmp[4:], 0x20000000) // override
	copy(icmp[8:24], addr[:])
	icmp[24], icmp[25] = 2, 1 // target link-layer address option
	copy(icmp[26:32], l.macB[:])
	sum := header.PseudoHeaderChecksum(header.ICMPv6ProtocolNumber, tcpip.AddrFrom16(addr), tcpip.AddrFrom16(allNodes), uint16(len(icmp)))
	binary.BigEndian.PutUint16(icmp[2:], ^checksum.Checksum(icmp, sum))
	ip := make([]byte, 40, 40+len(icmp))
	ip[0] = 0x60
	binary.BigEndian.PutUint16(ip[4:], uint16(len(icmp)))
	ip[6], ip[7] = 58, 255
	copy(ip[8:24], addr[:])
	copy(ip[24:40], allNodes[:])
	l.sendFrame([6]byte{0x33, 0x33, 0, 0, 0, 1}, uint16(header.IPv6ProtocolNumber), append(ip, icmp...))
}

// ---------------------------------------------------------------- receive

func (l *linkEP) beginBatch() {
	l.mu.RLock()
	l.g.Dispatcher = l.disp
	l.mu.RUnlock()
}

func (l *linkEP) endBatch() {
	if l.g.Dispatcher != nil {
		l.g.Flush()
	}
}

// deliver hands one received frame to the netstack. vh is the frame's
// virtio_net_hdr (nil without PACKET_VNET_HDR): partial checksums
// (NEEDS_CSUM) are completed, TCP GSO frames go up as one large segment (as
// after GRO), UDP GSO frames (UDP_SEGMENT senders on the same host) are split
// into the datagrams they stand for; other GSO types are dropped.
func (l *linkEP) deliver(frame []byte, status uint32, vh []byte) {
	if len(frame) < ethHdrLen || l.g.Dispatcher == nil {
		return
	}
	dst := frame[0:6]
	if dst[0]&1 == 0 && string(dst) != string(l.macB[:]) {
		l.rxDrops.Add(1)
		return
	}
	et := binary.BigEndian.Uint16(frame[12:14])
	switch et {
	case uint16(header.IPv4ProtocolNumber), uint16(header.IPv6ProtocolNumber):
		end := ipEnd(frame, et)
		if end < 0 {
			l.rxDrops.Add(1)
			return
		}
		l.rxBytes.Add(uint64(len(frame)))
		valid := status&(tpStatusCsumValid|tpStatusCsumNotReady) != 0
		gsoType := byte(0)
		if vh != nil {
			ne := binary.NativeEndian
			flags := vh[0]
			gsoType = vh[1] &^ vnetGSOECN
			if flags&vnetDataValid != 0 {
				valid = true
			}
			if flags&vnetNeedsCsum != 0 && gsoType != vnetGSOUDPL4 {
				if !completeCsum(frame[:end], int(ne.Uint16(vh[6:])), int(ne.Uint16(vh[8:]))) {
					l.rxDrops.Add(1)
					return
				}
				valid = true
			}
			switch gsoType {
			case vnetGSONone, vnetGSOTCPv4, vnetGSOTCPv6:
			case vnetGSOUDPL4:
				l.segmentUDP(frame[:end], et, int(ne.Uint16(vh[4:])))
				return
			default:
				l.rxDrops.Add(1)
				return
			}
		} else if end-ethHdrLen > int(l.mtu.Load()) && isUDP(frame[ethHdrLen:end], et) {
			// no vnet header (kernel refused it): a whole UDP packet above the
			// MTU is a GSO aggregate we cannot split; loss beats corruption
			l.rxDrops.Add(1)
			return
		}
		if !valid && !checksumOK(frame[ethHdrLen:end], et) {
			l.rxDrops.Add(1)
			return
		}
	case uint16(header.ARPProtocolNumber):
		l.rxBytes.Add(uint64(len(frame)))
	default:
		return
	}
	l.enqueue(frame, et)
}

func (l *linkEP) enqueue(frame []byte, et uint16) {
	pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(frame)})
	pkt.LinkHeader().Consume(ethHdrLen)
	pkt.NetworkProtocolNumber = tcpip.NetworkProtocolNumber(et)
	pkt.RXChecksumValidated = true
	l.g.Enqueue(pkt)
	pkt.DecRef()
}

// ipEnd returns the end of the IP packet in an Ethernet frame (frames may
// carry padding), or -1 when the IP length does not fit.
func ipEnd(frame []byte, et uint16) int {
	p := frame[ethHdrLen:]
	be := binary.BigEndian
	var n int
	if et == uint16(header.IPv4ProtocolNumber) {
		if len(p) < header.IPv4MinimumSize || p[0]>>4 != 4 {
			return -1
		}
		n = int(be.Uint16(p[2:4]))
		if n < int(p[0]&0x0f)*4 {
			return -1
		}
	} else {
		if len(p) < header.IPv6MinimumSize || p[0]>>4 != 6 {
			return -1
		}
		n = header.IPv6MinimumSize + int(be.Uint16(p[4:6]))
	}
	if n > len(p) {
		return -1
	}
	return ethHdrLen + n
}

// isUDP reports whether an IP packet is an unfragmented UDP datagram.
func isUDP(p []byte, et uint16) bool {
	if et == uint16(header.IPv4ProtocolNumber) {
		return p[9] == uint8(header.UDPProtocolNumber) && binary.BigEndian.Uint16(p[6:8])&0x3fff == 0
	}
	return p[6] == uint8(header.UDPProtocolNumber)
}

// completeCsum finishes a partial checksum (CHECKSUM_PARTIAL): the 16-bit
// field at start+off holds the pseudo-header sum; the checksum covers
// everything from start to the end of the packet.
func completeCsum(pkt []byte, start, off int) bool {
	pos := start + off
	if start < ethHdrLen || pos+2 > len(pkt) {
		return false
	}
	c := ^checksum.Checksum(pkt[start:], 0)
	if c == 0 && off == 6 { // UDP: 0 means "no checksum"
		c = 0xffff
	}
	binary.BigEndian.PutUint16(pkt[pos:], c)
	return true
}

// segmentUDP splits a UDP GSO frame (one UDP header, payload = n datagrams
// of gso bytes, the last one possibly shorter) into the datagrams, each with
// its own IP / UDP lengths, IPv4 id and header checksum, and UDP checksum.
func (l *linkEP) segmentUDP(frame []byte, et uint16, gso int) {
	be := binary.BigEndian
	p := frame[ethHdrLen:]
	v4 := et == uint16(header.IPv4ProtocolNumber)
	var l4 int
	if v4 {
		l4 = int(p[0]&0x0f) * 4
		if p[9] != uint8(header.UDPProtocolNumber) || be.Uint16(p[6:8])&0x3fff != 0 {
			l.rxDrops.Add(1)
			return
		}
	} else {
		l4 = header.IPv6MinimumSize
		if p[6] != uint8(header.UDPProtocolNumber) { // extension headers: not produced by UDP GSO
			l.rxDrops.Add(1)
			return
		}
	}
	hdr := ethHdrLen + l4 + header.UDPMinimumSize
	if gso <= 0 || len(frame) < hdr {
		l.rxDrops.Add(1)
		return
	}
	payload := frame[hdr:]
	if len(l.segBuf) < hdr+gso {
		l.segBuf = make([]byte, hdr+gso)
	}
	var src, dstA tcpip.Address
	if v4 {
		src, dstA = tcpip.AddrFrom4([4]byte(p[12:16])), tcpip.AddrFrom4([4]byte(p[16:20]))
	} else {
		src, dstA = tcpip.AddrFrom16([16]byte(p[8:24])), tcpip.AddrFrom16([16]byte(p[24:40]))
	}
	id := be.Uint16(p[4:6])
	for off := 0; off < len(payload); off += gso {
		chunk := payload[off:min(off+gso, len(payload))]
		seg := l.segBuf[:hdr+len(chunk)]
		copy(seg, frame[:hdr])
		copy(seg[hdr:], chunk)
		ip := seg[ethHdrLen:]
		udpLen := header.UDPMinimumSize + len(chunk)
		if v4 {
			be.PutUint16(ip[2:], uint16(l4+udpLen))
			be.PutUint16(ip[4:], id)
			id++
			be.PutUint16(ip[10:], 0)
			be.PutUint16(ip[10:], ^checksum.Checksum(ip[:l4], 0))
		} else {
			be.PutUint16(ip[4:], uint16(udpLen))
		}
		u := ip[l4:]
		be.PutUint16(u[4:], uint16(udpLen))
		be.PutUint16(u[6:], 0)
		c := ^checksum.Checksum(u, header.PseudoHeaderChecksum(header.UDPProtocolNumber, src, dstA, uint16(udpLen)))
		if c == 0 {
			c = 0xffff
		}
		be.PutUint16(u[6:], c)
		l.enqueue(seg, et)
	}
}

// checksumOK verifies the IPv4 header and TCP/UDP/ICMP checksums of a frame the
// kernel did not validate (no hardware RX checksum). Fragments, extension
// headers and anything else are left to the netstack.
func checksumOK(p []byte, et uint16) bool {
	be := binary.BigEndian
	switch et {
	case uint16(header.IPv4ProtocolNumber):
		if len(p) < header.IPv4MinimumSize {
			return false
		}
		ihl := int(p[0]&0x0f) * 4
		tot := int(be.Uint16(p[2:4]))
		if ihl < header.IPv4MinimumSize || len(p) < ihl || tot < ihl || tot > len(p) {
			return false
		}
		if checksum.Checksum(p[:ihl], 0) != 0xffff {
			return false
		}
		if be.Uint16(p[6:8])&0x3fff != 0 {
			return true
		}
		return l4OK(p[9], p[12:16], p[16:20], p[ihl:tot], true)
	case uint16(header.IPv6ProtocolNumber):
		if len(p) < header.IPv6MinimumSize {
			return false
		}
		end := header.IPv6MinimumSize + int(be.Uint16(p[4:6]))
		if end > len(p) {
			return false
		}
		return l4OK(p[6], p[8:24], p[24:40], p[header.IPv6MinimumSize:end], false)
	}
	return true
}

func l4OK(proto byte, src, dst, l4 []byte, v4 bool) bool {
	switch proto {
	case 6:
		if len(l4) < header.TCPMinimumSize {
			return false
		}
	case 17:
		if len(l4) < header.UDPMinimumSize {
			return false
		}
		if v4 && binary.BigEndian.Uint16(l4[6:8]) == 0 {
			return true
		}
	case 1:
		return !v4 || checksum.Checksum(l4, 0) == 0xffff
	case 58:
		if v4 {
			return true
		}
	default:
		return true
	}
	sum := header.PseudoHeaderChecksum(tcpip.TransportProtocolNumber(proto), tcpip.AddrFromSlice(src), tcpip.AddrFromSlice(dst), uint16(len(l4)))
	return checksum.Checksum(l4, sum) == 0xffff
}

// ---------------------------------------------------------------- probing

// vnetSupported reports whether PACKET_VNET_HDR can be set (decides GSO for
// the lifetime of an interface).
func vnetSupported() bool {
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return false
	}
	defer unix.Close(fd)
	return unix.SetsockoptInt(fd, unix.SOL_PACKET, unix.PACKET_VNET_HDR, 1) == nil
}

// ifindexOf is a cheap lookup used while waiting for a parent to (re)appear.
func ifindexOf(ctl int, name string) (int, bool, error) {
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		return 0, false, err
	}
	if err := unix.IoctlIfreq(ctl, unix.SIOCGIFINDEX, ifr); err != nil {
		return 0, false, err
	}
	idx := int(ifr.Uint32())
	if err := unix.IoctlIfreq(ctl, unix.SIOCGIFFLAGS, ifr); err != nil {
		return idx, false, err
	}
	return idx, ifr.Uint16()&unix.IFF_UP != 0, nil
}
