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
  <br>
  <b>English</b> | <a href="README-zh.md">中文</a> | <a href="README-ko.md">한국어</a>
</p>

---

## Documentation

**[Wiki](https://github.com/FrankoonG/hy2scale/wiki)** — Full documentation including installation, configuration, API reference, and more.

## Quick Start

### Full mode (host network — all features including Rules engine)

```bash
docker run -d --name hy2scale \
  --network host --privileged \
  -v hy2scale-data:/data \
  --restart unless-stopped \
  frankoong/hy2scale:latest
```

### Bridge mode (L2TP/IKEv2/WireGuard, no Rules engine)

```bash
docker run -d --name hy2scale \
  --cap-add NET_ADMIN --cap-add NET_RAW \
  -p 5565:5565/tcp -p 5565:5565/udp \
  -v hy2scale-data:/data \
  --restart unless-stopped \
  frankoong/hy2scale:latest
```

### iKuai v4

Download the `.ipkg` from [Releases](https://github.com/FrankoonG/hy2scale/releases) and install via **Applications → Application Store → Local installation**. See the [iKuai v4 guide](https://github.com/FrankoonG/hy2scale/wiki/iKuai-v4-Installation) for details.

---

Open `http://<host>:5565/scale/` — default login: `admin` / `admin`

## Features

- **Mesh Network** — Decentralized peer-to-peer topology over Hysteria 2 QUIC tunnels with nested discovery
- **Exit Routing** — Per-user traffic routing through any node or multi-hop chains
- **VPN Protocols** — L2TP/IPsec, IKEv2, WireGuard, SOCKS5, Shadowsocks — all with native OS client support
- **Routing Rules** — IP and domain-based traffic routing through specific exit nodes
- **TLS & CA** — Certificate management with CA signing for IKEv2
- **Web UI** — Single-page dashboard with real-time topology, traffic monitoring, and i18n (EN/KO)
- **TUN Mode** — Raw IP packet forwarding through relay, preserving end-to-end TCP/UDP sessions for protocols like Moonlight
- **Minimal Linux Compatible** — Runs on stripped-down Linux systems (e.g., router firmware) in non-host Docker without `--privileged`
- **[iKuai v4 Support](https://github.com/FrankoonG/hy2scale/wiki/iKuai-v4-Installation)** — Install via `.ipkg` package on iKuai Application Store with automatic compat mode
- **Hot Reload** — L2TP, IKEv2, WireGuard, proxies all reload without container restart

## Screenshots

![Nodes](.github/assets/nodes.png)
![Users](.github/assets/users.png)

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

## Building

```bash
git clone https://github.com/FrankoonG/hy2scale.git
cd hy2scale
docker build -t hy2scale .
```

## Star History

<a href="https://www.star-history.com/?repos=FrankoonG%2Fhy2scale&type=date&legend=top-left">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/chart?repos=FrankoonG/hy2scale&type=date&theme=dark&legend=top-left" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/chart?repos=FrankoonG/hy2scale&type=date&legend=top-left" />
   <img alt="Star History Chart" src="https://api.star-history.com/chart?repos=FrankoonG/hy2scale&type=date&legend=top-left" />
 </picture>
</a>