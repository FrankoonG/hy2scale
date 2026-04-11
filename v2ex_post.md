写了一个开源Hysteria2 Web面板，类TailScale，反向上网；iKuai 爱快 最佳搭档————Hy2 Scale

GitHub：https://github.com/FrankoonG/hy2scale

Docker Hub：https://hub.docker.com/r/frankoong/hy2scale

![nodes](https://raw.githubusercontent.com/FrankoonG/hy2scale/refs/heads/main/.github/assets/nodes.png)

## Hy2 Scale

一个基于 Hysteria 2 的去中心化组网工具。思路类似 Tailscale，但每个节点同时充当 Hy2 客户端、中转站和代理服务端，节点之间通过 QUIC 隧道互联，流量可沿任意路径出口。

部署一个 Docker 容器即一个节点，通过 Web 界面管理所有节点、用户和代理服务。

## 核心机制

- **去中心化**：节点之间可自由嵌套发现彼此的 Peer，模糊客户端与服务端之间的关系
- **灵活出口**：每个用户可指定不同的出口节点，支持多跳链路（如 `新加坡/香港/国内`）
- **出口策略**：自定义节点路径出口，可多出口并支持 Quality（自动故障切换）和 Aggregate（多路负载均衡）模式

## 适用场景

**借线出海 / 回国**：有公网 IP 的服务器借没有公网 IP 的住宅宽带线路上网。例如上海电信家宽（有公网 IP）借广东移动家宽（无公网 IP）的线路上网；此外，海外 VPS 也可借国内家宽节点回国。

**多跳优选路径**：多台海外 VPS 以最低延迟访问国内。例如新加坡直连国内电信通常绕美国、延迟 300ms+，若有一台与国内直连优化的香港 VPS，可使用嵌套路径 `新加坡/香港/国内电信` 获得更优延迟。

**iKuai 分流**：配合 [ikuai-bypass](https://github.com/joyanhui/ikuai-bypass) 按国家或特定应用分流。iKuai 企业版可使用更高性能的 IKEv2 接入，免费版可通过 L2TP 接入。

## Docker 部署

```bash
docker run -d --name hy2scale \
  --network host --privileged \
  -v hy2scale-data:/data \
  --restart unless-stopped \
  frankoong/hy2scale:latest
```

部署后打开 `http://localhost:5565/scale`，默认用户名和密码均为 `admin`。

> 也支持非 host 模式运行（使用端口映射），L2TP / IKEv2 / WireGuard 均可用，但 Rules 引擎需要 host 模式。

## 功能一览

- **节点管理**：节点可同时作为服务端（接受连接）和客户端（主动连接其它节点），支持多 IP 聚合
- **多用户**：可设置流量配额、指定出口节点，支持配置多条出口路径及自动切换
- **内置代理**：Shadowsocks、SOCKS5、L2TP/IPsec、IKEv2、WireGuard，以及原生 Hysteria 2 直连（供非 hy2scale 客户端使用）
- **Rules 路由**：按 IP 段或域名将流量转发至不同出口节点，可启用 TUN 模式保留原始 IP 包、避免连接拆分
- **TLS 证书管理**：内置证书管理，支持 CA 签发（IKEv2 所需）
- **热重载**：代理服务的启停及用户增删改均无需重启容器

## 关于 iKuai

建议使用 iKuai v4，因为 iKuai v3 的 Docker 可能存在局限性，无法启用完整功能。iKuai v4（企业版或免费版）可通过 **高级应用 → 应用市场 → 本地安装** 上传 `.ipkg` 安装包快速部署。

安装指南及 IKEv2 配置详见：https://github.com/FrankoonG/hy2scale/wiki/iKuai-v4-Installation

![ik1](https://raw.githubusercontent.com/wiki/FrankoonG/hy2scale/images/ikuai-v4-app-details-zh.png)

![ik2](https://raw.githubusercontent.com/wiki/FrankoonG/hy2scale/images/ikuai-v4-ikev2-connected-zh.png)

## 文档

完整文档：https://github.com/FrankoonG/hy2scale/wiki
