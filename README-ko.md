<p align="center">
  <img src=".github/assets/logo.svg" width="120" alt="HY2 SCALE">
</p>

<h1 align="center">HY2 SCALE</h1>

<p align="center">
  웹 관리 UI를 갖춘 Hysteria 2 메시 릴레이 네트워크.<br>
  어떤 노드를 통해서든 트래픽을 라우팅하고, VPN 서비스를 실행하며, 브라우저에서 모든 것을 관리하세요.
</p>

<p align="center">
  <a href="https://hub.docker.com/r/frankoong/hy2scale"><img src="https://img.shields.io/docker/v/frankoong/hy2scale?sort=semver&label=Docker%20Hub" alt="Docker Hub"></a>
  <a href="https://github.com/FrankoonG/hy2scale/wiki"><img src="https://img.shields.io/badge/docs-Wiki-blue" alt="Wiki"></a>
  <br>
  <a href="README.md">English</a> | <a href="README-zh.md">中文</a> | <b>한국어</b>
</p>

---

전체 설치, 설정, API 문서는 **[Wiki](https://github.com/FrankoonG/hy2scale/wiki)**를 참고하세요.

## 빠른 시작

### 원라이너 (네이티브 바이너리, systemd)

```bash
curl -fsSL https://raw.githubusercontent.com/FrankoonG/hy2scale/main/install.sh | sudo sh
```

CPU 아키텍처(`amd64` / `arm64` / `armv7` / `mips64le` /
`mipsle-softfloat`)를 자동 감지해 최신 릴리스의 해당 tar 파일을 받고,
`/usr/local/bin/hy2scale`에 바이너리를 설치한 뒤 systemd 서비스를
등록합니다. 다시 실행하면 그대로 업그레이드. 버전을 고정하려면
`HY2SCALE_VERSION=v1.4.1`, 제거하려면 `… | sudo sh -s -- --uninstall`.

### Docker

풀 모드 (호스트 네트워크 — 모든 기능 활성화):

```bash
docker run -d --name hy2scale \
  --network host --privileged \
  -v hy2scale-data:/data \
  --restart unless-stopped \
  frankoong/hy2scale:latest
```

브리지 모드 (VPN + 프록시, 규칙은 해당 사용자에 적용):

```bash
docker run -d --name hy2scale \
  -p 5565:5565/tcp -p 5565:5565/udp \
  -p 500:500/udp -p 4500:4500/udp -p 1194:1194/udp -p 51820:51820/udp \
  -v hy2scale-data:/data \
  --restart unless-stopped \
  frankoong/hy2scale:latest
```

VPN 서비스(IKEv2, L2TP/IPsec, OpenVPN, WireGuard)는 모두 사용자 공간에서 실행되며
capability나 디바이스가 필요 없고, 일반 포트 매핑만으로 충분합니다.
라우팅 규칙은 모든 모드에서 VPN 및 프록시 사용자에게 적용됩니다. 호스트 자체와
호스트를 경유하는 장치의 트래픽을 투명하게 캡처하려면 `--network host`와 `NET_ADMIN`이
필요하며, 서버 포트 호핑에는 `NET_ADMIN`이 필요합니다.

`https://<호스트>:5565/scale/` 접속(기본은 자체 서명 인증서) — 기본 로그인 `admin` / `admin`.

iKuai v4 사용자는 [Releases](https://github.com/FrankoonG/hy2scale/releases)에서 `.ipkg`를 다운로드해 앱 스토어로 설치하세요 — 자세한 내용은 [iKuai v4 가이드](https://github.com/FrankoonG/hy2scale/wiki/iKuai-v4-Installation)를 참고하세요.

## 주요 기능

- 노드를 메시로 연결하여 그중 어느 노드를 통해서든 트래픽을 라우팅하며, 여러 단계의 중첩 탐색을 지원합니다.
- 메시를 실시간으로 확인합니다: 개요 대시보드, 토폴로지 그래프, 리스트, 그리고 노드가 있는 국가별 세계 지도.
- VPN / 프록시 서비스 실행: Hysteria 2, SOCKS5, HTTP, Shadowsocks, Trojan, L2TP/IPsec, IKEv2, OpenVPN(UDP 및 TCP), WireGuard. VPN 서버는 권한 없이 완전히 사용자 공간에서 실행됩니다.
- 정책 세트로 사용자, 출발지, 목적지, 도메인(TLS SNI / HTTP Host), 국가 또는 ASN별로 트래픽을 라우팅하며 중첩 흐름을 지원합니다. 노드 자체 트래픽과 전달되는 트래픽을 투명하게 캡처할 수 있습니다.
- 사용자별 출구(단일 경로, 품질 기반 페일오버, 집계 또는 정책 세트), 주기적 초기화가 가능한 트래픽 제한, 업로드 / 다운로드 속도 제한, 만료일.
- 다중 주소 피어, NAT 뒤 노드 간 홀 펀칭, 주소별 출구 인터페이스 또는 소스 주소 지정. 인터페이스 페이지에서 서브 인터페이스와 인터페이스별 공인 IP를 관리합니다.
- 내장 인증 기관(CA)을 갖춘 TLS 인증서 관리.
- 모든 연결의 감사 기록, 시스템 로그, 네트워크 도구(Ping, 경로 추적, 처리량 측정, 터미널).
- 백업 / 복원, 서명 검증되는 앱 내 업그레이드, EN / 中文 / 한국어 UI.

## 스크린샷

싱가포르의 홈 노드(`sg-home`)를 중심으로 도쿄, 오사카, 서울, 홍콩, 로스앤젤레스, 프랑크푸르트, 암스테르담, 토론토, 런던에 피어가 있습니다:

![개요](.github/assets/overview.png)
![노드 — 토폴로지](.github/assets/nodes-graph.png)
![노드 — 지도](.github/assets/nodes-map.png)
![노드 — 리스트](.github/assets/nodes-table.png)

## 빌드

```bash
git clone https://github.com/FrankoonG/hy2scale.git
cd hy2scale
docker build -t hy2scale .
```

## 안내

HY2 SCALE은 Hysteria 2 프로토콜을 사용합니다. Hysteria 프로젝트 및 Tailscale과는 관련이 없습니다.

## Star History

<a href="https://www.star-history.com/?repos=FrankoonG%2Fhy2scale&type=date&legend=top-left">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/chart?repos=FrankoonG/hy2scale&type=date&theme=dark&legend=top-left" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/chart?repos=FrankoonG/hy2scale&type=date&legend=top-left" />
   <img alt="Star History Chart" src="https://api.star-history.com/chart?repos=FrankoonG/hy2scale&type=date&legend=top-left" />
 </picture>
</a>
