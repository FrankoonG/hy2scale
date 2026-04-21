<p align="center">
  <img src=".github/assets/logo.svg" width="120" alt="HY2 SCALE">
</p>

<h1 align="center">HY2 SCALE</h1>

<p align="center">
  Hysteria 2 mesh relay network with web management UI.<br>
  Route traffic through any node, run VPN services, manage everything from a browser.
</p>

<p align="center">
  <a href="https://hub.docker.com/r/frankoong/hy2scale"><img src="https://img.shields.io/docker/v/frankoong/hy2scale?sort=semver&label=Docker%20Hub" alt="Docker Hub"></a>
  <a href="https://github.com/FrankoonG/hy2scale/wiki"><img src="https://img.shields.io/badge/docs-Wiki-blue" alt="Wiki"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-GPL--3.0--or--later-blue" alt="License"></a>
  <br>
  <b>English</b> | <a href="README-zh.md">中文</a> | <a href="README-ko.md">한국어</a>
</p>

---

## Documentation

**[Wiki](https://github.com/FrankoonG/hy2scale/wiki)** — Installation, configuration, per-protocol guides, API reference.

## Quick Start

### Full mode (host network — all features including routing rules)

```bash
docker run -d --name hy2scale \
  --network host --privileged \
  -v hy2scale-data:/data \
  --restart unless-stopped \
  frankoong/hy2scale:latest
```

### Bridge mode (L2TP / IKEv2 / WireGuard, no routing rules)

```bash
docker run -d --name hy2scale \
  --cap-add NET_ADMIN --cap-add NET_RAW \
  -p 5565:5565/tcp -p 5565:5565/udp \
  -p 500:500/udp -p 4500:4500/udp -p 1701:1701/udp -p 51820:51820/udp \
  -v hy2scale-data:/data \
  --restart unless-stopped \
  frankoong/hy2scale:latest
```

### iKuai v4

Download the `.ipkg` from [Releases](https://github.com/FrankoonG/hy2scale/releases) and install via **Advanced Apps → App Market → Local Install**. See the [iKuai v4 guide](https://github.com/FrankoonG/hy2scale/wiki/iKuai-v4-Installation) for details.

---

Open `http://<host>:5565/scale/` — default login: `admin` / `admin`.

## Features

- **Mesh Network** — decentralised peer-to-peer topology over Hysteria 2 QUIC tunnels; each node is both server and client.
- **Nested Discovery** — reach a peer's peers through the existing tunnel, up to arbitrary depth; iron-rule-based path filtering prevents cycles and unauthorised exits.
- **Graph Topology View** — pan / zoom / pinch-zoom interactive SVG graph with live latency and throughput on every edge; layout is server-persisted and synchronised across browser sessions over SSE.
- **Exit Routing** — per-user / per-rule traffic steering through any node or explicit multi-hop chain (`jp`, `us/us-east/us-east-va`, etc.).
- **VPN Protocols** — Hysteria 2, SOCKS5, HTTP, Shadowsocks, L2TP/IPsec, IKEv2/IPsec, WireGuard. All authenticate against the same user database and honour per-user exit routing.
- **Routing Rules** — IP- and domain-based steering through exit nodes via iptables DNAT + transparent proxy (host mode) or TUN mode (bridge / router firmware).
- **TLS & CA** — generate, import, or CA-sign certificates from the UI; auto-mint IKEv2 server certs from an uploaded CA.
- **Bond Aggregation** — multi-address peers: aggregate bandwidth of several links to the same remote, or run in failover (quality) mode.
- **Minimal Linux / Router Compatible** — runs on stripped-down Linux systems (iKuai v4 custom kernel, stock OpenWrt, etc.) in non-host Docker without `--privileged`; automatic compat mode swaps broken kernel paths for xfrm-bridge and TUN capture.
- **[iKuai v4 Support](https://github.com/FrankoonG/hy2scale/wiki/iKuai-v4-Installation)** — one-click `.ipkg` install with compat mode auto-enabled.
- **Web UI** — React SPA in English / 中文 / 한국어, dark-reader-extension friendly, responsive down to phone width, auto-reloads when a new server build is deployed.
- **Hot Reload** — L2TP, IKEv2, WireGuard, Shadowsocks, SOCKS5, HTTP, proxies all reload without restarting the container.
- **In-Container Upgrade** — upload a new binary tarball from the web UI to upgrade in place, no container rebuild required.

## Screenshots

Network topology with multi-level nesting (Singapore-based home node peering with eight countries, some exposing sub-peers):

![Nodes — graph view](.github/assets/nodes-graph.png)

Selecting a destination reveals the full relay path (`sg-home → us → us-east → us-east-va`, 165 ms accumulated) and exposes per-hop actions:

![Nodes — selected path](.github/assets/nodes-graph-path.png)

Users with per-user exit routing, shared credentials across every VPN protocol:

![Users](.github/assets/users.png)

Seven proxy protocols in one place:

![Proxies — WireGuard](.github/assets/proxies-wireguard.png)

## Architecture

```
                               sg-home (self)
     /    /      |      |      |      |      |      |      \      \
    jp  us   kr  hk   tw   in   de   uk    au    br
    |   / \    \                     |            |     |
   jp-r1 us-east us-west           de-r1        au-r1   br-r1
          |                                      |
      us-east-va                              au-r1-a
```

Every node runs a Hysteria 2 QUIC server and a relay plane. A node can act as a pure relay, a pure exit, or both. Devices behind NAT remain reachable because their parent peers hold the QUIC tunnel.

## Building

```bash
git clone https://github.com/FrankoonG/hy2scale.git
cd hy2scale
docker build -t hy2scale .
```

Frontend only:

```bash
cd web/ui-framework && npm ci && npm run build
cd ../app        && npm ci && npm run build
```

## License

GPL-3.0-or-later — see [LICENSE](LICENSE). The Docker image bundles strongSwan (GPLv2+), iptables (GPLv2+) and xl2tpd (GPLv2+); redistribution must carry the licence text and a source-code offer.

## Star History

<a href="https://www.star-history.com/?repos=FrankoonG%2Fhy2scale&type=date&legend=top-left">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/chart?repos=FrankoonG/hy2scale&type=date&theme=dark&legend=top-left" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/chart?repos=FrankoonG/hy2scale&type=date&legend=top-left" />
   <img alt="Star History Chart" src="https://api.star-history.com/chart?repos=FrankoonG/hy2scale&type=date&legend=top-left" />
 </picture>
</a>
