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

完整安装、配置和 API 文档请参阅 **[Wiki](https://github.com/FrankoonG/hy2scale/wiki)**。

## 快速开始

Full 模式（Host 网络 — 全部功能启用）：

```bash
docker run -d --name hy2scale \
  --network host --privileged \
  -v hy2scale-data:/data \
  --restart unless-stopped \
  frankoong/hy2scale:latest
```

Bridge 模式（VPN + 代理，不含路由规则）：

```bash
docker run -d --name hy2scale \
  --cap-add NET_ADMIN --cap-add NET_RAW \
  -p 5565:5565/tcp -p 5565:5565/udp \
  -p 500:500/udp -p 4500:4500/udp -p 1701:1701/udp -p 51820:51820/udp \
  -v hy2scale-data:/data \
  --restart unless-stopped \
  frankoong/hy2scale:latest
```

打开 `http://<主机>:5565/scale/` — 默认登录 `admin` / `admin`。

iKuai v4 用户请从 [Releases](https://github.com/FrankoonG/hy2scale/releases) 下载 `.ipkg` 并通过应用商店安装 — 详见 [iKuai v4 指南](https://github.com/FrankoonG/hy2scale/wiki/iKuai-v4-Installation)。

## 功能特性

- 将节点连接成网状网络，通过其中任意一个节点转发流量。
- 在交互式图形视图或表格视图中查看实时拓扑和延迟。
- 运行 VPN / 代理服务：Hysteria 2、SOCKS5、HTTP、Shadowsocks、L2TP、IKEv2、WireGuard。
- 按用户出口路由、流量限额、到期时间和批量管理。
- IP 与域名路由规则将特定流量引向特定出口。
- TLS 证书管理，内置 CA 签发。
- 多 IP 对等节点聚合（故障转移或带宽叠加）。
- 备份 / 恢复、容器内二进制升级，UI 支持 EN / 中文 / 한국어。

## 截图

拓扑图与列表视图，展示新加坡本地节点与多地远程节点的对等关系：

![节点 — 图形视图](.github/assets/nodes-graph.png)
![节点 — 列表视图](.github/assets/nodes-table.png)

## 构建

```bash
git clone https://github.com/FrankoonG/hy2scale.git
cd hy2scale
docker build -t hy2scale .
```

## Star 历史

<a href="https://www.star-history.com/?repos=FrankoonG%2Fhy2scale&type=date&legend=top-left">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/chart?repos=FrankoonG/hy2scale&type=date&theme=dark&legend=top-left" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/chart?repos=FrankoonG/hy2scale&type=date&legend=top-left" />
   <img alt="Star History Chart" src="https://api.star-history.com/chart?repos=FrankoonG/hy2scale&type=date&legend=top-left" />
 </picture>
</a>
