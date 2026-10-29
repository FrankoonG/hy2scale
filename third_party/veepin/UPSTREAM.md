# veepin (vendored, patched)

Upstream: https://github.com/xen0bit/veepin (MIT, see LICENSE)
Commit:   bcac178 "docs(readme): refresh interop results from CI"

Only the packages hy2scale uses are copied (IKEv2, L2TP/IPsec, OpenVPN and
their internal dependencies), without tests. The module path is unchanged and
the root `go.mod` points at this directory with a `replace` directive.

## Patches (`hy2scale.patch`)

Every change is marked `hy2scale P<n>` in the source.

- **P1 — packet device.** `dataplane.Device`; `ikev2`, `l2tp` and `openvpn`
  server configs take a `Device` used instead of opening `/dev/net/tun`.
- **P2 — injected sockets.** `dataplane.Datagram` / `NewVirtualPacketConn`,
  and a `ListenPacket` hook on each server, so one process can share UDP
  500/4500 between IKEv2 and L2TP/IPsec. `PacketConn.ReadFromUDPLocal` and
  `LocalFor` expose the per-peer local address; L2TP uses it as its IKE
  identity when listening on the wildcard.
- **P3 — credentials and sessions.** Per-attempt credential lookups
  (`EAPLookup`, `UserLookup`) and an `OnSession(up, user, addr)` callback
  reporting each client's tunnel address with the user it authenticated as.
  The EAP identity now survives an IKE SA rekey and is what `Peers()` reports.
- **P4 — OpenVPN auth-user-pass.** `VerifyUser` checks key-method-2
  credentials; with it set the client certificate becomes optional and a
  failure is answered with `AUTH_FAILED` as the reply to `PUSH_REQUEST`,
  after the key exchange, as OpenVPN's own server does.
- **P5 — EAP with a server certificate.** When a certificate is configured
  the responder signs its EAP-phase AUTH with it (and sends CERT) instead of
  using the PSK; native Windows / Apple / Android EAP clients require this.
- **P6 — batched egress for userspace devices.** A `Device` implementing
  `BatchReader` lets the pump encapsulate a burst and flush it with one
  batched send; `DatagramBatch` carries that burst to an injected socket as
  one `sendmmsg`.
- **P7 — IPv6 leases in sessions.** `OnSession` is called once per tunnel
  address: the IPv4 lease and, on a dual-stack session, the IPv6 one (IKEv2
  from `Pool6`, OpenVPN derived from `Pool6`). An IKE SA rekey also moves the
  v6 lease off the retiring SA, so deleting it no longer releases the
  addresses its successor still holds.
- **P8 — IKEv1 suites.** The responder takes AES-CBC 128/192/256 with SHA-1
  or SHA2-256/384/512 over MODP-1024/2048/3072/4096 or ECP-256/384/521 in
  phase 1, and AES-CBC (same integrity) or AES-GCM-16 for ESP, with PFS over
  any of those groups (KE in Quick Mode, g^xy in KEYMAT). Every Proposal
  payload is read, not just the first, and the chosen transform is echoed as
  offered (lifetime included or absent). An offer with nothing acceptable is
  answered with NO-PROPOSAL-CHOSEN: a plaintext Informational in Main Mode, a
  protected one in Quick Mode. iKuai's L2TP client offers only
  AES-128/SHA2-256/MODP-3072.
- **P9 — L2TP peer port.** Replies go to the inner UDP source port of the
  peer's last authenticated L2TP datagram (source stays 1701). xl2tpd as a
  LAC binds its own port; native clients use 1701 on both ends.
- **P10 — CCP/MPPE and Protocol-Reject.** The PPP server answers CCP with
  128-bit MPPE (stateless or stateful, RFC 3078/3079), keyed from the
  MS-CHAPv2 exchange; other compression is rejected, so clients without MPPE
  keep a plain link. Protocols the server does not run draw an LCP
  Protocol-Reject instead of silence. pppd with `mppe required` (iKuai) needs
  this.
- **P11 — OpenVPN MTU and OCC.** The OCC options string reports the
  configured MTU; `tun-mtu` is pushed only to clients that apply it (IV_MTU
  or 2.6+) and `route-ipv6-gateway` only to 2.5+, since older clients refuse
  them; OCC messages on the data channel (the UDP explicit-exit-notify) are
  no longer logged as malformed data.
- **P12 — ESP SPI claims.** `dataplane.ESPClaimer` / `PacketConn.ClaimESP`:
  the IKEv2 data path and the L2TP server claim each inbound SPI on their
  NAT-T socket and release it with the SA, so an embedder sharing 4500
  between them routes ESP by SPI. One IKE daemon running an IKEv2 client and
  an L2TP/IPsec client to the same server sends both from one address and
  port, which routing by sender cannot split.

## Updating

Copy the same package list from a new upstream commit (non-test `.go` files,
LF line endings), then `patch -p1 < hy2scale.patch` and fix any rejects.
