# iKuai Router Compatibility Research

## Kernel: Linux 5.10.194 (iKuai custom build)
## Docker: 18.09.9 at `/tmp/ikpkg/docker-bin/`

## Architecture Overview

```
testIptablesAvailable()
├── true  → Standard path: iptables DNAT + transparent proxy (normal Linux, no new code)
└── false → Compat path: TUN capture + gvisor netstack (iKuai / incompatible kernels)
                ├── L2TP: policy routing → kernel TUN → gvisor forwarders
                └── IKEv2: xfrm interface → AF_PACKET bridge → gvisor forwarders
```

**Key guarantee**: On normal Linux where iptables works, NONE of the compat code executes.
Verified on 10.130.32.32 (Ubuntu, kernel 6.8.0): zero compat/xfrm/TUN capture logs.

## Core Findings

### iptables in Container Namespaces
- **iptables-legacy 1.8.3 (Alpine compiled)**: `ERROR: 0 not a valid target` in ANY namespace
- **iptables-legacy 1.8.3 (iKuai native)**: works in host namespace only
- **iptables nft 1.8.10 (Alpine)**: `Could not fetch rule set generation id` everywhere
- **Root cause**: iKuai kernel's netfilter uses custom target IDs (IKQUEUE, EMARK, APPMARK, etc.)
  that standard iptables binaries don't understand. GET_ENTRIES returns empty data in containers.
- **Workaround**: `chroot /host /usr/sbin/iptables` (mount host root as `/host`)
  works because it uses iKuai's own glibc-linked iptables against its own kernel.
- **chroot only works with --network host** (same network namespace as host).

### iptables FORWARD Chain in Container Namespaces
- Docker sets FORWARD chain default policy to DROP in container network namespaces
- Cannot change via iptables binary (broken targets)
- Cannot change via raw getsockopt/setsockopt (kernel returns empty entries in containers)
- **Impact**: Kernel forwarding from xfrm interfaces to TUN is blocked
- **Solution**: AF_PACKET bridge bypasses FORWARD chain entirely (reads at link layer, never enters routing/forwarding)

### Policy Routing (`ip rule + ip route`)
- **Works** in container namespaces: `ip rule add from X lookup Y` ✓
- **Works** with kernel TUN: `ip route add default dev hy2cap0 table Y` ✓
- **Works** for PPP traffic: packets from `ppp0` interface correctly enter TUN ✓
- **Works** for xfrm forwarded traffic (`ip route get` confirms correct route)
- **BUT**: Forwarded packets (xfrm→TUN) are dropped by netfilter FORWARD chain

### L2TP in Non-Host Network (TUN Capture Mode)
```
PPP Client → ppp0 (kernel) → ip rule from 192.168.25.0/24 → hy2cap0 (kernel TUN)
    → gvisor netstack → TCP/UDP forwarders → net.DialTimeout / dialExit → internet
```
- **Status: WORKING** ✅
- PPP creates per-client kernel interfaces (ppp0, ppp1, etc.)
- PPP traffic is LOCAL (not forwarded), so FORWARD chain doesn't affect it
- Policy routing captures outbound traffic from PPP subnet → TUN → gvisor
- gvisor TCP forwarder intercepts, dials real destination, relays
- UDP forwarder handles DNS and general UDP
- User identification: pppSessions maps IP → username via ip-up hooks
- Exit routing: per-user ExitVia works through forwarder

### IKEv2 in Non-Host Network (xfrm Bridge Mode)
```
IKEv2 Client → ESP tunnel → xfrm decrypt → ikecN interface
    → AF_PACKET socket captures → gvisor netstack → TCP/UDP forwarders
    → dialExit(AUB) → internet

Response: internet → forwarder → gvisor → raw IP socket → ikecN
    → kernel xfrm encapsulate → ESP → client
```
- **Status: WORKING** ✅ (via AF_PACKET bridge, bypassing FORWARD chain)
- swanctl.conf with `if_id_in/if_id_out = %unique` creates per-client xfrm interfaces
- Updown script creates `ikecN` interface, notifies hy2scale via webhook
- hy2scale opens AF_PACKET SOCK_RAW on each xfrm interface
- Inbound: AF_PACKET captures decapsulated packets at link layer (before FORWARD)
- Filter `PACKET_OUTGOING` (pkttype=4) to prevent infinite capture loops
- Inject captured IP packets into shared gvisor netstack
- Outbound: raw IP socket (`IPPROTO_RAW` + `IP_HDRINCL` + `SO_BINDTODEVICE=ikecN`)
  sends response through xfrm interface → kernel ESP encapsulation → client
