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

### 一键安装（原生二进制 + systemd）

```bash
curl -fsSL https://raw.githubusercontent.com/FrankoonG/hy2scale/main/install.sh | sudo sh
```

自动识别 CPU 架构（`amd64` / `arm64` / `armv7` / `mips64le` /
`mipsle-softfloat`），从最新 release 下载匹配的 tar 包，把二进制装到
`/usr/local/bin/hy2scale` 并注册 systemd 服务。重复运行即原地升级。
固定版本用 `HY2SCALE_VERSION=v1.3.3`；卸载用 `… | sudo sh -s -- --uninstall`。

### Docker

Full 模式（Host 网络 — 全部功能启用）：

```bash
docker run -d --name hy2scale \
  --network host --privileged \
  -v hy2scale-data:/data \
  --restart unless-stopped \
  frankoong/hy2scale:latest
```

Bridge 模式（VPN + 代理，规则作用于其用户）：

```bash
docker run -d --name hy2scale \
  -p 5565:5565/tcp -p 5565:5565/udp \
  -p 500:500/udp -p 4500:4500/udp -p 1194:1194/udp -p 51820:51820/udp \
  -v hy2scale-data:/data \
  --restart unless-stopped \
  frankoong/hy2scale:latest
```

VPN 服务（IKEv2、L2TP/IPsec、OpenVPN、WireGuard）和代理均在用户态运行，不需要任何
capability 或设备，普通端口映射即可。路由规则在任何模式下都作用于它们的用户。
透明接管宿主机自身以及经其路由的主机的流量需要 `--network host` 和 `NET_ADMIN`；
服务端端口跳跃需要 `NET_ADMIN`。

打开 `http://<主机>:5565/scale/` — 默认登录 `admin` / `admin`。

iKuai v4 用户请从 [Releases](https://github.com/FrankoonG/hy2scale/releases) 下载 `.ipkg` 并通过应用商店安装 — 详见 [iKuai v4 指南](https://github.com/FrankoonG/hy2scale/wiki/iKuai-v4-Installation)。

## 功能特性

- 将节点连接成网状网络，通过其中任意一个节点转发流量。
- 在交互式图形视图或表格视图中查看实时拓扑和延迟。
- 运行 VPN / 代理服务：Hysteria 2、SOCKS5、HTTP、Shadowsocks、L2TP、IKEv2、OpenVPN、WireGuard，其中 VPN 完全运行在用户态，无需特权。
- 打洞：位于 NAT 之后的节点之间，经双方都能到达的节点协助建立直连。
- 多网卡节点可为每个地址指定出口网卡或源地址。
- 按用户出口路由、流量限额、到期时间和批量管理。
- IP 与域名路由规则将特定流量引向特定出口。
- TLS 证书管理，内置 CA 签发。
- 多 IP 对等节点聚合（故障转移或带宽叠加）。
- 备份 / 恢复、容器内二进制升级（手动上传或一键在线检查），UI 支持 EN / 中文 / 한국어。
- 单选即编辑：在任一列表中点选一行，卡片右上角即出现绿色 **Edit** 按钮作为编辑入口；点击列表外空白取消单选；长按 **+ Add Node** 可粘贴 hysteria2:// 链接导入。

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
