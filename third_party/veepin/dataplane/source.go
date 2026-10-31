package dataplane

import "net/netip"

// Inbound source enforcement: a server only delivers what a peer sends as its
// own address.
//
// Every server here shares one TUN between its clients, so once a packet is on
// it the kernel cannot tell which tunnel carried it. Reverse-path filtering
// does not help either: every client's address routes back out the same
// interface. The check therefore has to happen where the tunnel is still
// known, between decryption and the TUN write. WireGuard calls this the inbound
// half of cryptokey routing; RFC 4301 §5.2 requires the same of an IPsec SA
// against its traffic selectors; stock OpenVPN drops a packet from a client
// with "bad source address".

// SourceCheckedTunnel is an optional Tunnel capability: a tunnel that asks the
// pump to drop any decapsulated packet whose source address is not inside one
// of its own Routes.
//
// It is opt-in because the right answer differs by role. A server tunnel's
// Routes are its peer's assigned addresses, which is exactly what the peer may
// send as. A client tunnel's Routes are 0.0.0.0/0 or a split-tunnel set
// describing where it sends, not who may send to it, so a client keeps its own
// rule (WireGuard's AllowedIPs) or none.
type SourceCheckedTunnel interface {
	Tunnel
	VerifyInboundSource() bool
}

// InnerSource extracts the source address of an inner IP packet, for either
// family.
func InnerSource(pkt []byte) (netip.Addr, bool) {
	if len(pkt) < 1 {
		return netip.Addr{}, false
	}
	switch pkt[0] >> 4 {
	case 4:
		if len(pkt) < 20 {
			return netip.Addr{}, false
		}
		return netip.AddrFrom4([4]byte(pkt[12:16])), true
	case 6:
		if len(pkt) < 40 {
			return netip.Addr{}, false
		}
		return netip.AddrFrom16([16]byte(pkt[8:24])), true
	}
	return netip.Addr{}, false
}

// SourceWithin reports whether pkt's source address lies inside one of
// prefixes. A packet that is not IPv4 or IPv6 has no source to vouch for and
// is refused.
func SourceWithin(pkt []byte, prefixes []netip.Prefix) bool {
	src, ok := InnerSource(pkt)
	if !ok {
		return false
	}
	for _, p := range prefixes {
		if p.Contains(src) {
			return true
		}
	}
	return false
}

// SourceIs reports whether pkt's source address is exactly want.
func SourceIs(pkt []byte, want netip.Addr) bool {
	src, ok := InnerSource(pkt)
	return ok && want.IsValid() && src == want.Unmap()
}

// sourceOK applies b's inbound source rule to one decapsulated packet,
// counting the drop when it fails.
func (p *Pump) sourceOK(b bound, inner []byte) bool {
	if b.srcRoutes == nil || SourceWithin(inner, b.srcRoutes) {
		return true
	}
	p.drops[DropSpoofedSource].Add(1)
	return false
}