- **MTU**: gvisor netstack MTU = 1300 (ESP adds ~60-80 bytes overhead; 1300+80 < 1500 eth MTU)
- **Verified**: `curl --interface 10.10.10.2 http://ifconfig.me` returns AU server IP (38.180.128.200)


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
    // NO new code runs. Exact same path as before.
} else {
    // Compat: TUN capture + gvisor (iKuai / incompatible kernels)
    ensureTunCapture(a, subnet)          // shared kernel TUN + gvisor
    startXfrmBridge(ctx, ifName, ep)      // per-client AF_PACKET bridge (IKEv2 only)
}
```

`testIptablesAvailable()` tries `iptExec("iptables-legacy", "-t", "nat", "-L", "-n")`.
This handles both native iptables and chroot fallback (detected by `iptUseChroot`).

## MTU Considerations

| Path | Inner MTU | Overhead | Total | Fits 1500? |
|------|-----------|----------|-------|------------|
| L2TP PPP (compat) | 1300 | ~40 (PPP/L2TP) | ~1340 | ✅ |
| IKEv2 ESP (compat) | 1300 | ~80 (ESP+outer IP) | ~1380 | ✅ |
| L2TP PPP (standard) | 1500 | N/A (iptables DNAT) | N/A | ✅ |
| IKEv2 ESP (standard) | 1500 | N/A (iptables DNAT) | N/A | ✅ |

gvisor netstack MTU set to 1300 in compat mode to ensure ESP-encapsulated packets
fit within the 1500-byte ethernet MTU. TCP MSS adjusts automatically.

## Shared IKE Port Issue

L2TP and IKEv2 share strongswan (charon) which binds UDP 500/4500.
If L2TP starts first, IKEv2's port check would detect 500/4500 as "in use".
Fixed: skip IKEv2 port check when L2TP is also enabled (they share charon).

## Files

### New (compat mode)
- `internal/app/tun_capture.go` — kernel TUN + gvisor netstack + TCP/UDP forwarders
- `internal/app/tun_capture_linux.go` — Linux syscall wrapper for TUN ioctl
- `internal/app/xfrm_bridge_linux.go` — AF_PACKET bridge: xfrm interfaces ↔ gvisor

### Modified
- `internal/app/l2tp.go` — iptables test + TUN capture fallback branch
- `internal/app/ikev2.go` — xfrm interface + AF_PACKET bridge branch, swanctl if_id config
- `Dockerfile` — iptables-legacy 1.8.3 from source

### Unchanged on normal Linux path
- All standard iptables DNAT + transparent proxy code remains unchanged
- `testIptablesAvailable()` gates all compat code

## Deployment

### Option A: Non-host network (L2TP + IKEv2, recommended for port isolation)
```bash
docker run -d --name hy2scale \
  --network hy2net --ip 192.168.10.4 \
  --privileged \
  -v /etc/disk_user/main/hy2scale:/data \
  -p 5565:5565/tcp -p 5565:5565/udp \
  -p 1701:1701/udp \
  hy2scale:latest
```
- L2TP: works (TUN capture compat mode)
- IKEv2: works (xfrm bridge compat mode, verified with AU exit)
- WireGuard: works (pure userspace)
- No port conflicts with host services

### Option B: Host network (maximum compatibility)
```bash
docker run -d --name hy2scale \
  --network host --privileged --pid=host \
  -v /etc/disk_user/main/hy2scale:/data \
  -v /:/host:ro \
  hy2scale:latest
