# HY2 SCALE

Hysteria 2 mesh relay network with web management UI.
Route traffic through any node, run VPN services, manage everything from a browser.

[![GitHub](https://img.shields.io/badge/GitHub-FrankoonG%2Fhy2scale-blue)](https://github.com/FrankoonG/hy2scale)
[![Wiki](https://img.shields.io/badge/docs-Wiki-blue)](https://github.com/FrankoonG/hy2scale/wiki)

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

Open `http://<host>:5565/scale/` — default login: `admin` / `admin`

## Features

- **Mesh Network** — Decentralized peer-to-peer topology over Hysteria 2 QUIC tunnels with nested discovery
- **Exit Routing** — Per-user traffic routing through any node or multi-hop chains, with Quality (failover) and Aggregate (load balance) modes
- **VPN Protocols** — L2TP/IPsec, IKEv2, WireGuard, SOCKS5, Shadowsocks, native Hysteria 2 — all with native OS client support
- **Routing Rules** — IP and domain-based traffic routing through specific exit nodes
- **TUN Mode** — Raw IP packet forwarding through relay, preserving end-to-end TCP/UDP sessions
- **TLS & CA** — Certificate management with CA signing for IKEv2
- **Web UI** — Single-page dashboard with real-time topology, traffic monitoring, and i18n (EN/ZH/KO)
- **Minimal Linux Compatible** — Runs on stripped-down Linux systems (e.g. iKuai router firmware) with automatic compat mode
- **Hot Reload** — L2TP, IKEv2, WireGuard, proxies, users all reload without container restart

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

## Documentation

Full documentation: https://github.com/FrankoonG/hy2scale/wiki

## iKuai v4

Install via `.ipkg` package on iKuai Application Store. See the [iKuai v4 guide](https://github.com/FrankoonG/hy2scale/wiki/iKuai-v4-Installation).

## License

MIT
