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
`HY2SCALE_VERSION=v1.3.3`, 제거하려면 `… | sudo sh -s -- --uninstall`.

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

`http://<호스트>:5565/scale/` 접속 — 기본 로그인 `admin` / `admin`.

iKuai v4 사용자는 [Releases](https://github.com/FrankoonG/hy2scale/releases)에서 `.ipkg`를 다운로드해 앱 스토어로 설치하세요 — 자세한 내용은 [iKuai v4 가이드](https://github.com/FrankoonG/hy2scale/wiki/iKuai-v4-Installation)를 참고하세요.

## 주요 기능

- 노드를 메시로 연결하여 그중 어느 노드를 통해서든 트래픽을 라우팅합니다.
- 인터랙티브 그래프 또는 테이블 뷰에서 실시간 토폴로지와 지연 시간을 확인합니다.
- VPN / 프록시 서비스 실행: Hysteria 2, SOCKS5, HTTP, Shadowsocks, L2TP, IKEv2, OpenVPN, WireGuard — VPN은 권한 없이 완전히 사용자 공간에서 실행됩니다.
- 홀 펀칭: NAT 뒤의 노드끼리, 양쪽 모두 연결된 노드의 도움으로 직접 연결을 설정합니다.
- 다중 인터페이스 노드는 주소별로 출구 인터페이스나 소스 주소를 지정할 수 있습니다.
- 사용자별 출구 라우팅, 트래픽 제한, 만료일, 일괄 관리.
- IP 및 도메인 라우팅 규칙으로 특정 트래픽을 특정 출구로 보냅니다.
- 내장 CA 서명을 지원하는 TLS 인증서 관리.
- 다중 IP 피어 집계 (페일오버 또는 대역폭 결합).
- 백업 / 복원, 컨테이너 내 바이너리 업그레이드(수동 업로드 또는 한 번 클릭으로 온라인 검사), EN / 中文 / 한국어 UI.
- 선택 후 편집: 어떤 리스트에서든 행을 단일 선택하면 카드 우상단에 녹색 **Edit** 버튼이 편집 진입점으로 표시됩니다. 외부 빈 영역을 클릭하면 단일 선택이 해제되고, **+ Add Node**를 길게 누르면 hysteria2:// URL 가져오기가 열립니다.

## 스크린샷

싱가포르 홈 노드가 여러 지역의 원격 노드와 피어링하는 토폴로지 그래프와 리스트 뷰:

![노드 — 그래프 뷰](.github/assets/nodes-graph.png)
![노드 — 리스트 뷰](.github/assets/nodes-table.png)

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
