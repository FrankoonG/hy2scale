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
</p>

---

## Quick Start

```bash
docker run -d --name hy2scale \
  --network host --cap-add NET_ADMIN \
  -v hy2scale-data:/data \
  frankoong/hy2scale:latest
```

Open `http://<host>:5565/scale/` — default login: `admin` / `admin`

## Features

- **Mesh Network** — Decentralized peer-to-peer topology over Hysteria 2 QUIC tunnels with nested discovery
- **Exit Routing** — Per-user traffic routing through any node or multi-hop chains
- **VPN Protocols** — L2TP/IPsec, IKEv2, WireGuard, SOCKS5, Shadowsocks — all with native OS client support
- **Routing Rules** — IP and domain-based traffic routing through specific exit nodes
- **TLS & CA** — Certificate management with CA signing for IKEv2
- **Web UI** — Single-page dashboard with real-time topology, traffic monitoring, and i18n (EN/KO)
- **iKuai Compatible** — Runs on iKuai routers in non-host Docker without `--privileged`
- **Hot Reload** — L2TP, IKEv2, WireGuard, proxies all reload without container restart

## Screenshots

![Nodes](.github/assets/nodes.png)
![Users](.github/assets/users.png)

## Documentation

Full documentation is available on the **[Wiki](https://github.com/FrankoonG/hy2scale/wiki)**.

- [Installation](https://github.com/FrankoonG/hy2scale/wiki/Installation)
- [Nodes & Mesh](https://github.com/FrankoonG/hy2scale/wiki/Nodes)
- [Proxies & VPN](https://github.com/FrankoonG/hy2scale/wiki/Proxies)
- [Users & Routing](https://github.com/FrankoonG/hy2scale/wiki/Users)
- [Routing Rules](https://github.com/FrankoonG/hy2scale/wiki/Rules)
- [TLS Certificates](https://github.com/FrankoonG/hy2scale/wiki/TLS)
- [iKuai Compatibility](https://github.com/FrankoonG/hy2scale/wiki/iKuai-Compatibility)
- [API Reference](https://github.com/FrankoonG/hy2scale/wiki/API-Reference)

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

## License

MIT
