<p align="center">
  <img src=".github/assets/logo.svg" width="120" alt="HY2 SCALE">
</p>

<h1 align="center">HY2 SCALE</h1>

<p align="center">
  Hysteria 2 mesh relay network with a web management UI.<br>
  Route traffic through any node, run VPN services, manage everything from a browser.
</p>

<p align="center">
  <a href="https://hub.docker.com/r/frankoong/hy2scale"><img src="https://img.shields.io/docker/v/frankoong/hy2scale?sort=semver&label=Docker%20Hub" alt="Docker Hub"></a>
  <a href="https://github.com/FrankoonG/hy2scale/wiki"><img src="https://img.shields.io/badge/docs-Wiki-blue" alt="Wiki"></a>
  <br>
  <b>English</b> | <a href="README-zh.md">中文</a> | <a href="README-ko.md">한국어</a>
</p>

---

See the **[Wiki](https://github.com/FrankoonG/hy2scale/wiki)** for full installation, configuration, and API documentation.

## Quick Start

### One-liner (native binary, systemd)

```bash
curl -fsSL https://raw.githubusercontent.com/FrankoonG/hy2scale/main/install.sh | sudo sh
```

Auto-detects the CPU architecture (`amd64`, `arm64`, `armv7`, `mips64le`,
`mipsle-softfloat`), downloads the matching tarball from the latest
release, installs the binary to `/usr/local/bin/hy2scale`, and registers
a systemd service. Re-running upgrades in place. Pin a version with
`HY2SCALE_VERSION=v1.3.2`; uninstall with `… | sudo sh -s -- --uninstall`.

### Docker

Full mode (host network — everything enabled):

```bash
docker run -d --name hy2scale \
  --network host --privileged \
  -v hy2scale-data:/data \
  --restart unless-stopped \
  frankoong/hy2scale:latest
```

Bridge mode (VPN + proxies, no routing rules):

```bash
docker run -d --name hy2scale \
  --cap-add NET_ADMIN --cap-add NET_RAW \
  -p 5565:5565/tcp -p 5565:5565/udp \
  -p 500:500/udp -p 4500:4500/udp -p 1701:1701/udp -p 51820:51820/udp \
  -v hy2scale-data:/data \
  --restart unless-stopped \
  frankoong/hy2scale:latest
```

Open `http://<host>:5565/scale/` — default login `admin` / `admin`.

For iKuai v4, download the `.ipkg` from [Releases](https://github.com/FrankoonG/hy2scale/releases) and install via the app store — see the [iKuai v4 guide](https://github.com/FrankoonG/hy2scale/wiki/iKuai-v4-Installation).

## Features

- Connect nodes into a mesh and route traffic through any of them.
- See live topology and latency in an interactive graph or a table view.
- Run VPN / proxy services: Hysteria 2, SOCKS5, HTTP, Shadowsocks, L2TP, IKEv2, WireGuard.
- Per-user exit routing, traffic limits, expiry, bulk management.
- IP and domain routing rules steer specific traffic through specific exits.
- TLS certificate management with built-in CA signing.
- Multi-IP peer aggregation (failover or bandwidth bonding).
- Backup / restore, in-place binary upgrade (manual or one-click online check), UI in EN / 中文 / 한국어.
- Select-to-edit UX: pick a row in any list and a top-right **Edit** button drives editing; clicking blank area outside the list clears the single selection; hysteria2:// share URLs can be imported by long-pressing **+ Add Node**.

## Screenshots

Topology graph and list view, showing a Singapore home node peering with remote nodes across several regions:

![Nodes — graph](.github/assets/nodes-graph.png)
![Nodes — list](.github/assets/nodes-table.png)

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
