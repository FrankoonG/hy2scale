<p align="center">
  <img src=".github/assets/logo.svg" width="120" alt="HY2 SCALE">
</p>

<h1 align="center">HY2 SCALE</h1>

<p align="center">
  A mesh relay network built on <a href="https://github.com/apernet/hysteria">Hysteria 2</a> QUIC tunnels.<br>
  Connect devices across NATs, route traffic through any node, and run VPN services — all from a web UI.
</p>

---

## Why HY2 SCALE?

Like Tailscale, HY2 SCALE lets devices behind NAT reach each other through relay nodes with public IPs. Unlike Tailscale:

- **Built on Hysteria 2 (QUIC)** — Designed to saturate your ISP's rated bandwidth, not limited by traditional TCP-based tunnels. On lossy or high-latency links, QUIC's loss recovery outperforms WireGuard and OpenVPN.
- **Any node is an exit** — Traffic can exit from any node in the mesh, including nodes behind NAT (reached via relay). A user in Tokyo can exit through a home server in Sydney that has no public IP.
- **Any node is a VPN server** — Each node can serve L2TP/IPsec, IKEv2, SOCKS5, or Shadowsocks. Connect your phone's native VPN client directly to any node.
- **No coordination server** — Nodes connect to each other directly. No central control plane, no account registration, no third-party dependency.

## Concepts

### Node

A node is a machine running HY2 SCALE. Each node has a unique ID, runs a Hysteria 2 QUIC server, and connects to other nodes as peers. Nodes discover each other's sub-peers automatically, forming a multi-level mesh.

