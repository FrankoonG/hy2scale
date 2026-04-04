<p align="center">
  <img src=".github/assets/logo.svg" width="120" alt="HY2 SCALE">
</p>

<h1 align="center">HY2 SCALE</h1>

<p align="center">
  基于 Hysteria 2 的 Mesh 中继网络，内置 Web 管理界面。<br>
  通过任意节点转发流量、运行 VPN 服务，一切均可在浏览器中管理。
</p>

<p align="center">
  <a href="https://hub.docker.com/r/frankoong/hy2scale"><img src="https://img.shields.io/docker/v/frankoong/hy2scale?sort=semver&label=Docker%20Hub" alt="Docker Hub"></a>
  <a href="https://github.com/FrankoonG/hy2scale/wiki"><img src="https://img.shields.io/badge/docs-Wiki-blue" alt="Wiki"></a>
  <br>
  <a href="README.md">English</a> | <b>中文</b> | <a href="README-ko.md">한국어</a>
</p>

---

## 文档

**[Wiki](https://github.com/FrankoonG/hy2scale/wiki)** — 完整文档，包括安装、配置、API 参考等。

## 快速开始

### Full 模式（Host 网络 — 全部功能，包括 Rules 引擎）

```bash
docker run -d --name hy2scale \
  --network host --privileged \
  -v hy2scale-data:/data \
  --restart unless-stopped \
  frankoong/hy2scale:latest
```

### Bridge 模式（L2TP/IKEv2/WireGuard，无 Rules 引擎）

```bash
docker run -d --name hy2scale \
  --cap-add NET_ADMIN --cap-add NET_RAW \
  -p 5565:5565/tcp -p 5565:5565/udp \
  -v hy2scale-data:/data \
  --restart unless-stopped \
  frankoong/hy2scale:latest
```

### iKuai v4

从 [Releases](https://github.com/FrankoonG/hy2scale/releases) 下载 `.ipkg` 安装包，通过 **应用管理 → 应用商店 → 本地安装** 进行安装。详情请参阅 [iKuai v4 安装指南](https://github.com/FrankoonG/hy2scale/wiki/iKuai-v4-Installation)。

---

打开 `http://<主机IP>:5565/scale/` — 默认登录：`admin` / `admin`

## 功能特性

- **Mesh 网络** — 基于 Hysteria 2 QUIC 隧道的去中心化 P2P 拓扑，支持嵌套节点发现
- **出口路由** — 按用户将流量路由到任意节点或多跳链路
- **VPN 协议** — L2TP/IPsec、IKEv2、WireGuard、SOCKS5、Shadowsocks — 均支持系统原生客户端
- **路由规则** — 基于 IP 和域名的流量路由，通过指定出口节点转发
- **TLS 与 CA** — 证书管理，支持 CA 签名（用于 IKEv2）
- **Web UI** — 单页管理面板，实时拓扑可视化、流量监控、多语言支持 (EN/KO)
- **TUN 模式** — 通过中继进行原始 IP 包转发，保持端到端 TCP/UDP 会话（适用于 Moonlight 等协议）
- **精简 Linux 兼容** — 可在精简 Linux 系统（如路由器固件）上以非 Host Docker 模式运行，无需 `--privileged`
- **[iKuai v4 支持](https://github.com/FrankoonG/hy2scale/wiki/iKuai-v4-Installation)** — 通过 `.ipkg` 包在 iKuai 应用商店安装，自动启用 Compat 模式
- **热重载** — L2TP、IKEv2、WireGuard、代理均可在不重启容器的情况下重新加载

## 截图

![节点](.github/assets/nodes.png)
![用户](.github/assets/users.png)

## 架构

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

## 构建

```bash
git clone https://github.com/FrankoonG/hy2scale.git
cd hy2scale
docker build -t hy2scale .
```

## 许可证

MIT
