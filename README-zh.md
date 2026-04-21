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
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-GPL--3.0--or--later-blue" alt="License"></a>
  <br>
  <a href="README.md">English</a> | <b>中文</b> | <a href="README-ko.md">한국어</a>
</p>

---

## 文档

**[Wiki](https://github.com/FrankoonG/hy2scale/wiki)** — 安装、配置、各协议指南及 API 参考。

## 快速开始

### Full 模式（Host 网络 — 包含路由规则在内的全部功能）

```bash
docker run -d --name hy2scale \
  --network host --privileged \
  -v hy2scale-data:/data \
  --restart unless-stopped \
  frankoong/hy2scale:latest
```

### Bridge 模式（L2TP / IKEv2 / WireGuard，不含路由规则）

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

从 [Releases](https://github.com/FrankoonG/hy2scale/releases) 下载 `.ipkg` 安装包，通过 **应用管理 → 应用商店 → 本地安装** 进行安装。详情请参阅 [iKuai v4 安装指南](https://github.com/FrankoonG/hy2scale/wiki/iKuai-v4-Installation)。

---

打开 `http://<主机IP>:5565/scale/` — 默认登录：`admin` / `admin`。

## 功能特性

- **网状网络（Mesh）** — 基于 Hysteria 2 QUIC 隧道的去中心化对等拓扑；每个节点同时是服务端和客户端。
- **嵌套发现** — 通过已有隧道访问对等节点的对等节点，支持任意深度；基于铁律（iron rules）的路径过滤机制防止环路和未授权出口。
- **图形化拓扑视图** — 可平移、缩放、触控缩放的交互式 SVG 图；每条连线实时显示延迟和吞吐量；布局由服务端持久化，并通过 SSE 在所有浏览器会话间同步。
- **出口路由** — 按用户 / 按规则将流量引导至任意节点或显式多跳链路（`jp`、`us/us-east/us-east-va` 等）。
- **VPN 协议** — Hysteria 2、SOCKS5、HTTP、Shadowsocks、L2TP/IPsec、IKEv2/IPsec、WireGuard。所有协议共享同一用户数据库，并遵循按用户出口路由。
- **路由规则** — 通过 iptables DNAT + 透明代理（Host 模式）或 TUN 模式（Bridge / 路由器固件）实现基于 IP 和域名的分流。
- **TLS 与 CA** — 在 UI 中生成、导入或 CA 签发证书；从已上传 CA 自动签发 IKEv2 服务器证书。
- **Bond 聚合** — 多地址对等节点：可聚合多个链路的带宽，也可运行在故障转移（quality）模式。
- **精简 Linux / 路由器兼容** — 可在裁剪过的 Linux 系统（iKuai v4 定制内核、原版 OpenWrt 等）上以非 Host Docker 运行，无需 `--privileged`；自动兼容模式将失效的内核路径替换为 xfrm-bridge 与 TUN 捕获。
- **[iKuai v4 支持](https://github.com/FrankoonG/hy2scale/wiki/iKuai-v4-Installation)** — 一键 `.ipkg` 安装，自动启用兼容模式。
- **Web UI** — React 单页应用，支持 English / 中文 / 한국어；兼容 Dark Reader 扩展；响应式设计下探至手机宽度；服务端部署新版本时自动重载。
- **热重载** — L2TP、IKEv2、WireGuard、Shadowsocks、SOCKS5、HTTP、代理均无需重启容器即可热重载。
- **容器内升级** — 从 Web UI 上传新的二进制压缩包即可原地升级，无需重建容器。

## 截图

多层级嵌套的网络拓扑（新加坡本地节点与八个国家的节点建立对等关系，部分节点公开了子对等节点）：

![节点 — 图形视图](.github/assets/nodes-graph.png)

选中目标节点后显示完整中继路径（`sg-home → us → us-east → us-east-va`，累计 165 ms）并提供逐跳操作：

![节点 — 选中路径](.github/assets/nodes-graph-path.png)

支持按用户出口路由的用户，所有 VPN 协议共享同一套凭据：

![用户](.github/assets/users.png)

七种代理协议集中管理：

![代理 — WireGuard](.github/assets/proxies-wireguard.png)

## 架构

```
                               sg-home (self)
     /    /      |      |      |      |      |      |      \      \
    jp  us   kr  hk   tw   in   de   uk    au    br
    |   / \    \                     |            |     |
   jp-r1 us-east us-west           de-r1        au-r1   br-r1
          |                                      |
      us-east-va                              au-r1-a
```

每个节点运行一个 Hysteria 2 QUIC 服务器和中继平面。节点可以作为纯中继、纯出口或同时兼任。NAT 后的设备仍可被访问，因为其上游对等节点持有 QUIC 隧道。

## 构建

```bash
git clone https://github.com/FrankoonG/hy2scale.git
cd hy2scale
docker build -t hy2scale .
```

仅构建前端：

```bash
cd web/ui-framework && npm ci && npm run build
cd ../app        && npm ci && npm run build
```

## 许可证

GPL-3.0-or-later — 详见 [LICENSE](LICENSE)。Docker 镜像捆绑了 strongSwan（GPLv2+）、iptables（GPLv2+）和 xl2tpd（GPLv2+）；再分发时必须附带许可证文本和源代码获取方式。

## Star 历史

<a href="https://www.star-history.com/?repos=FrankoonG%2Fhy2scale&type=date&legend=top-left">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/chart?repos=FrankoonG/hy2scale&type=date&theme=dark&legend=top-left" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/chart?repos=FrankoonG/hy2scale&type=date&legend=top-left" />
   <img alt="Star History Chart" src="https://api.star-history.com/chart?repos=FrankoonG/hy2scale&type=date&legend=top-left" />
 </picture>
</a>
