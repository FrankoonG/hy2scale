# Needs BuildKit (the default builder): stages are picked by build args.
#
# The binary is built here from source, or, for release builds
# (--build-arg BIN=release), it is the signed release binary for this
# platform taken from release/ in the build context, with its signature
# (see .github/workflows/release.yml), so the image runs exactly the bytes
# that were signed.
ARG BIN=source

# Build React frontend
FROM node:20-alpine AS frontend
WORKDIR /web
COPY web/ .
RUN cd ui-framework && npm ci --ignore-scripts && npm run build
RUN cd app && npm ci --ignore-scripts && npm run build

FROM golang:1.27-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
# go.mod replaces veepin with the patched in-tree copy, and the shared core
# module with ./core
COPY third_party/ third_party/
COPY core/go.* core/
RUN go mod download
ARG CACHEBUST=0
COPY . .
# Copy built frontend into static dir before Go embed
COPY --from=frontend /web/app/dist/ internal/web/static/
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /hy2scale ./cmd/node

FROM scratch AS bin-source
COPY --from=builder /hy2scale /hy2scale

FROM scratch AS bin-release
ARG TARGETARCH
COPY release/hy2scale-linux-${TARGETARCH} /hy2scale
COPY release/hy2scale-linux-${TARGETARCH}.sig /hy2scale.sig

FROM bin-${BIN} AS bin

# Build iptables 1.8.3 from source (Alpine's 1.8.10 incompatible with some kernels)
FROM alpine:3.19 AS iptables-builder
RUN apk add --no-cache build-base curl linux-headers libnftnl-dev libmnl-dev
# sha256 of the release tarball (its netfilter.org GPG signature checked when pinned)
RUN curl -fsSL -o /tmp/iptables.tar.bz2 https://www.netfilter.org/projects/iptables/files/iptables-1.8.3.tar.bz2 \
    && echo "a23cac034181206b4545f4e7e730e76e08b5f3dd78771ba9645a6756de9cdd80  /tmp/iptables.tar.bz2" | sha256sum -c - \
    && tar xjf /tmp/iptables.tar.bz2 -C /tmp
RUN cd /tmp/iptables-1.8.3 && ./configure --prefix=/usr --sbindir=/sbin \
    --disable-nftables --enable-static --disable-shared \
    && make -j$(nproc) && make DESTDIR=/out install


FROM alpine:3.19
RUN apk add --no-cache ca-certificates iproute2 libmnl libnftnl conntrack-tools
# Install Alpine's iptables for nft mode (Docker compat)
RUN apk add --no-cache iptables ipset
# Overlay with compiled iptables-legacy 1.8.3 for kernel compat
COPY --from=iptables-builder /out/sbin/xtables-legacy-multi /sbin/xtables-legacy-multi
RUN ln -sf /sbin/xtables-legacy-multi /sbin/iptables-legacy \
    && ln -sf /sbin/xtables-legacy-multi /sbin/iptables-legacy-restore \
    && ln -sf /sbin/xtables-legacy-multi /sbin/iptables-legacy-save \
    && ln -sf /sbin/xtables-legacy-multi /sbin/ip6tables-legacy \
    && ln -sf /sbin/xtables-legacy-multi /sbin/ip6tables-legacy-restore \
    && ln -sf /sbin/xtables-legacy-multi /sbin/ip6tables-legacy-save
COPY --from=bin / /usr/local/bin/
RUN chmod 0755 /usr/local/bin/hy2scale
# Platform compatibility: iKuai iptables bundle, per-arch.
# amd64 iKuai = uClibc; arm64 iKuai = musl. Each bundle contains the
# arch-matching `xtables-legacy-multi` plus its libc + iptables shared
# libs. TARGETARCH is set automatically by `docker buildx build
# --platform linux/<arch>` and matches Go's GOARCH ("amd64", "arm64").
ARG TARGETARCH
COPY internal/platform/ikuai-iptables/bundle-${TARGETARCH}.tar.gz /opt/platform-compat/ikuai-iptables.tar.gz
VOLUME /data
ENTRYPOINT ["hy2scale", "--data", "/data"]