```
- L2TP: works (chroot iptables)
- IKEv2: works only if host charon not running on 500/4500
- WireGuard: works on any port

## Test Results

| Environment | L2TP | IKEv2 | IKEv2 Exit via AU | WireGuard |
|-------------|------|-------|-------------------|-----------|
| Normal Linux (host network) | ✅ iptables | ✅ iptables | ✅ | ✅ |
| iKuai (host network + chroot) | ✅ chroot ipt | ✅ chroot ipt | Not tested | ✅ |
| iKuai (non-host, compat) | ✅ TUN capture | ✅ xfrm bridge | ✅ 38.180.128.200 | ✅ |

Verified after iKuai reboot (2026-03-25): image reloaded from persistent storage,
container restarted, IKEv2 exit via AU confirmed working.

---

## Dead Ends (approaches that FAILED, don't retry)

### 1. Raw getsockopt to modify iptables FORWARD chain (Go)
- Tried reading filter table via `IPT_SO_GET_ENTRIES` from Go using `syscall.Syscall6`
- `sizeof(ipt_get_entries)` = 40 on x86_64 (not 36) due to flexible array alignment
- `offsetof(entrytable)` = 40 (not 36)
- Buffer size check passes, but entries data is ALL ZEROS
- **Root cause**: iKuai kernel returns empty entries for the filter table in container namespaces
- Go GC/memory issues also suspected (unsafe.Pointer → uintptr conversion loses GC tracking)
- **Conclusion**: Cannot modify iptables in iKuai container namespaces by any means

### 2. C helper binary (ipt_forward_accept.c)
- Compiled statically with Alpine's linux-headers
- Same result: GET_ENTRIES returns valid size (728 bytes) but entry data is zeros
- `target_offset=0, next_offset=0` — clearly empty
- SET_REPLACE fails because modified entries are still zeros
- **Removed**: code deleted, serves no purpose

### 3. AF_PACKET SOCK_DGRAM for xfrm write
- SOCK_DGRAM writes to xfrm interface (ARPHRD_NONE) don't trigger ESP encapsulation properly
- Packet written via AF_PACKET bypasses xfrm transform layer
- xfrm `oseq` increments but client doesn't receive data
- **Fix**: Use raw IP socket (`IPPROTO_RAW` + `IP_HDRINCL` + `SO_BINDTODEVICE`) for writes

### 4. AF_PACKET without PACKET_OUTGOING filter
- AF_PACKET SOCK_RAW captures BOTH inbound and outbound on the interface
- Response packets written to xfrm are re-captured and re-injected into gvisor
- Creates infinite loop, saturates CPU, makes `docker exec` hang
- **Fix**: Filter `sa.Pkttype == 4` (PACKET_OUTGOING) in receive loop

### 5. gvisor MTU 1500 for IKEv2
- TCP handshake works (small packets), but data transfer fails
- Inner TCP segments ~1460 bytes + ESP overhead ~80 bytes = ~1540 > 1500 MTU
- Kernel drops oversized ESP packets (DF bit set), ICMP frag-needed can't reach gvisor
- **Fix**: Set gvisor MTU to 1300 (tunCaptureMTU constant)

### 6. Docker --data-root on persistent storage
- `/etc/disk_user/main/Docker` as data-root: I/O extremely slow
- `docker load` takes 5+ minutes for 25MB tar
- `docker run` and `docker rm -f` hang for minutes
- After reboot: zombie docker-proxy, OCI runtime errors, corrupted overlay2
- **Conclusion**: Always use default `/tmp/lib/docker` (tmpfs, fast). Reload after reboot.

### 7. Docker --dns flag on custom networks
- `--dns 8.8.8.8` does NOT override Docker's embedded DNS (127.0.0.11) on bridge networks
- Docker 18.09.9 behavior: resolv.conf always gets `nameserver 127.0.0.11`
- **Fix**: `docker exec hy2scale sh -c "echo nameserver 8.8.8.8 > /etc/resolv.conf"` post-start

### 8. Docker bridge conflicts after reboot
- Each `docker network create` creates a new `br-XXXXX` bridge
- Old bridges from previous dockerd runs persist in kernel namespace
- Two bridges with same IP (192.168.10.1/24) → routing conflict → container unreachable
- `ip route get` confirms packets routed to wrong (DOWN) bridge
- **Fix**: `ip link del br-XXXXX` for any NO-CARRIER bridge with conflicting IP

---

## iKuai System Details

### Custom kernel extensions
- Custom iptables targets: IKQUEUE, EMARK, APPMARK, NTH_CONNMARK, FULLCONENAT, IMQ, MIRROR
- Custom iptables matches: ikverdict, suffix, local_host, ifaces, bytesband, matchcount, ifname, emark, appmark, high_prio_host, urlroute, byteslimit, peerconns
- These cause `ERROR: 0 not a valid target` when standard iptables tries to parse the filter table
- `GET_ENTRIES` returns zero-filled entries in container namespaces (security restriction?)

### Docker 18.09.9 quirks on iKuai
- containerd must be in PATH when starting dockerd
- `docker load` serializes internally — parallel loads cause corruption
- `docker rm -f` can hang indefinitely if containerd-shim is stuck
- `docker restart` often hangs; prefer `docker rm -f` + `docker run`
- `docker exec` hangs if container's network is saturated (e.g., AF_PACKET loop)
- Image size on disk: hy2scale ~55MB, alpine ~7.4MB, test-client ~20MB

### Filesystem
- `/tmp` — tmpfs, fast, cleared on reboot
- `/etc/disk_user/main/` → `/etc/disk/UUID/` — persistent, slow I/O (~5MB/s reads)
- `/var/lib` → `/tmp/lib` (symlink)
- Docker default data-root: `/var/lib/docker` → `/tmp/lib/docker`
