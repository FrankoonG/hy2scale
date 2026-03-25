# iKuai Router Compatibility Research

## Kernel: Linux 5.10.194 (iKuai custom build)
## Docker: 18.09.9 at `/tmp/ikpkg/docker-bin/`

## Core Findings

### iptables in Container Namespaces
- **iptables-legacy 1.8.3 (Alpine compiled)**: `ERROR: 0 not a valid target` in ANY namespace
- **iptables-legacy 1.8.3 (iKuai native)**: works in host namespace only
- **iptables nft 1.8.10 (Alpine)**: `Could not fetch rule set generation id` everywhere
- **Root cause**: iKuai kernel's netfilter implementation is incompatible with
  standard iptables binaries compiled against musl libc (Alpine). The kernel
  uses custom target IDs that standard iptables doesn't understand.
- **Workaround**: `chroot /host /usr/sbin/iptables` (mount host root as `/host`)
  works because it uses iKuai's own glibc-linked iptables against its own kernel.
- **chroot only works with --network host** (same network namespace as host).

### Policy Routing (`ip rule + ip route`)
- **Works** in container namespaces: `ip rule add from X lookup Y` ✓
- **Works** with kernel TUN: `ip route add default dev hy2cap0 table Y` ✓
- **Works** for PPP traffic: packets from `ppp0` interface correctly enter TUN ✓
- **Does NOT work** for xfrm forward: IKEv2 ESP-decapsulated packets bypass `ip rule`

### L2TP in Non-Host Network (TUN Capture Mode)
```
PPP Client → ppp0 (kernel) → ip rule from 192.168.25.0/24 → hy2cap0 (kernel TUN)
    → gvisor netstack → TCP/UDP forwarders → net.DialTimeout → internet
```
- **Status: WORKING** ✅
- PPP creates per-client kernel interfaces (ppp0, ppp1, etc.)
- Each ppp interface has a point-to-point route
- Policy routing captures outbound traffic from PPP subnet
- gvisor TCP forwarder intercepts, dials real destination, relays
- UDP forwarder handles DNS and general UDP
- User identification: pppSessions maps IP → username via ip-up hooks
- Exit routing: per-user ExitVia works through forwarder

### IKEv2 in Non-Host Network
```
IKEv2 Client → ESP tunnel → xfrm decrypt → (should hit ip rule) → hy2cap0
```
- **Status: NOT WORKING** ❌
- IKE_SA established successfully
- CHILD_SA established, virtual IP assigned (10.10.10.2)
- ESP tunnel functional (xfrm state/policy correct)
- **Problem**: xfrm forward-decoded packets (src=10.10.10.2 dst=any)
  do NOT enter `ip rule` routing on iKuai kernel
- TUN `hy2cap0` receives 0 bytes despite policy routing being set
- **Conclusion**: iKuai kernel's xfrm implementation skips policy routing
  for forwarded/decapsulated packets. This is a kernel-level limitation.

### IKEv2 in Host Network
- Works with `--network host + --privileged + chroot /host iptables`
- **Conflict**: host's charon occupies UDP 500/4500
- hy2scale's strongswan can coexist if host charon is stopped

### WireGuard
- Works in any network mode (no iptables needed)
- Pure userspace: wireguard-go + gvisor netstack
- No port conflicts (configurable port)

## Auto-Detection Logic

```go
// In StartL2TP / StartIKEv2:
if testIptablesAvailable() {
    // Standard: iptables DNAT + transparent proxy (normal Linux)
} else {
    // Compat: TUN capture + gvisor (iKuai / incompatible kernels)
    ensureTunCapture(a, subnet)
}
```

`testIptablesAvailable()` tries `iptExec("iptables-legacy", "-t", "nat", "-L", "-n")`.
This handles both native iptables and chroot fallback (detected by `iptUseChroot`).

## Shared IKE Port Issue

L2TP and IKEv2 share strongswan (charon) which binds UDP 500/4500.
If L2TP starts first, IKEv2's port check would detect 500/4500 as "in use".
Fixed: skip IKEv2 port check when L2TP is also enabled (they share charon).

## Files
- `internal/app/tun_capture.go` — kernel TUN + gvisor netstack + forwarders
- `internal/app/tun_capture_linux.go` — Linux syscall wrapper
- `internal/app/l2tp.go` — iptables test + TUN capture fallback
- `internal/app/ikev2.go` — same fallback (but xfrm routing issue on iKuai)
- `Dockerfile` — iptables-legacy 1.8.3 compiled from source
