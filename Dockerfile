FROM golang:1.24-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
ARG CACHEBUST=0
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /hy2scale ./cmd/node

# Build strongswan 5.8.4 (5.9.x has L2TP transport mode xfrm outbound bug)
FROM alpine:3.19 AS swan-builder
RUN apk add --no-cache build-base curl linux-headers openssl-dev
RUN curl -sL https://download.strongswan.org/strongswan-5.8.4.tar.bz2 | tar xj -C /tmp
RUN cd /tmp/strongswan-5.8.4 && ./configure --prefix=/usr --sysconfdir=/etc \
    --disable-gmp --enable-openssl --enable-md4 --enable-eap-mschapv2 --enable-eap-identity \
    --enable-xauth-generic --enable-farp --enable-dhcp --enable-unity \
    --enable-vici --enable-swanctl --enable-stroke --enable-updown \
    --enable-kernel-libipsec \
    && make -j$(nproc) && make DESTDIR=/out install

# Build iptables 1.8.3 from source (Alpine's 1.8.10 incompatible with some kernels)
FROM alpine:3.19 AS iptables-builder
RUN apk add --no-cache build-base curl linux-headers libnftnl-dev libmnl-dev
RUN curl -sL https://www.netfilter.org/projects/iptables/files/iptables-1.8.3.tar.bz2 | tar xj -C /tmp
RUN cd /tmp/iptables-1.8.3 && ./configure --prefix=/usr --sbindir=/sbin \
    --disable-nftables --enable-static --disable-shared \
    && make -j$(nproc) && make DESTDIR=/out install


FROM alpine:3.19
RUN apk add --no-cache ca-certificates xl2tpd ppp iproute2 openssl libmnl libnftnl conntrack-tools
# Install Alpine's iptables for nft mode (Docker compat)
RUN apk add --no-cache iptables
# Overlay with compiled iptables-legacy 1.8.3 for kernel compat
COPY --from=iptables-builder /out/sbin/xtables-legacy-multi /sbin/xtables-legacy-multi
RUN ln -sf /sbin/xtables-legacy-multi /sbin/iptables-legacy \
    && ln -sf /sbin/xtables-legacy-multi /sbin/iptables-legacy-restore \
    && ln -sf /sbin/xtables-legacy-multi /sbin/iptables-legacy-save
COPY --from=swan-builder /out/usr/ /usr/
COPY --from=swan-builder /out/etc/strongswan.conf /etc/strongswan.conf
COPY --from=swan-builder /out/etc/strongswan.d/ /etc/strongswan.d/
COPY --from=swan-builder /out/etc/ipsec.conf /etc/ipsec.conf.default
COPY --from=swan-builder /out/etc/ipsec.d/ /etc/ipsec.d/
COPY --from=builder /hy2scale /usr/local/bin/hy2scale
VOLUME /data
ENTRYPOINT ["hy2scale"]
