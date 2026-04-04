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
  <a href="README.md">English</a> | <a href="README-zh.md">中文</a> | <b>한국어</b>
</p>

---

## 문서

**[Wiki](https://github.com/FrankoonG/hy2scale/wiki)** — 설치, 설정, API 레퍼런스 등 전체 문서.

## 빠른 시작

```bash
docker run -d --name hy2scale \
  --network host --cap-add NET_ADMIN \
  -v hy2scale-data:/data \
  frankoong/hy2scale:latest
```

`http://<호스트>:5565/scale/` 접속 — 기본 로그인: `admin` / `admin`

## 주요 기능

- **메시 네트워크** — Hysteria 2 QUIC 터널을 통한 분산형 P2P 토폴로지 및 중첩 디스커버리
- **출구 라우팅** — 사용자별 트래픽을 특정 노드 또는 멀티홉 체인을 통해 라우팅
- **VPN 프로토콜** — L2TP/IPsec, IKEv2, WireGuard, SOCKS5, Shadowsocks — OS 기본 클라이언트 지원
- **라우팅 규칙** — IP 및 도메인 기반 트래픽을 특정 출구 노드로 라우팅
- **TLS 및 CA** — IKEv2를 위한 CA 서명 기능 포함 인증서 관리
- **웹 UI** — 실시간 토폴로지, 트래픽 모니터링, 다국어 지원 (EN/KO)
- **경량 Linux 호환** — 라우터 펌웨어 등 고도로 경량화된 Linux 시스템에서 `--privileged` 없이 비호스트 Docker로 실행
- **핫 리로드** — L2TP, IKEv2, WireGuard, 프록시 모두 컨테이너 재시작 없이 리로드

## 스크린샷

![노드](.github/assets/nodes.png)
![사용자](.github/assets/users.png)

## 아키텍처

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

## 빌드

```bash
git clone https://github.com/FrankoonG/hy2scale.git
cd hy2scale
docker build -t hy2scale .
```

## 라이선스

MIT