A node can be:
- A **cloud server** with a public IP (relay for NAT'd devices)
- A **home server** behind NAT (reachable via a relay node)
- A **NAS, router, or any Docker-capable device**

### Exit

An exit is the node where a user's traffic leaves the mesh and reaches the internet. Each user can be assigned a different exit node — or a chain of nodes (e.g., `Japan-01/US-01` routes through Japan-01, then exits from US-01).

If no exit is specified, traffic exits directly from the node the user connected to.

### VPN Server

Every node can act as a VPN access point. When you enable L2TP, IKEv2, SOCKS5, or Shadowsocks on a node, users can connect to it using their device's built-in VPN client (iOS, macOS, Windows, Android — no app install required for L2TP and IKEv2). The node then routes the user's traffic through the mesh to their assigned exit.

```
┌──────────┐      ┌──────────────┐      ┌──────────┐      ┌──────────┐
│  Phone   │─VPN─▶│  Node A      │─QUIC─▶│  Node B  │─QUIC─▶│  Node C  │──▶ Internet
│ (iOS)    │      │  (L2TP/IKEv2)│      │  (relay) │      │  (exit)  │
└──────────┘      └──────────────┘      └──────────┘      └──────────┘
                   public IP             behind NAT        public IP
```

## Features

- **Mesh Topology** — Peer-to-peer mesh with automatic discovery, nested peers, and multi-level nesting. Native Hysteria 2 servers are detected and integrated seamlessly.

- **Per-User Exit Routing** — Each user can be assigned a different exit path through the mesh. Traffic is transparently routed through relay chains with per-user traffic accounting.

- **VPN Protocols** — Built-in SOCKS5, Shadowsocks, L2TP/IPsec, and IKEv2/IPsec servers. L2TP and IKEv2 use the OS native VPN client — no third-party app needed.

- **Real-Time Monitoring** — Live latency probing, per-peer traffic rates, connection status, and nested topology visualization in a single-page web UI.

- **TLS Management** — Generate self-signed certificates, import from PEM or file path, with full lifecycle management.

- **Docker-Only Deployment** — No config files needed. All state persists to a `/data` volume. Single binary, single container.

## Screenshots

### Nodes

Multi-level mesh topology with real-time latency, nested peer discovery, and native Hysteria 2 compatibility.

![Nodes](.github/assets/nodes.png)

### Users

Per-user exit routing with traffic limits, usage tracking, and reachability indicators. Green hops are reachable through the mesh; red hops indicate unreachable or non-existent nodes.

![Users](.github/assets/users.png)

## Quick Start

```bash
docker run -d --name hy2scale \
  --network host \
  --cap-add NET_ADMIN \
  --device-cgroup-rule='c 108:0 rwm' \
  -v hy2scale-data:/data \
  frankoong/hy2scale:latest
```

Open `http://<host>:5565/scale/` — default login: `admin` / `admin`

> **Note:** `--network host` is required for L2TP/IPsec and IKEv2 VPN services. Without it, these features are disabled (shown as "Limited" in the UI). SOCKS5, Shadowsocks, and the mesh relay work with standard port mapping.

### Minimal (relay only, no VPN)

```bash
docker run -d --name hy2scale \
  -p 5565:5565/tcp -p 5565:5565/udp \
  -v hy2scale-data:/data \
  frankoong/hy2scale:latest
```

### Docker Hub

```
frankoong/hy2scale:latest
frankoong/hy2scale:1.0.1
```

## Architecture

```
                    ┌──────────┐
                    │   Hub    │
                    └────┬─────┘
               ┌─────────┼──────────┐
          ┌────┴───┐ ┌───┴────┐ ┌───┴────────┐
          │Japan-01│ │  SG-01 │ │ native-hy2 │
          └───┬────┘ └───┬────┘ └────────────┘
           ┌──┴──┐    ┌──┴──┐       [NATIVE]
          US-01 US-02 AU-01
                       │
                      NZ-01
```

Each node runs a Hysteria 2 server and connects to peers via QUIC tunnels. The relay protocol handles:

- **Peer discovery** — Nodes exchange peer lists, enabling multi-level nested topology
- **Latency probing** — Background ping with cross-node latency reporting
- **Traffic routing** — `DialTCP` for direct peers, `DialVia` for multi-hop chains
- **Native hy2 compat** — Auto-detect plain Hysteria 2 servers (no relay protocol)

## VPN Services

| Protocol | Auth | Exit Routing | Notes |
|----------|------|--------------|-------|
| SOCKS5 | Username/Password | Per-user exit_via | RFC 1929 auth |
| Shadowsocks | Per-user key | Per-user exit_via | AEAD ciphers |
| L2TP/IPsec | MSCHAPv2 | Per-user exit_via | iOS/macOS/Windows/Android native client |
| IKEv2/IPsec | EAP-MSCHAPv2 or PSK | Per-user or global exit | iOS/macOS/Windows/Android native client |

### L2TP/IPsec & IKEv2/IPsec

These protocols use the operating system's **built-in VPN client** — no third-party app required. Users connect directly from iOS Settings, macOS System Preferences, Windows VPN settings, or Android VPN settings.

- **L2TP** uses IKEv1 + xl2tpd + PPP (MSCHAPv2 auth). Compatible with virtually every OS.
- **IKEv2** uses certificate-based authentication with EAP-MSCHAPv2 for user credentials. Supports both certificate mode (import your own CA) and PSK mode.

Both protocols require `--network host` and `--cap-add NET_ADMIN`:

```bash
docker run -d --name hy2scale \
  --network host \
  --cap-add NET_ADMIN \
  --device-cgroup-rule='c 108:0 rwm' \
  -v hy2scale-data:/data \
  frankoong/hy2scale:latest
```

> Without `--network host`, the web UI shows **"v1.x.x Limited"** and L2TP/IKEv2 panels are disabled. This is because IPsec requires direct access to the host network stack — Docker port mapping cannot handle ESP tunnel/transport mode packets. All other features (mesh relay, SOCKS5, Shadowsocks, web UI) work normally with standard port mapping.

## Configuration

All configuration is managed through the web UI. No config files to edit. State is persisted to the `/data` volume:

```
/data/
├── node-id          # Unique node identifier (editable)
├── config.yaml      # Auto-generated, atomic writes
└── tls/             # Certificates (PEM)
    ├── default.crt
    ├── default.key
    └── default.name
```

## Building from Source

```bash
git clone https://github.com/FrankoonG/hy2scale.git
cd hy2scale
docker build -t hy2scale .
```

The image includes:
- Go binary (statically compiled)
- strongSwan 5.8.4 (compiled from source, IKEv1 + IKEv2)
- xl2tpd, pppd, iptables-legacy
- ~73MB total

## License

MIT
